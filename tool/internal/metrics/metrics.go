package metrics

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type Registry struct {
	mu     sync.Mutex
	order  []string
	help   map[string]string
	kind   map[string]string
	labels map[string][]string
	values map[string]map[string]*series
	bounds map[string][]float64
}

type series struct {
	labelValues []string
	value       float64
	buckets     []uint64
	sum         float64
	count       uint64
}

func NewRegistry() *Registry {
	return &Registry{
		help: map[string]string{}, kind: map[string]string{}, labels: map[string][]string{},
		values: map[string]map[string]*series{}, bounds: map[string][]float64{},
	}
}

type Counter struct {
	r    *Registry
	name string
}

type Histogram struct {
	r    *Registry
	name string
}

var DefaultBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

func (r *Registry) register(name, help, kind string, labels []string, bounds []float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.kind[name]; ok {
		return
	}
	r.order = append(r.order, name)
	r.help[name], r.kind[name], r.labels[name] = help, kind, labels
	r.values[name] = map[string]*series{}
	r.bounds[name] = bounds
}

func (r *Registry) Counter(name, help string, labels ...string) *Counter {
	r.register(name, help, "counter", labels, nil)
	return &Counter{r: r, name: name}
}

func (r *Registry) Histogram(name, help string, bounds []float64, labels ...string) *Histogram {
	if bounds == nil {
		bounds = DefaultBuckets
	}
	r.register(name, help, "histogram", labels, bounds)
	return &Histogram{r: r, name: name}
}

func (r *Registry) get(name string, values []string) *series {
	key := strings.Join(values, "\xff")
	s, ok := r.values[name][key]
	if !ok {
		s = &series{labelValues: append([]string(nil), values...)}
		if b := r.bounds[name]; b != nil {
			s.buckets = make([]uint64, len(b))
		}
		r.values[name][key] = s
	}
	return s
}

func (c *Counter) Add(delta float64, labelValues ...string) {
	c.r.mu.Lock()
	c.r.get(c.name, labelValues).value += delta
	c.r.mu.Unlock()
}

func (c *Counter) Inc(labelValues ...string) { c.Add(1, labelValues...) }

func (h *Histogram) Observe(v float64, labelValues ...string) {
	h.r.mu.Lock()
	s := h.r.get(h.name, labelValues)
	for i, b := range h.r.bounds[h.name] {
		if v <= b {
			s.buckets[i]++
		}
	}
	s.sum += v
	s.count++
	h.r.mu.Unlock()
}

func (r *Registry) Value(name string, labelValues ...string) float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.values[name][strings.Join(labelValues, "\xff")]; ok {
		if s.buckets != nil {
			return float64(s.count)
		}
		return s.value
	}
	return 0
}

func (r *Registry) Write(w io.Writer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, name := range r.order {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", name, r.help[name], name, r.kind[name])
		keys := make([]string, 0, len(r.values[name]))
		for k := range r.values[name] {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			s := r.values[name][k]
			base := labelPairs(r.labels[name], s.labelValues)
			if r.kind[name] != "histogram" {
				fmt.Fprintf(w, "%s%s %s\n", name, wrap(base), num(s.value))
				continue
			}
			for i, b := range r.bounds[name] {
				fmt.Fprintf(w, "%s_bucket%s %d\n", name, wrap(join(base, `le="`+num(b)+`"`)), s.buckets[i])
			}
			fmt.Fprintf(w, "%s_bucket%s %d\n", name, wrap(join(base, `le="+Inf"`)), s.count)
			fmt.Fprintf(w, "%s_sum%s %s\n", name, wrap(base), num(s.sum))
			fmt.Fprintf(w, "%s_count%s %d\n", name, wrap(base), s.count)
		}
	}
}

func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		r.Write(w)
	})
}

func labelPairs(names, values []string) string {
	parts := make([]string, 0, len(names))
	for i, n := range names {
		v := ""
		if i < len(values) {
			v = values[i]
		}
		parts = append(parts, n+`="`+escape(v)+`"`)
	}
	return strings.Join(parts, ",")
}

func join(a, b string) string {
	if a == "" {
		return b
	}
	return a + "," + b
}

func wrap(s string) string {
	if s == "" {
		return ""
	}
	return "{" + s + "}"
}

func escape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

func num(v float64) string {
	if math.IsInf(v, 1) {
		return "+Inf"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}
