package proxy

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

func AdminHandler(p *Proxy, allow []netip.Prefix) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.Handle("/metrics", p.opts.Metrics.Handler())
	mux.HandleFunc("/diffs", p.opts.Diffs.HTML)
	mux.HandleFunc("/diffs.json", p.opts.Diffs.JSON)
	return AllowOnly(mux, allow)
}

func AllowOnly(next http.Handler, allow []netip.Prefix) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowed(r.RemoteAddr, allow) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func ParseAllow(list string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, s := range strings.Split(list, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if !strings.Contains(s, "/") {
			if a, err := netip.ParseAddr(s); err == nil {
				out = append(out, netip.PrefixFrom(a, a.BitLen()))
				continue
			}
		}
		pfx, err := netip.ParsePrefix(s)
		if err != nil {
			return nil, err
		}
		out = append(out, pfx)
	}
	return out, nil
}

func allowed(remote string, allow []netip.Prefix) bool {
	if len(allow) == 0 {
		return true
	}
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	a = a.Unmap()
	for _, p := range allow {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
