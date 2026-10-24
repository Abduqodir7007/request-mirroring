package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Abduqodir7007/request-mirroring/tool/internal/metrics"
	"github.com/Abduqodir7007/request-mirroring/tool/internal/proxy"
)

const defaultIgnore = "created_at,updated_at,date,timestamp,request_id,access,refresh,access_token,refresh_token,token,expires_at,exp,iat,jti"

const defaultMask = `(?i)(token|password|passwd|secret|access|refresh|otp|sms_code|verify_code|pinfl|passport|authorization|cookie|hash)`

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("fatal", "error", err.Error())
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	primary, err := mustURL("PRIMARY_URL")
	if err != nil {
		return err
	}
	var shadow *url.URL
	if os.Getenv("SHADOW_URL") != "" {
		if shadow, err = mustURL("SHADOW_URL"); err != nil {
			return err
		}
	}
	var skip *regexp.Regexp
	if s := os.Getenv("SKIP_PATHS"); s != "" {
		if skip, err = regexp.Compile(s); err != nil {
			return fmt.Errorf("SKIP_PATHS: %w", err)
		}
	}
	methods := map[string]bool{}
	for _, m := range strings.Split(env("SHADOW_METHODS", ""), ",") {
		if m = strings.ToUpper(strings.TrimSpace(m)); m != "" {
			methods[m] = true
		}
	}
	allow, err := proxy.ParseAllow(env("ADMIN_ALLOW", "127.0.0.1/32,::1/128"))
	if err != nil {
		return fmt.Errorf("ADMIN_ALLOW: %w", err)
	}

	reg := metrics.NewRegistry()
	p := proxy.New(proxy.Options{
		Service:       env("SERVICE", "bot"),
		Primary:       primary,
		Shadow:        shadow,
		ShadowTimeout: envDuration("SHADOW_TIMEOUT", 5*time.Second),
		MaxBody:       int64(envInt("MAX_BODY_MB", 10)) << 20,
		MaxCompare:    envInt("MAX_COMPARE_KB", 2048) << 10,
		MaxInFlight:   envInt("SHADOW_MAX_INFLIGHT", 64),
		SkipPaths:     skip,
		ShadowMethods: methods,
		Comparer:      proxy.NewComparer(strings.Split(env("IGNORE_KEYS", defaultIgnore), ",")).WithReplace(os.Getenv("SHADOW_BODY_REPLACE")),
		Masker:        proxy.NewMasker(env("MASK_KEYS", defaultMask)),
		Diffs:         proxy.NewDiffStore(envInt("DIFFS_KEEP", 1000)),
		Metrics:       reg,
		Logger:        logger,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	front := &http.Server{Addr: env("ADDR", ":8019"), Handler: p, ReadHeaderTimeout: 15 * time.Second}
	admin := &http.Server{Addr: env("ADMIN_ADDR", ":9187"), Handler: proxy.AdminHandler(p, allow), ReadHeaderTimeout: 5 * time.Second}
	errCh := make(chan error, 2)
	go func() { errCh <- front.ListenAndServe() }()
	go func() { errCh <- admin.ListenAndServe() }()
	logger.Info("listening", "addr", front.Addr, "admin", admin.Addr, "primary", primary.String(), "shadow", fmt.Sprint(shadow), "service", env("SERVICE", "bot"))

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	sd, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = admin.Shutdown(sd)
	return front.Shutdown(sd)
}

func mustURL(k string) (*url.URL, error) {
	u, err := url.Parse(os.Getenv(k))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("%s: http(s)://host:port kerak", k)
	}
	return u, nil
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
