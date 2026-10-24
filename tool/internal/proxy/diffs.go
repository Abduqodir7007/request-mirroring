package proxy

import (
	"encoding/json"
	"html/template"
	"net/http"
	"sync"
	"time"
)

type Diff struct {
	Time     time.Time `json:"time"`
	Service  string    `json:"service"`
	Method   string    `json:"method"`
	Route    string    `json:"route"`
	Path     string    `json:"path"`
	Result   string    `json:"result"`
	PyStatus int       `json:"primary_status"`
	GoStatus int       `json:"shadow_status"`
	PyMillis float64   `json:"primary_ms"`
	GoMillis float64   `json:"shadow_ms"`
	Paths    []string  `json:"paths,omitempty"`
	PyBody   string    `json:"primary_body,omitempty"`
	GoBody   string    `json:"shadow_body,omitempty"`
	GoError  string    `json:"shadow_error,omitempty"`
}

type DiffStore struct {
	mu    sync.Mutex
	items []Diff
	next  int
	full  bool
}

func NewDiffStore(size int) *DiffStore {
	if size <= 0 {
		size = 1000
	}
	return &DiffStore{items: make([]Diff, size)}
}

func (s *DiffStore) Add(d Diff) {
	s.mu.Lock()
	s.items[s.next] = d
	s.next = (s.next + 1) % len(s.items)
	if s.next == 0 {
		s.full = true
	}
	s.mu.Unlock()
}

func (s *DiffStore) List() []Diff {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.next
	if s.full {
		n = len(s.items)
	}
	out := make([]Diff, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, s.items[(s.next-i+len(s.items))%len(s.items)])
	}
	return out
}

var page = template.Must(template.New("diffs").Funcs(template.FuncMap{
	"ts": func(t time.Time) string { return t.Format("2006-01-02 15:04:05") },
}).Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>shadow diffs</title>
<style>
:root{--bg:#fff;--fg:#1d1d1f;--muted:#6e6e73;--line:#e5e5ea;--py:#3274d9;--go:#00add8;--bad:#d93025;--warn:#e37400}
@media (prefers-color-scheme:dark){:root{--bg:#111;--fg:#eee;--muted:#999;--line:#333}}
body{margin:0;padding:16px;background:var(--bg);color:var(--fg);font:14px/1.45 system-ui,sans-serif}
h1{font-size:18px;margin:0 0 4px}.muted{color:var(--muted)}
details{border:1px solid var(--line);border-radius:8px;margin:8px 0;padding:8px 12px}
summary{cursor:pointer;display:flex;gap:12px;flex-wrap:wrap;align-items:baseline}
.r{font-weight:600}.status_diff,.shadow_error,.shadow_timeout{color:var(--bad)}.body_diff{color:var(--warn)}
.cols{display:grid;grid-template-columns:1fr 1fr;gap:12px;margin-top:8px}
@media (max-width:720px){.cols{grid-template-columns:1fr}}
pre{margin:0;padding:8px;border:1px solid var(--line);border-radius:6px;overflow:auto;max-height:420px;white-space:pre-wrap;word-break:break-word;font-size:12px}
.py{border-left:3px solid var(--py)}.go{border-left:3px solid var(--go)}
code{font-size:12px}
</style></head><body>
<h1>shadow — primary ↔ shadow differences</h1>
<p class="muted">Last {{len .}} differences (newest first). Secret fields are masked as ***. <a href="diffs.json">JSON</a></p>
{{range .}}<details><summary><span class="r {{.Result}}">{{.Result}}</span><code>{{.Method}} {{.Route}}</code>
<span>primary {{.PyStatus}} · {{printf "%.0f" .PyMillis}} ms</span><span>shadow {{.GoStatus}} · {{printf "%.0f" .GoMillis}} ms</span>
<span class="muted">{{.Service}} · {{ts .Time}}</span></summary>
<p class="muted"><code>{{.Path}}</code></p>
{{if .GoError}}<p class="shadow_error">shadow error: {{.GoError}}</p>{{end}}
{{if .Paths}}<p>Differing paths: {{range .Paths}}<code>{{.}}</code> {{end}}</p>{{end}}
<div class="cols"><div><div class="muted">primary</div><pre class="py">{{.PyBody}}</pre></div><div><div class="muted">shadow</div><pre class="go">{{.GoBody}}</pre></div></div>
</details>{{else}}<p>No differences yet.</p>{{end}}
</body></html>`))

func (s *DiffStore) HTML(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = page.Execute(w, s.List())
}

func (s *DiffStore) JSON(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.List())
}
