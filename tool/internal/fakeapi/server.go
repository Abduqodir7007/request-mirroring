package fakeapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Abduqodir7007/request-mirroring/tool/internal/metrics"
)

var readMethods = map[string]bool{
	"getme": true, "getchat": true, "getchatmember": true, "getchatadministrators": true,
	"getchatmembercount": true, "getmycommands": true,
}

type Options struct {
	RealAPI        string
	Passthrough    bool
	PassthroughTTL time.Duration
	MaxPollWait    time.Duration
	FirstMessageID int64
	Recorder       *Recorder
	Mirror         *Mirror
	Metrics        *metrics.Registry
	Logger         *slog.Logger
	HTTPClient     *http.Client
}

type Server struct {
	opts    Options
	calls   *metrics.Counter
	mu      sync.Mutex
	nextID  map[string]int64
	cache   map[string]cached
	botName map[string]string
}

type cached struct {
	body    []byte
	expires time.Time
}

func New(opts Options) *Server {
	if opts.RealAPI == "" {
		opts.RealAPI = "https://api.telegram.org"
	}
	if opts.PassthroughTTL == 0 {
		opts.PassthroughTTL = 10 * time.Minute
	}
	if opts.MaxPollWait == 0 {
		opts.MaxPollWait = 25 * time.Second
	}
	if opts.FirstMessageID == 0 {
		opts.FirstMessageID = 900_000_000
	}
	if opts.Metrics == nil {
		opts.Metrics = metrics.NewRegistry()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Server{
		opts:    opts,
		calls:   opts.Metrics.Counter("fakeapi_calls_total", "Bot API calls received by the fake Telegram", "bot", "method", "mode"),
		nextID:  map[string]int64{},
		cache:   map[string]cached{},
		botName: map[string]string{},
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.Handle("/metrics", s.opts.Metrics.Handler())
	mux.HandleFunc("/myid/", s.myid)
	mux.HandleFunc("/ai/", s.ai)
	mux.HandleFunc("/sink/", s.sink)
	mux.HandleFunc("/", s.botAPI)
	return mux
}

func (s *Server) ai(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req)
	text := ""
	for _, m := range req.Messages {
		if m.Role == "user" && len(m.Content) > 0 {
			text = m.Content[0].Text
		}
	}
	s.calls.Inc("ai", "chat.completions", "fake")
	if s.opts.Recorder != nil {
		s.opts.Recorder.Write(Event{Time: time.Now(), Bot: "ai", Method: "chat.completions", Mode: "fake", Text: clip(text, 500)})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": "shadow", "object": "chat.completion", "model": "shadow",
		"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": "[]"}}},
	})
}

func (s *Server) sink(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/sink/")
	s.calls.Inc("sink", name, "fake")
	if s.opts.Recorder != nil {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
		s.opts.Recorder.Write(Event{Time: time.Now(), Bot: "sink", Method: r.Method + " " + name, Mode: "fake", Text: string(body)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "shadow": true})
}

func (s *Server) myid(w http.ResponseWriter, r *http.Request) {
	s.calls.Inc("myid", strings.TrimPrefix(r.URL.Path, "/myid"), "fake")
	if s.opts.Recorder != nil {
		s.opts.Recorder.Write(Event{Time: time.Now(), Bot: "myid", Method: r.Method + " " + r.URL.Path, Mode: "fake"})
	}
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "shadow: this external call is stubbed"})
}

func (s *Server) botAPI(w http.ResponseWriter, r *http.Request) {
	token, method, ok := splitPath(r.URL.Path)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error_code": 404, "description": "Not Found"})
		return
	}
	bot := botID(token)
	params := readParams(r)
	lower := strings.ToLower(method)

	if readMethods[lower] && s.opts.Passthrough {
		if body, ok := s.passthrough(r.Context(), token, method, params); ok {
			s.calls.Inc(bot, method, "passthrough")
			if lower == "getme" {
				s.rememberName(bot, body)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
			return
		}
	}

	if lower == "getupdates" {
		s.calls.Inc(bot, method, "fake")
		s.waitPoll(r.Context(), params["timeout"])
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": []any{}})
		return
	}

	result, assigned := s.answer(bot, lower, params)
	s.calls.Inc(bot, method, "fake")
	ev := Event{Time: time.Now(), Bot: bot, BotName: s.name(bot), Method: method, Mode: "fake",
		ChatID: params["chat_id"], ThreadID: params["message_thread_id"], MessageID: params["message_id"],
		AssignedID: assigned, Text: firstNonEmpty(params["text"], params["caption"]),
		ReplyMarkup: params["reply_markup"], Params: trimParams(params)}
	if s.opts.Recorder != nil {
		s.opts.Recorder.Write(ev)
	}
	if s.opts.Mirror != nil {
		s.opts.Mirror.Add(ev)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
}

func (s *Server) answer(bot, method string, p map[string]string) (any, int64) {
	switch {
	case method == "getme":
		id, _ := strconv.ParseInt(bot, 10, 64)
		return map[string]any{"id": id, "is_bot": true, "first_name": "Shadow " + bot, "username": "shadow_" + bot + "_bot",
			"can_join_groups": true, "can_read_all_group_messages": false, "supports_inline_queries": false}, 0
	case method == "getchat":
		return chatObject(p["chat_id"]), 0
	case method == "getchatmember":
		uid, _ := strconv.ParseInt(p["user_id"], 10, 64)
		return map[string]any{"status": "member", "user": map[string]any{"id": uid, "is_bot": false, "first_name": "user"}}, 0
	case method == "getchatadministrators":
		return []any{}, 0
	case method == "getchatmembercount":
		return 0, 0
	case method == "getwebhookinfo":
		return map[string]any{"url": "", "has_custom_certificate": false, "pending_update_count": 0}, 0
	case method == "copymessage":
		id := s.next(p["chat_id"])
		return map[string]any{"message_id": id}, id
	case method == "copymessages" || method == "forwardmessages":
		return []any{map[string]any{"message_id": s.next(p["chat_id"])}}, 0
	case method == "sendmediagroup":
		n := 1
		var media []any
		if json.Unmarshal([]byte(p["media"]), &media) == nil && len(media) > 0 {
			n = len(media)
		}
		out := make([]any, 0, n)
		var first int64
		for i := 0; i < n; i++ {
			id := s.next(p["chat_id"])
			if i == 0 {
				first = id
			}
			out = append(out, s.message(bot, id, p))
		}
		return out, first
	case strings.HasPrefix(method, "send") || method == "forwardmessage":
		id := s.next(p["chat_id"])
		return s.message(bot, id, p), id
	case strings.HasPrefix(method, "edit") || method == "stopmessagelivelocation":
		if p["inline_message_id"] != "" {
			return true, 0
		}
		id, _ := strconv.ParseInt(p["message_id"], 10, 64)
		return s.message(bot, id, p), 0
	case method == "stoppoll":
		return map[string]any{"id": "0", "question": "", "options": []any{}, "total_voter_count": 0, "is_closed": true,
			"is_anonymous": true, "type": "regular", "allows_multiple_answers": false}, 0
	case method == "exportchatinvitelink":
		return "https://t.me/+shadow", 0
	case method == "createchatinvitelink" || method == "editchatinvitelink":
		return map[string]any{"invite_link": "https://t.me/+shadow", "creator": map[string]any{"id": 0, "is_bot": true, "first_name": "shadow"},
			"creates_join_request": false, "is_primary": false, "is_revoked": false}, 0
	}
	return true, 0
}

func (s *Server) message(bot string, id int64, p map[string]string) map[string]any {
	botNum, _ := strconv.ParseInt(bot, 10, 64)
	m := map[string]any{
		"message_id": id,
		"date":       time.Now().Unix(),
		"chat":       chatObject(p["chat_id"]),
		"from":       map[string]any{"id": botNum, "is_bot": true, "first_name": "Shadow " + bot, "username": s.name(bot)},
	}
	if t := p["text"]; t != "" {
		m["text"] = t
	}
	if c := p["caption"]; c != "" {
		m["caption"] = c
	}
	if th := p["message_thread_id"]; th != "" {
		if n, err := strconv.ParseInt(th, 10, 64); err == nil {
			m["message_thread_id"] = n
			m["is_topic_message"] = true
		}
	}
	if rm := p["reply_markup"]; rm != "" {
		var v any
		if json.Unmarshal([]byte(rm), &v) == nil {
			m["reply_markup"] = v
		}
	}
	return m
}

func (s *Server) next(chat string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.nextID[chat]
	if !ok {
		id = s.opts.FirstMessageID
	}
	id++
	s.nextID[chat] = id
	return id
}

func (s *Server) passthrough(ctx context.Context, token, method string, p map[string]string) ([]byte, bool) {
	key := token + "/" + strings.ToLower(method) + "?" + encodeSorted(p)
	s.mu.Lock()
	if c, ok := s.cache[key]; ok && time.Now().Before(c.expires) {
		s.mu.Unlock()
		return c.body, true
	}
	s.mu.Unlock()
	form := url.Values{}
	for k, v := range p {
		form.Set(k, v)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.opts.RealAPI+"/bot"+token+"/"+method, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, false
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.opts.HTTPClient.Do(req)
	if err != nil {
		s.opts.Logger.Warn("passthrough failed", "method", method, "error", err.Error())
		return nil, false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode >= 500 || resp.StatusCode == 429 {
		return nil, false
	}
	s.mu.Lock()
	if len(s.cache) > 5000 {
		s.cache = map[string]cached{}
	}
	s.cache[key] = cached{body: body, expires: time.Now().Add(s.opts.PassthroughTTL)}
	s.mu.Unlock()
	return body, true
}

func (s *Server) rememberName(bot string, body []byte) {
	var r struct {
		OK     bool `json:"ok"`
		Result struct {
			Username string `json:"username"`
		} `json:"result"`
	}
	if json.Unmarshal(body, &r) == nil && r.OK && r.Result.Username != "" {
		s.mu.Lock()
		s.botName[bot] = r.Result.Username
		s.mu.Unlock()
	}
}

func (s *Server) name(bot string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n, ok := s.botName[bot]; ok {
		return n
	}
	return "shadow_" + bot + "_bot"
}

func (s *Server) waitPoll(ctx context.Context, timeout string) {
	d := s.opts.MaxPollWait
	if n, err := strconv.Atoi(timeout); err == nil && time.Duration(n)*time.Second < d {
		d = time.Duration(n) * time.Second
	}
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func splitPath(path string) (token, method string, ok bool) {
	rest, found := strings.CutPrefix(path, "/bot")
	if !found {
		return "", "", false
	}
	token, method, ok = strings.Cut(rest, "/")
	if !ok || token == "" || method == "" || strings.Contains(method, "/") {
		return "", "", false
	}
	return token, method, true
}

func botID(token string) string {
	id, _, _ := strings.Cut(token, ":")
	if id == "" {
		return "unknown"
	}
	return id
}

func readParams(r *http.Request) map[string]string {
	p := map[string]string{}
	for k, v := range r.URL.Query() {
		p[k] = v[0]
	}
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	switch ct {
	case "application/json":
		body, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		var m map[string]json.RawMessage
		if json.Unmarshal(body, &m) == nil {
			for k, raw := range m {
				var str string
				if json.Unmarshal(raw, &str) == nil {
					p[k] = str
				} else {
					p[k] = string(bytes.TrimSpace(raw))
				}
			}
		}
	case "multipart/form-data":
		if r.ParseMultipartForm(8<<20) == nil {
			for k, v := range r.MultipartForm.Value {
				p[k] = v[0]
			}
			for k := range r.MultipartForm.File {
				p[k] = "(file)"
			}
		}
	default:
		if r.ParseForm() == nil {
			for k, v := range r.PostForm {
				p[k] = v[0]
			}
		}
	}
	return p
}

func chatObject(chatID string) map[string]any {
	if strings.HasPrefix(chatID, "@") {
		return map[string]any{"id": int64(-1), "type": "channel", "username": strings.TrimPrefix(chatID, "@")}
	}
	id, _ := strconv.ParseInt(chatID, 10, 64)
	typ := "private"
	switch {
	case id < -1000000000000:
		typ = "supergroup"
	case id < 0:
		typ = "group"
	}
	return map[string]any{"id": id, "type": typ}
}

func encodeSorted(p map[string]string) string {
	v := url.Values{}
	for k, val := range p {
		v.Set(k, val)
	}
	return v.Encode()
}

func trimParams(p map[string]string) map[string]string {
	out := make(map[string]string, len(p))
	for k, v := range p {
		if k == "text" || k == "caption" || k == "reply_markup" {
			continue
		}
		if len(v) > 500 {
			v = v[:500] + "…"
		}
		out[k] = v
	}
	return out
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
