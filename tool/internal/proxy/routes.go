package proxy

import (
	"regexp"
	"strings"
	"sync"
)

var (
	reNumber = regexp.MustCompile(`^-?\d+$`)
	reUUID   = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	reKey    = regexp.MustCompile(`^[A-Za-z0-9_]{24,}$`)
	reMixed  = regexp.MustCompile(`\d`)
)

type Routes struct {
	mu    sync.Mutex
	seen  map[string]struct{}
	limit int
}

func NewRoutes(limit int) *Routes {
	if limit <= 0 {
		limit = 500
	}
	return &Routes{seen: map[string]struct{}{}, limit: limit}
}

func (r *Routes) Normalize(path string) string {
	route := normalize(path)
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.seen[route]; ok {
		return route
	}
	if len(r.seen) >= r.limit {
		return "other"
	}
	r.seen[route] = struct{}{}
	return route
}

func normalize(path string) string {
	if path == "" {
		return "/"
	}
	parts := strings.Split(path, "/")
	for i, p := range parts {
		switch {
		case p == "":
		case reNumber.MatchString(p):
			parts[i] = "{id}"
		case reUUID.MatchString(p):
			parts[i] = "{uuid}"
		case reKey.MatchString(p) && reMixed.MatchString(p):
			parts[i] = "{key}"
		}
	}
	return strings.Join(parts, "/")
}
