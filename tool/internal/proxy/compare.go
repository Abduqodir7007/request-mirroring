package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
)

const (
	ResultMatch      = "match"
	ResultBodyDiff   = "body_diff"
	ResultStatusDiff = "status_diff"
	ResultGoError    = "shadow_error"
	ResultGoTimeout  = "shadow_timeout"
	ResultSkipped    = "skipped"
)

type Comparer struct {
	Ignore   map[string]bool
	MaxPaths int
	Replace  [][2][]byte
}

func (c *Comparer) WithReplace(spec string) *Comparer {
	for _, pair := range strings.Split(spec, ",") {
		from, to, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if ok && from != "" {
			c.Replace = append(c.Replace, [2][]byte{[]byte(from), []byte(to)})
		}
	}
	return c
}

func NewComparer(ignore []string) *Comparer {
	c := &Comparer{Ignore: map[string]bool{}, MaxPaths: 20}
	for _, k := range ignore {
		if k = strings.TrimSpace(k); k != "" {
			c.Ignore[strings.ToLower(k)] = true
		}
	}
	return c
}

func (c *Comparer) Compare(pyStatus int, pyBody []byte, goStatus int, goBody []byte) (string, []string) {
	if pyStatus != goStatus {
		return ResultStatusDiff, []string{fmt.Sprintf("status: %d != %d", pyStatus, goStatus)}
	}
	for _, r := range c.Replace {
		goBody = bytes.ReplaceAll(goBody, r[0], r[1])
	}
	a, aok := decode(pyBody)
	b, bok := decode(goBody)
	if aok && bok {
		a, b = c.strip(a), c.strip(b)
		if reflect.DeepEqual(a, b) {
			return ResultMatch, nil
		}
		var paths []string
		c.diff("$", a, b, &paths)
		if len(paths) == 0 {
			paths = []string{"$"}
		}
		return ResultBodyDiff, paths
	}
	if bytes.Equal(bytes.TrimSpace(pyBody), bytes.TrimSpace(goBody)) {
		return ResultMatch, nil
	}
	return ResultBodyDiff, []string{"$ (not JSON)"}
}

func decode(b []byte) (any, bool) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || (b[0] != '{' && b[0] != '[') {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return nil, false
	}
	return v, true
}

func (c *Comparer) strip(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if c.Ignore[strings.ToLower(k)] {
				continue
			}
			out[k] = c.strip(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = c.strip(val)
		}
		return out
	case json.Number:
		return normNumber(t)
	}
	return v
}

func normNumber(n json.Number) string {
	if f, err := n.Float64(); err == nil {
		return fmt.Sprintf("%g", f)
	}
	return n.String()
}

func (c *Comparer) diff(path string, a, b any, out *[]string) {
	if len(*out) >= c.MaxPaths {
		return
	}
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			*out = append(*out, path+": type")
			return
		}
		keys := map[string]bool{}
		for k := range av {
			keys[k] = true
		}
		for k := range bv {
			keys[k] = true
		}
		sorted := make([]string, 0, len(keys))
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		for _, k := range sorted {
			x, xok := av[k]
			y, yok := bv[k]
			p := path + "." + k
			switch {
			case !xok:
				*out = append(*out, p+": only in shadow")
			case !yok:
				*out = append(*out, p+": only in primary")
			default:
				c.diff(p, x, y, out)
			}
			if len(*out) >= c.MaxPaths {
				return
			}
		}
	case []any:
		bv, ok := b.([]any)
		if !ok {
			*out = append(*out, path+": type")
			return
		}
		if len(av) != len(bv) {
			*out = append(*out, fmt.Sprintf("%s: length %d != %d", path, len(av), len(bv)))
		}
		n := min(len(av), len(bv))
		for i := 0; i < n; i++ {
			c.diff(fmt.Sprintf("%s[%d]", path, i), av[i], bv[i], out)
			if len(*out) >= c.MaxPaths {
				return
			}
		}
	default:
		if !reflect.DeepEqual(a, b) {
			*out = append(*out, path)
		}
	}
}

type Masker struct {
	re *regexp.Regexp
}

func NewMasker(pattern string) *Masker {
	if pattern == "" {
		return &Masker{}
	}
	return &Masker{re: regexp.MustCompile(pattern)}
}

func (m *Masker) Body(b []byte, limit int) string {
	if v, ok := decode(b); ok && m.re != nil {
		v = m.mask(v)
		if out, err := json.Marshal(v); err == nil {
			b = out
		}
	}
	s := string(b)
	if limit > 0 && len(s) > limit {
		s = s[:limit] + "…"
	}
	return s
}

func (m *Masker) Key(k string) bool { return m.re != nil && m.re.MatchString(k) }

func (m *Masker) mask(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if m.re.MatchString(k) {
				t[k] = "***"
				continue
			}
			t[k] = m.mask(val)
		}
	case []any:
		for i, val := range t {
			t[i] = m.mask(val)
		}
	}
	return v
}
