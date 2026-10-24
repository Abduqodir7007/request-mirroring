package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"time"

	"github.com/Abduqodir7007/request-mirroring/tool/internal/cgstats"
	"github.com/Abduqodir7007/request-mirroring/tool/internal/proxy"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	c := cgstats.NewCollector(env("DOCKER_SOCKET", "/var/run/docker.sock"), env("CGROUP_ROOT", "/sys/fs/cgroup"))
	allow, err := proxy.ParseAllow(env("ALLOW", "127.0.0.1/32,::1/128"))
	if err != nil {
		logger.Error("fatal", "error", err.Error())
		os.Exit(1)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		samples, err := c.Collect(ctx)
		if err != nil {
			logger.Warn("collect failed", "error", err.Error())
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		cgstats.Write(w, samples)
	})
	srv := &http.Server{Addr: env("ADDR", ":8080"), Handler: guard(mux, allow), ReadHeaderTimeout: 5 * time.Second}
	logger.Info("listening", "addr", srv.Addr)
	if err := srv.ListenAndServe(); err != nil {
		logger.Error("fatal", "error", err.Error())
		os.Exit(1)
	}
}

func guard(next http.Handler, allow []netip.Prefix) http.Handler {
	return proxy.AllowOnly(next, allow)
}

func env(k, def string) string {
	if v, ok := os.LookupEnv(k); ok {
		return v
	}
	return def
}
