// Package config loads runtime settings from the environment. No credential is
// ever committed: the bootstrap director password is generated at first start
// when the variable is absent.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/vance1852/manjuflow-studio/internal/apperr"
)

// Config is the validated runtime configuration.
type Config struct {
	HTTPAddr            string
	DatabasePath        string
	LogLevel            string
	ShutdownGrace       time.Duration
	RequestTimeout      time.Duration
	SessionTTL          time.Duration
	RenderLeaseTTL      time.Duration
	RenderPollInterval  time.Duration
	RenderMaxAttempts   int
	RenderBackoffBase   time.Duration
	DailyRenderCapacity int
	WorkshopSweepEvery  time.Duration
	StudioSlug          string
	StudioName          string
	DirectorEmail       string
	DirectorPassword    string
	ApprenticeEmail     string
	ApprenticePassword  string
}

// Lookup abstracts os.LookupEnv so tests can supply a fixed environment.
type Lookup func(key string) (string, bool)

// FromEnvironment loads configuration from the process environment.
func FromEnvironment() (Config, error) { return Load(os.LookupEnv) }

// Load builds configuration from an arbitrary lookup function.
func Load(lookup Lookup) (Config, error) {
	cfg := Config{
		HTTPAddr:           text(lookup, "MANJU_HTTP_ADDR", ":8080"),
		DatabasePath:       text(lookup, "MANJU_DB_PATH", "/data/manjuflow.sqlite"),
		LogLevel:           text(lookup, "MANJU_LOG_LEVEL", "info"),
		StudioSlug:         text(lookup, "MANJU_STUDIO_SLUG", "solo-studio"),
		StudioName:         text(lookup, "MANJU_STUDIO_NAME", "一人漫剧工坊"),
		DirectorEmail:      text(lookup, "MANJU_DIRECTOR_EMAIL", "director@manjuflow.local"),
		DirectorPassword:   text(lookup, "MANJU_DIRECTOR_PASSWORD", ""),
		ApprenticeEmail:    text(lookup, "MANJU_APPRENTICE_EMAIL", "apprentice@manjuflow.local"),
		ApprenticePassword: text(lookup, "MANJU_APPRENTICE_PASSWORD", ""),
	}

	var err error
	if cfg.ShutdownGrace, err = duration(lookup, "MANJU_SHUTDOWN_GRACE", 15*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.RequestTimeout, err = duration(lookup, "MANJU_REQUEST_TIMEOUT", 20*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.SessionTTL, err = duration(lookup, "MANJU_SESSION_TTL", 12*time.Hour); err != nil {
		return Config{}, err
	}
	if cfg.RenderLeaseTTL, err = duration(lookup, "MANJU_RENDER_LEASE_TTL", 30*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.RenderPollInterval, err = duration(lookup, "MANJU_RENDER_POLL_INTERVAL", 500*time.Millisecond); err != nil {
		return Config{}, err
	}
	if cfg.RenderBackoffBase, err = duration(lookup, "MANJU_RENDER_BACKOFF_BASE", 2*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.WorkshopSweepEvery, err = duration(lookup, "MANJU_WORKSHOP_SWEEP_EVERY", 5*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.RenderMaxAttempts, err = number(lookup, "MANJU_RENDER_MAX_ATTEMPTS", 3); err != nil {
		return Config{}, err
	}
	if cfg.DailyRenderCapacity, err = number(lookup, "MANJU_DAILY_RENDER_CAPACITY", 24); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate rejects settings that cannot produce a working service.
func (c Config) Validate() error {
	if strings.TrimSpace(c.HTTPAddr) == "" {
		return apperr.New(apperr.CodeInvalidArgument, "MANJU_HTTP_ADDR must not be empty")
	}
	if strings.TrimSpace(c.DatabasePath) == "" {
		return apperr.New(apperr.CodeInvalidArgument, "MANJU_DB_PATH must not be empty")
	}
	if strings.TrimSpace(c.StudioSlug) == "" {
		return apperr.New(apperr.CodeInvalidArgument, "MANJU_STUDIO_SLUG must not be empty")
	}
	if c.SessionTTL < time.Minute {
		return apperr.New(apperr.CodeInvalidArgument, "MANJU_SESSION_TTL must be at least one minute")
	}
	if c.RenderLeaseTTL < time.Second {
		return apperr.New(apperr.CodeInvalidArgument, "MANJU_RENDER_LEASE_TTL must be at least one second")
	}
	if c.RenderMaxAttempts < 1 || c.RenderMaxAttempts > 10 {
		return apperr.New(apperr.CodeInvalidArgument, "MANJU_RENDER_MAX_ATTEMPTS must be between 1 and 10")
	}
	if c.DailyRenderCapacity < 1 {
		return apperr.New(apperr.CodeInvalidArgument, "MANJU_DAILY_RENDER_CAPACITY must be positive")
	}
	if c.RenderPollInterval < 10*time.Millisecond {
		return apperr.New(apperr.CodeInvalidArgument, "MANJU_RENDER_POLL_INTERVAL must be at least 10ms")
	}
	if _, err := parseLogLevel(c.LogLevel); err != nil {
		return err
	}
	return nil
}

func parseLogLevel(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug", "info", "warn", "warning", "error", "":
		return strings.ToLower(strings.TrimSpace(raw)), nil
	default:
		return "", apperr.New(apperr.CodeInvalidArgument, "MANJU_LOG_LEVEL %q is not supported", raw)
	}
}

func text(lookup Lookup, key, fallback string) string {
	if value, ok := lookup(key); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}

func duration(lookup Lookup, key string, fallback time.Duration) (time.Duration, error) {
	raw, ok := lookup(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return 0, apperr.Wrap(err, apperr.CodeInvalidArgument, "%s is not a duration", key)
	}
	if parsed <= 0 {
		return 0, apperr.New(apperr.CodeInvalidArgument, "%s must be positive", key)
	}
	return parsed, nil
}

func number(lookup Lookup, key string, fallback int) (int, error) {
	raw, ok := lookup(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, apperr.Wrap(err, apperr.CodeInvalidArgument, "%s is not an integer", key)
	}
	return parsed, nil
}

// Redacted renders the configuration for logs without secrets.
func (c Config) Redacted() string {
	return fmt.Sprintf(
		"addr=%s db=%s log=%s session_ttl=%s lease_ttl=%s max_attempts=%d daily_capacity=%d studio=%s",
		c.HTTPAddr, c.DatabasePath, c.LogLevel, c.SessionTTL, c.RenderLeaseTTL,
		c.RenderMaxAttempts, c.DailyRenderCapacity, c.StudioSlug,
	)
}
