package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/Abduqodir7007/request-mirroring/tool/internal/fakeapi"
	"github.com/Abduqodir7007/request-mirroring/tool/internal/metrics"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("fatal", "error", err.Error())
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	reg := metrics.NewRegistry()
	realAPI := env("REAL_TELEGRAM_API_URL", "https://api.telegram.org")

	var rec *fakeapi.Recorder
	if path := env("RECORD_PATH", "/data/calls.jsonl"); path != "" {
		r, err := fakeapi.NewRecorder(path, int64(envInt("RECORD_MAX_MB", 100))<<20)
		if err != nil {
			return err
		}
		defer r.Close()
		rec = r
	}

	var mirror *fakeapi.Mirror
	if token, chat := os.Getenv("MIRROR_TG_BOT_TOKEN"), os.Getenv("MIRROR_TG_CHAT_ID"); token != "" && chat != "" {
		mirror = fakeapi.NewMirror(fakeapi.MirrorOptions{RealAPI: realAPI, Token: token, ChatID: chat,
			Interval: envDuration("MIRROR_INTERVAL", time.Minute), Examples: envInt("MIRROR_EXAMPLES", 4),
			Logger: logger, Metrics: reg})
		go mirror.Run(ctx)
	}

	srv := fakeapi.New(fakeapi.Options{RealAPI: realAPI, Passthrough: env("PASSTHROUGH_READS", "true") == "true",
		PassthroughTTL: envDuration("PASSTHROUGH_TTL", 10*time.Minute), Recorder: rec, Mirror: mirror,
		Metrics: reg, Logger: logger})

	httpSrv := &http.Server{Addr: env("ADDR", ":8081"), Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.ListenAndServe() }()
	logger.Info("listening", "addr", httpSrv.Addr, "mirror", mirror != nil, "record", rec != nil)
	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return httpSrv.Shutdown(shutdown)
}

func env(k, def string) string {
	if v, ok := os.LookupEnv(k); ok {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(k)); err == nil {
		return n
	}
	return def
}

func envDuration(k string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(os.Getenv(k)); err == nil {
		return d
	}
	return def
}
