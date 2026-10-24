package faketg

import (
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type apiResp struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
}

func newServer(t *testing.T, real http.HandlerFunc) (*httptest.Server, *Recorder, *Mirror, string) {
	t.Helper()
	rs := httptest.NewServer(real)
	t.Cleanup(rs.Close)
	path := filepath.Join(t.TempDir(), "calls.jsonl")
	rec, err := NewRecorder(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rec.Close() })
	mir := NewMirror(MirrorOptions{RealAPI: rs.URL, Token: "999:mirror", ChatID: "-1", Interval: time.Hour})
	s := New(Options{RealAPI: rs.URL, Passthrough: true, Recorder: rec, Mirror: mir})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, rec, mir, path
}

func post(t *testing.T, u string, form url.Values) apiResp {
	t.Helper()
	resp, err := http.PostForm(u, form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var r apiResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestWritesNeverReachRealTelegram(t *testing.T) {
	var realCalls atomic.Int32
	srv, _, _, path := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		realCalls.Add(1)
		if !strings.HasSuffix(r.URL.Path, "/getMe") && !strings.HasSuffix(r.URL.Path, "/sendMessage") {
			t.Errorf("real API got %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"id":123,"is_bot":true,"first_name":"x","username":"real_bot"}}`))
	})
	base := srv.URL + "/bot123:abc/"
	me := post(t, base+"getMe", nil)
	if !strings.Contains(string(me.Result), "real_bot") {
		t.Fatalf("getMe passthrough: %s", me.Result)
	}
	before := realCalls.Load()
	for _, m := range []string{"sendMessage", "deleteMessage", "setWebhook", "editMessageText", "answerCallbackQuery", "copyMessage", "deleteWebhook"} {
		r := post(t, base+m, url.Values{"chat_id": {"-1001234567890"}, "text": {"salom"}, "message_id": {"5"}, "url": {"https://evil"}})
		if !r.OK {
			t.Fatalf("%s not ok", m)
		}
	}
	if realCalls.Load() != before {
		t.Fatalf("writes reached real API: %d", realCalls.Load()-before)
	}
	data, _ := os.ReadFile(path)
	if strings.Count(string(data), "\n") != 7 || !strings.Contains(string(data), `"bot_name":"real_bot"`) {
		t.Fatalf("recorder: %s", data)
	}
}

func TestSendMessageShapeAndIDs(t *testing.T) {
	srv, _, _, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) })
	base := srv.URL + "/bot55:x/"
	var buf strings.Builder
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("chat_id", "-1001111111111")
	_ = mw.WriteField("text", "e'lon")
	_ = mw.WriteField("message_thread_id", "77")
	_ = mw.WriteField("reply_markup", `{"inline_keyboard":[[{"text":"a","callback_data":"b"}]]}`)
	mw.Close()
	resp, err := http.Post(base+"sendMessage", mw.FormDataContentType(), strings.NewReader(buf.String()))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var r struct {
		OK     bool `json:"ok"`
		Result struct {
			MessageID int64 `json:"message_id"`
			Chat      struct {
				ID   int64  `json:"id"`
				Type string `json:"type"`
			} `json:"chat"`
			Thread int64 `json:"message_thread_id"`
			Markup any   `json:"reply_markup"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatal(err)
	}
	if r.Result.MessageID != 900_000_001 || r.Result.Chat.ID != -1001111111111 || r.Result.Chat.Type != "supergroup" || r.Result.Thread != 77 || r.Result.Markup == nil {
		t.Fatalf("bad message: %s", body)
	}
	second := post(t, base+"sendMessage", url.Values{"chat_id": {"-1001111111111"}, "text": {"2"}})
	if !strings.Contains(string(second.Result), "900000002") {
		t.Fatalf("id not incremented: %s", second.Result)
	}
	me := post(t, base+"getMe", nil)
	if !strings.Contains(string(me.Result), "shadow_55_bot") {
		t.Fatalf("getMe fallback: %s", me.Result)
	}
}

func TestGetUpdatesWaitsAndReturnsEmpty(t *testing.T) {
	srv, _, _, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) { t.Error("real API called") })
	start := time.Now()
	r := post(t, srv.URL+"/bot1:x/getUpdates", url.Values{"timeout": {"1"}})
	if string(r.Result) != "[]" || time.Since(start) < 900*time.Millisecond {
		t.Fatalf("getUpdates: %s after %s", r.Result, time.Since(start))
	}
}

func TestAIStubAndSinkStayLocal(t *testing.T) {
	srv, _, _, path := newServer(t, func(w http.ResponseWriter, _ *http.Request) { t.Error("real API called") })
	body := `{"model":"m","messages":[{"role":"system","content":[{"type":"text","text":"p"}]},{"role":"user","content":[{"type":"text","text":"Toshkent - Buxoro 20t"}]}]}`
	resp, err := http.Post(srv.URL+"/ai/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&c)
	resp.Body.Close()
	if resp.StatusCode != 200 || len(c.Choices) != 1 || c.Choices[0].Message.Content != "[]" {
		t.Fatalf("ai stub: %d %+v", resp.StatusCode, c)
	}
	resp, err = http.Post(srv.URL+"/sink/api-drivers", "application/json", strings.NewReader(`{"id":1}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("sink: %d", resp.StatusCode)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "Toshkent - Buxoro 20t") || !strings.Contains(string(data), `"method":"POST api-drivers"`) {
		t.Fatalf("recorder: %s", data)
	}
}

func TestMirrorDigest(t *testing.T) {
	var got atomic.Value
	_, _, mir, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got.Store(r.PostForm.Get("text") + "|" + r.PostForm.Get("chat_id") + "|" + r.URL.Path)
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	})
	mir.Add(Event{Bot: "1", BotName: "collector_bot", Method: "sendMessage", ChatID: "-100", Text: "Toshkent - Samarqand 20t"})
	mir.Add(Event{Bot: "1", Method: "deleteMessage", ChatID: "-100"})
	mir.flush(t.Context())
	s, _ := got.Load().(string)
	if !strings.Contains(s, "2 calls") || !strings.Contains(s, "Toshkent - Samarqand") || !strings.Contains(s, "|-1|/bot999:mirror/sendMessage") {
		t.Fatalf("digest: %q", s)
	}
	got.Store("")
	mir.flush(t.Context())
	if got.Load() != "" {
		t.Fatal("empty digest sent")
	}
}
