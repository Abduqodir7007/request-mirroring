package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Abduqodir7007/request-mirroring/tool/internal/metrics"
)

type Options struct {
	Service       string
	Primary       *url.URL
	Shadow        *url.URL
	ShadowTimeout time.Duration
	MaxBody       int64
	MaxCompare    int
	MaxInFlight   int
	SkipPaths     *regexp.Regexp
	ShadowMethods map[string]bool
	Comparer      *Comparer
	Masker        *Masker
	Diffs         *DiffStore
	Metrics       *metrics.Registry
	Logger        *slog.Logger
	Transport     http.RoundTripper
}

type Proxy struct {
	opts      Options
	primary   *httputil.ReverseProxy
	client    *http.Client
	routes    *Routes
	slots     chan struct{}
	requests  *metrics.Counter
	durations *metrics.Histogram
	compares  *metrics.Counter
	statuses  *metrics.Counter
	inflight  *metrics.Counter
}

type ctxKey struct{}

type capture struct {
	status int
	body   bytes.Buffer
	limit  int
	over   bool
	stream bool
}

type shadowResult struct {
	status  int
	body    []byte
	dur     time.Duration
	err     error
	timeout bool
	over    bool
}

func New(opts Options) *Proxy {
	if opts.ShadowTimeout == 0 {
		opts.ShadowTimeout = 5 * time.Second
	}
	if opts.MaxBody == 0 {
		opts.MaxBody = 10 << 20
	}
	if opts.MaxCompare == 0 {
		opts.MaxCompare = 2 << 20
	}
	if opts.MaxInFlight == 0 {
		opts.MaxInFlight = 64
	}
	if opts.Comparer == nil {
		opts.Comparer = NewComparer(nil)
	}
	if opts.Masker == nil {
		opts.Masker = NewMasker("")
	}
	if opts.Diffs == nil {
		opts.Diffs = NewDiffStore(1000)
	}
	if opts.Metrics == nil {
		opts.Metrics = metrics.NewRegistry()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Transport == nil {
		opts.Transport = &http.Transport{
			Proxy:               nil,
			DialContext:         (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			MaxIdleConns:        256,
			MaxIdleConnsPerHost: 128,
			IdleConnTimeout:     90 * time.Second,
		}
	}
	reg := opts.Metrics
	p := &Proxy{
		opts:      opts,
		client:    &http.Client{Transport: opts.Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		routes:    NewRoutes(500),
		slots:     make(chan struct{}, opts.MaxInFlight),
		requests:  reg.Counter("shadow_requests_total", "Requests answered by each backend", "service", "backend", "route", "method", "code"),
		durations: reg.Histogram("shadow_request_duration_seconds", "Full response time per backend", nil, "service", "backend", "route", "method"),
		compares:  reg.Counter("shadow_compare_total", "Primary vs shadow comparison results", "service", "route", "result"),
		statuses:  reg.Counter("shadow_status_diff_total", "Status code differences", "service", "route", "primary_code", "shadow_code"),
		inflight:  reg.Counter("shadow_dropped_total", "Shadow requests not sent", "service", "reason"),
	}
	p.primary = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(opts.Primary)
			pr.Out.Host = pr.In.Host
			for _, h := range []string{"X-Forwarded-For", "X-Forwarded-Proto", "X-Forwarded-Host", "Forwarded"} {
				if v, ok := pr.In.Header[h]; ok {
					pr.Out.Header[h] = v
				}
			}
		},
		Transport:     opts.Transport,
		FlushInterval: -1,
		ModifyResponse: func(resp *http.Response) error {
			c, _ := resp.Request.Context().Value(ctxKey{}).(*capture)
			if c == nil {
				return nil
			}
			c.status = resp.StatusCode
			if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") || resp.StatusCode == http.StatusSwitchingProtocols {
				c.stream = true
				return nil
			}
			resp.Body = &teeBody{rc: resp.Body, c: c}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if c, _ := r.Context().Value(ctxKey{}).(*capture); c != nil {
				c.status = http.StatusBadGateway
			}
			opts.Logger.Warn("primary error", "path", r.URL.Path, "error", err.Error())
			w.WriteHeader(http.StatusBadGateway)
		},
	}
	return p
}

type teeBody struct {
	rc io.ReadCloser
	c  *capture
}

func (t *teeBody) Read(b []byte) (int, error) {
	n, err := t.rc.Read(b)
	if n > 0 && !t.c.over {
		if t.c.body.Len()+n > t.c.limit {
			t.c.over = true
		} else {
			t.c.body.Write(b[:n])
		}
	}
	return n, err
}

func (t *teeBody) Close() error { return t.rc.Close() }

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	route := p.routes.Normalize(r.URL.Path)
	method := r.Method
	shadowWanted := p.wantShadow(r)

	var reqBody []byte
	if shadowWanted && r.Body != nil && r.Body != http.NoBody {
		buf, err := io.ReadAll(io.LimitReader(r.Body, p.opts.MaxBody+1))
		if err != nil {
			http.Error(w, "bad request body", http.StatusBadRequest)
			return
		}
		if int64(len(buf)) > p.opts.MaxBody {
			shadowWanted = false
			p.inflight.Inc(p.opts.Service, "body_too_large")
			r.Body = struct {
				io.Reader
				io.Closer
			}{io.MultiReader(bytes.NewReader(buf), r.Body), r.Body}
		} else {
			reqBody = buf
			r.Body = io.NopCloser(bytes.NewReader(buf))
			r.ContentLength = int64(len(buf))
		}
	}

	var shadowCh chan shadowResult
	if shadowWanted {
		select {
		case p.slots <- struct{}{}:
			shadowCh = make(chan shadowResult, 1)
			out := p.shadowRequest(r, reqBody)
			go func() {
				defer func() { <-p.slots }()
				shadowCh <- p.doShadow(out)
			}()
		default:
			p.inflight.Inc(p.opts.Service, "busy")
		}
	}

	c := &capture{limit: p.opts.MaxCompare}
	start := time.Now()
	p.primary.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, c)))
	pyDur := time.Since(start)
	if c.status == 0 {
		c.status = http.StatusOK
	}
	p.requests.Inc(p.opts.Service, "primary", route, method, strconv.Itoa(c.status))
	p.durations.Observe(pyDur.Seconds(), p.opts.Service, "primary", route, method)

	if shadowCh == nil {
		return
	}
	path := p.safePath(r.URL)
	go p.finish(route, method, path, c, pyDur, shadowCh)
}

func (p *Proxy) wantShadow(r *http.Request) bool {
	if p.opts.Shadow == nil {
		return false
	}
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") {
		return false
	}
	if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		return false
	}
	if p.opts.SkipPaths != nil && p.opts.SkipPaths.MatchString(r.URL.Path) {
		return false
	}
	if len(p.opts.ShadowMethods) > 0 && !p.opts.ShadowMethods[r.Method] {
		return false
	}
	if r.ContentLength > p.opts.MaxBody {
		return false
	}
	return true
}

func (p *Proxy) shadowRequest(r *http.Request, body []byte) *http.Request {
	u := *p.opts.Shadow
	u.Path = singleJoin(p.opts.Shadow.Path, r.URL.Path)
	u.RawQuery = r.URL.RawQuery
	ctx, cancel := context.WithTimeout(context.Background(), p.opts.ShadowTimeout)
	var rd io.Reader = http.NoBody
	if body != nil {
		rd = bytes.NewReader(body)
	}
	out, _ := http.NewRequestWithContext(ctx, r.Method, u.String(), rd)
	out.Header = r.Header.Clone()
	for _, h := range hopHeaders {
		out.Header.Del(h)
	}
	out.Header.Set("X-Shadow-Request", "1")
	out.Host = r.Host
	if body != nil {
		out.ContentLength = int64(len(body))
	}
	out = out.WithContext(context.WithValue(ctx, cancelKey{}, cancel))
	return out
}

type cancelKey struct{}

func (p *Proxy) doShadow(req *http.Request) shadowResult {
	if cancel, ok := req.Context().Value(cancelKey{}).(context.CancelFunc); ok {
		defer cancel()
	}
	start := time.Now()
	resp, err := p.client.Do(req)
	if err != nil {
		return shadowResult{err: err, dur: time.Since(start), timeout: errors.Is(err, context.DeadlineExceeded) || isTimeout(err)}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(p.opts.MaxCompare)+1))
	_, _ = io.Copy(io.Discard, resp.Body)
	res := shadowResult{status: resp.StatusCode, body: body, dur: time.Since(start)}
	if len(body) > p.opts.MaxCompare {
		res.over, res.body = true, nil
	}
	if err != nil {
		res.err = err
		res.timeout = errors.Is(err, context.DeadlineExceeded) || isTimeout(err)
	}
	return res
}

func (p *Proxy) finish(route, method, path string, c *capture, pyDur time.Duration, ch chan shadowResult) {
	res := <-ch
	svc := p.opts.Service
	if res.err == nil {
		p.requests.Inc(svc, "shadow", route, method, strconv.Itoa(res.status))
		p.durations.Observe(res.dur.Seconds(), svc, "go", route, method)
	}
	d := Diff{Time: time.Now(), Service: svc, Method: method, Route: route, Path: path,
		PyStatus: c.status, GoStatus: res.status, PyMillis: ms(pyDur), GoMillis: ms(res.dur)}
	switch {
	case c.stream || c.over || res.over:
		p.compares.Inc(svc, route, ResultSkipped)
		return
	case res.timeout:
		d.Result, d.GoError = ResultGoTimeout, "timeout"
	case res.err != nil:
		d.Result, d.GoError = ResultGoError, res.err.Error()
	default:
		d.Result, d.Paths = p.opts.Comparer.Compare(c.status, c.body.Bytes(), res.status, res.body)
	}
	if d.Result != ResultMatch {
		d.PyBody = p.opts.Masker.Body(c.body.Bytes(), 8192)
		d.GoBody = p.opts.Masker.Body(res.body, 8192)
		p.opts.Diffs.Add(d)
	}
	if d.Result == ResultStatusDiff {
		p.statuses.Inc(svc, route, strconv.Itoa(c.status), strconv.Itoa(res.status))
	}
	p.compares.Inc(svc, route, d.Result)
}

func (p *Proxy) safePath(u *url.URL) string {
	q := u.Query()
	for k := range q {
		if p.opts.Masker.Key(k) {
			q.Set(k, "***")
		}
	}
	path := u.Path
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	return path
}

var hopHeaders = []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"}

func singleJoin(a, b string) string {
	switch {
	case a == "" || a == "/":
		return b
	case strings.HasSuffix(a, "/") && strings.HasPrefix(b, "/"):
		return a + b[1:]
	case !strings.HasSuffix(a, "/") && !strings.HasPrefix(b, "/"):
		return a + "/" + b
	}
	return a + b
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
