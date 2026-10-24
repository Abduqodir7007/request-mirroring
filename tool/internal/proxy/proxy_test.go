package proxy

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Abduqodir7007/request-mirroring/tool/internal/metrics"
)

func newProxy(t *testing.T, py, goSrv http.HandlerFunc, timeout time.Duration) (*Proxy, *metrics.Registry, *httptest.Server) {
	t.Helper()
	pys := httptest.NewServer(py)
	t.Cleanup(pys.Close)
	gos := httptest.NewServer(goSrv)
	t.Cleanup(gos.Close)
	pu, _ := url.Parse(pys.URL)
	gu, _ := url.Parse(gos.URL)
	reg := metrics.NewRegistry()
	p := New(Options{Service: "bot", Primary: pu, Shadow: gu, ShadowTimeout: timeout, Metrics: reg,
		Comparer: NewComparer([]string{"created_at"}), Masker: NewMasker(`(?i)token`)})
	front := httptest.NewServer(p)
	t.Cleanup(front.Close)
	return p, reg, front
}

func waitCompare(t *testing.T, reg *metrics.Registry, route, result string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if reg.Value("shadow_compare_total", "bot", route, result) >= 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	var b bytes.Buffer
	reg.Write(&b)
	t.Fatalf("no %s for %s\n%s", result, route, b.String())
}

func TestClientGetsPrimaryAndMatch(t *testing.T) {
	var goBody atomic.Value
	_, reg, front := newProxy(t,
		func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			if string(b) != `{"q":1}` {
				t.Errorf("python body %q", b)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"a":1,"created_at":"x","list":[1,2]}`))
		},
		func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			goBody.Store(string(b))
			if r.Header.Get("X-Shadow-Request") != "1" {
				t.Error("missing shadow header")
			}
			_, _ = w.Write([]byte(`{"list":[1,2],"a":1.0,"created_at":"y"}`))
		}, time.Second)

	resp, err := http.Post(front.URL+"/api/orders/42/", "application/json", strings.NewReader(`{"q":1}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != `{"a":1,"created_at":"x","list":[1,2]}` {
		t.Fatalf("client got %q", body)
	}
	waitCompare(t, reg, "/api/orders/{id}/", ResultMatch)
	if goBody.Load() != `{"q":1}` {
		t.Fatalf("go body %v", goBody.Load())
	}
}

func TestStatusDiffAndMasking(t *testing.T) {
	p, reg, front := newProxy(t,
		func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"token":"secret-123","ok":true}`))
		},
		func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"detail":"no"}`))
		}, time.Second)
	resp, _ := http.Get(front.URL + "/login/?token=abc")
	resp.Body.Close()
	waitCompare(t, reg, "/login/", ResultStatusDiff)
	if reg.Value("shadow_status_diff_total", "bot", "/login/", "200", "401") != 1 {
		t.Fatal("status diff counter")
	}
	d := p.opts.Diffs.List()
	if len(d) != 1 || strings.Contains(d[0].PyBody, "secret-123") || strings.Contains(d[0].Path, "abc") {
		t.Fatalf("diff not masked: %+v", d)
	}
}

func TestSlowGoDoesNotDelayClient(t *testing.T) {
	_, reg, front := newProxy(t,
		func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) },
		func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-time.After(2 * time.Second):
			case <-r.Context().Done():
			}
		}, 300*time.Millisecond)
	start := time.Now()
	resp, _ := http.Get(front.URL + "/x")
	resp.Body.Close()
	if time.Since(start) > 200*time.Millisecond {
		t.Fatalf("client waited %s", time.Since(start))
	}
	waitCompare(t, reg, "/x", ResultGoTimeout)
}

func TestBodyDiffPaths(t *testing.T) {
	c := NewComparer(nil)
	res, paths := c.Compare(200, []byte(`{"a":{"b":1,"c":[1,2]},"d":"x"}`), 200, []byte(`{"a":{"b":2,"c":[1]},"e":1}`))
	if res != ResultBodyDiff {
		t.Fatal(res)
	}
	got := strings.Join(paths, "|")
	for _, want := range []string{"$.a.b", "$.a.c: length 2 != 1", "$.d: only in primary", "$.e: only in shadow"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %s", want, got)
		}
	}
}

func TestGoBodyReplace(t *testing.T) {
	c := NewComparer(nil).WithReplace("core-media-shadow=core-media, auth-media-shadow=auth-media")
	res, paths := c.Compare(200, []byte(`{"u":"https://f/core-media/a.png"}`), 200, []byte(`{"u":"https://f/core-media-shadow/a.png"}`))
	if res != ResultMatch {
		t.Fatalf("%s %v", res, paths)
	}
}

func TestRoutesNormalize(t *testing.T) {
	r := NewRoutes(10)
	cases := map[string]string{
		"/hook/v1/webhook/sender-bot/7":             "/hook/v1/webhook/sender-bot/{id}",
		"/api/v1/orders/123/":                       "/api/v1/orders/{id}/",
		"/a/0f8fad5b-d9cb-469f-a165-70867728950e/b": "/a/{uuid}/b",
	}
	for in, want := range cases {
		if got := r.Normalize(in); got != want {
			t.Errorf("%s -> %s, want %s", in, got, want)
		}
	}
}

func TestAdminAllow(t *testing.T) {
	allow, err := ParseAllow("127.0.0.1,10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	if !allowed("127.0.0.1:5000", allow) || !allowed("10.2.3.4:1", allow) || allowed("8.8.8.8:1", allow) {
		t.Fatal("allow list")
	}
}
