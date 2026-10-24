package faketg

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Abduqodir7007/request-mirroring/tool/internal/metrics"
)

type MirrorOptions struct {
	RealAPI  string
	Token    string
	ChatID   string
	Interval time.Duration
	Examples int
	Logger   *slog.Logger
	Metrics  *metrics.Registry
	Client   *http.Client
}

type Mirror struct {
	opts   MirrorOptions
	mu     sync.Mutex
	counts map[string]int
	bots   map[string]int
	total  int
	sample []Event
	sent   *metrics.Counter
}

func NewMirror(opts MirrorOptions) *Mirror {
	if opts.RealAPI == "" {
		opts.RealAPI = "https://api.telegram.org"
	}
	if opts.Interval == 0 {
		opts.Interval = time.Minute
	}
	if opts.Examples == 0 {
		opts.Examples = 4
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Metrics == nil {
		opts.Metrics = metrics.NewRegistry()
	}
	if opts.Client == nil {
		opts.Client = &http.Client{Timeout: 15 * time.Second}
	}
	return &Mirror{opts: opts, counts: map[string]int{}, bots: map[string]int{},
		sent: opts.Metrics.Counter("faketg_mirror_messages_total", "Digest messages sent to the shadow Telegram group", "result")}
}

func (m *Mirror) Add(ev Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.total++
	m.counts[ev.Method]++
	label := ev.Bot
	if ev.BotName != "" {
		label = "@" + ev.BotName
	}
	m.bots[label]++
	if ev.Text != "" && len(m.sample) < m.opts.Examples {
		m.sample = append(m.sample, ev)
	}
}

func (m *Mirror) Run(ctx context.Context) {
	t := time.NewTicker(m.opts.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			m.flush(context.Background())
			return
		case <-t.C:
			m.flush(ctx)
		}
	}
}

func (m *Mirror) flush(ctx context.Context) {
	text := m.digest()
	if text == "" {
		return
	}
	form := url.Values{"chat_id": {m.opts.ChatID}, "text": {text}, "disable_web_page_preview": {"true"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.opts.RealAPI+"/bot"+m.opts.Token+"/sendMessage", strings.NewReader(form.Encode()))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := m.opts.Client.Do(req)
	if err != nil {
		m.sent.Inc("error")
		m.opts.Logger.Warn("mirror send failed", "error", err.Error())
		return
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		m.sent.Inc("error")
		m.opts.Logger.Warn("mirror send failed", "status", resp.StatusCode)
		return
	}
	m.sent.Inc("ok")
}

func (m *Mirror) digest() string {
	m.mu.Lock()
	total, counts, bots, sample := m.total, m.counts, m.bots, m.sample
	m.total, m.counts, m.bots, m.sample = 0, map[string]int{}, map[string]int{}, nil
	m.mu.Unlock()
	if total == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "🟦 shadow — last %s: %d calls (none reached the real Telegram)\n\n", human(m.opts.Interval), total)
	b.WriteString("Methods: " + top(counts, 8) + "\n")
	b.WriteString("Bots: " + top(bots, 8) + "\n")
	for i, ev := range sample {
		where := ev.ChatID
		if ev.ThreadID != "" {
			where += " / topic " + ev.ThreadID
		}
		who := ev.Bot
		if ev.BotName != "" {
			who = "@" + ev.BotName
		}
		fmt.Fprintf(&b, "\n— %d) %s → %s · %s\n%s\n", i+1, who, where, ev.Method, clip(ev.Text, 600))
	}
	return clip(b.String(), 4000)
}

func top(m map[string]int, n int) string {
	type kv struct {
		k string
		v int
	}
	list := make([]kv, 0, len(m))
	for k, v := range m {
		list = append(list, kv{k, v})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].v != list[j].v {
			return list[i].v > list[j].v
		}
		return list[i].k < list[j].k
	})
	parts := []string{}
	for i, e := range list {
		if i == n {
			parts = append(parts, fmt.Sprintf("+%d more", len(list)-n))
			break
		}
		parts = append(parts, fmt.Sprintf("%s ×%d", e.k, e.v))
	}
	return strings.Join(parts, ", ")
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func human(d time.Duration) string {
	if d%time.Minute == 0 {
		return fmt.Sprintf("%d min", int(d/time.Minute))
	}
	return d.String()
}
