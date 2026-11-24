package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Cooldowns struct {
	RateLimit    time.Duration
	Upstream     time.Duration
	UpstreamMax  time.Duration
	Quota        time.Duration
	Auth         time.Duration
	MaxAttempts  int
	ImageTimeout time.Duration
}

type Config struct {
	ListenAddr   string
	DBPath       string
	AdminToken   string
	MasterKey    string
	MaxBodyBytes int64
	ImageTimeout time.Duration
	HTTPTimeout  time.Duration
	LogRetention time.Duration
	CORSOrigins  []string
	Cooldowns    Cooldowns
}

func Load() (Config, error) {
	var missing []string
	req := func(key string) string {
		v := os.Getenv(key)
		if v == "" {
			missing = append(missing, key)
		}
		return v
	}
	var bad []string
	str := func(key, def string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}
		return def
	}
	num := func(key string, def int) int {
		if v := os.Getenv(key); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				bad = append(bad, key)
				return def
			}
			return n
		}
		return def
	}
	dur := func(key string, def time.Duration) time.Duration {
		if v := os.Getenv(key); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil {
				bad = append(bad, key)
				return def
			}
			return d
		}
		return def
	}

	cfg := Config{
		ListenAddr:   str("LISTEN_ADDR", ":8080"),
		DBPath:       str("DB_PATH", "data/gateway.db"),
		AdminToken:   req("ADMIN_TOKEN"),
		MasterKey:    req("MASTER_KEY"),
		MaxBodyBytes: int64(num("MAX_BODY_MB", 100)) << 20,
		ImageTimeout: dur("IMAGE_TIMEOUT", 5*time.Minute),
		HTTPTimeout:  dur("HTTP_TIMEOUT", 5*time.Minute),
		LogRetention: dur("LOG_RETENTION", 720*time.Hour),
		CORSOrigins:  list(str("CORS_ORIGINS", "*")),
		Cooldowns: Cooldowns{
			RateLimit:    dur("COOLDOWN_RATELIMIT", time.Minute),
			Upstream:     dur("COOLDOWN_UPSTREAM", 30*time.Second),
			UpstreamMax:  dur("COOLDOWN_UPSTREAM_MAX", 10*time.Minute),
			Quota:        dur("COOLDOWN_QUOTA", time.Hour),
			Auth:         dur("COOLDOWN_AUTH", 24*time.Hour),
			MaxAttempts:  num("MAX_ATTEMPTS", 0),
			ImageTimeout: dur("IMAGE_TIMEOUT", 5*time.Minute),
		},
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required env vars: %s", strings.Join(missing, ", "))
	}
	if len(bad) > 0 {
		return Config{}, fmt.Errorf("invalid env var values: %s", strings.Join(bad, ", "))
	}
	if dir := filepath.Dir(cfg.DBPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Config{}, fmt.Errorf("create DB dir %s: %w", dir, err)
		}
	}
	return cfg, nil
}

func list(v string) []string {
	parts := strings.Split(v, ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
