package config

import (
	"strings"
	"testing"
	"time"

	"github.com/vance1852/manjuflow-studio/internal/apperr"
)

func lookupFrom(values map[string]string) Lookup {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func TestDefaultsProduceAUsableConfiguration(t *testing.T) {
	cfg, err := Load(lookupFrom(map[string]string{}))
	if err != nil {
		t.Fatalf("defaults were rejected: %v", err)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Fatalf("default address is %q", cfg.HTTPAddr)
	}
	if cfg.SessionTTL != 12*time.Hour {
		t.Fatalf("default session ttl is %v", cfg.SessionTTL)
	}
	if cfg.RenderMaxAttempts != 3 || cfg.DailyRenderCapacity != 24 {
		t.Fatalf("default render settings are %d attempts and %d slots",
			cfg.RenderMaxAttempts, cfg.DailyRenderCapacity)
	}
	if cfg.DirectorPassword != "" || cfg.ApprenticePassword != "" {
		t.Fatal("a default credential is baked into the configuration")
	}
}

func TestEnvironmentOverridesAreParsed(t *testing.T) {
	cfg, err := Load(lookupFrom(map[string]string{
		"MANJU_HTTP_ADDR":             "127.0.0.1:9100",
		"MANJU_DB_PATH":               "/tmp/manju.sqlite",
		"MANJU_LOG_LEVEL":             "debug",
		"MANJU_SESSION_TTL":           "45m",
		"MANJU_RENDER_MAX_ATTEMPTS":   "5",
		"MANJU_DAILY_RENDER_CAPACITY": "48",
		"MANJU_RENDER_BACKOFF_BASE":   "750ms",
		"MANJU_STUDIO_SLUG":           "night-market",
	}))
	if err != nil {
		t.Fatalf("valid overrides were rejected: %v", err)
	}
	if cfg.HTTPAddr != "127.0.0.1:9100" || cfg.DatabasePath != "/tmp/manju.sqlite" {
		t.Fatalf("addresses were not applied: %#v", cfg)
	}
	if cfg.SessionTTL != 45*time.Minute || cfg.RenderBackoffBase != 750*time.Millisecond {
		t.Fatalf("durations were not applied: %#v", cfg)
	}
	if cfg.RenderMaxAttempts != 5 || cfg.DailyRenderCapacity != 48 {
		t.Fatalf("integers were not applied: %#v", cfg)
	}
	if cfg.StudioSlug != "night-market" {
		t.Fatalf("studio slug is %q", cfg.StudioSlug)
	}
}

func TestBlankValuesFallBackToDefaults(t *testing.T) {
	cfg, err := Load(lookupFrom(map[string]string{
		"MANJU_HTTP_ADDR":   "   ",
		"MANJU_SESSION_TTL": "",
	}))
	if err != nil {
		t.Fatalf("blank values were rejected: %v", err)
	}
	if cfg.HTTPAddr != ":8080" || cfg.SessionTTL != 12*time.Hour {
		t.Fatalf("blank values did not fall back: %#v", cfg)
	}
}

func TestInvalidValuesAreRejectedWithAField(t *testing.T) {
	cases := map[string]map[string]string{
		"duration":     {"MANJU_SESSION_TTL": "forever"},
		"negative":     {"MANJU_REQUEST_TIMEOUT": "-5s"},
		"integer":      {"MANJU_RENDER_MAX_ATTEMPTS": "many"},
		"attempts":     {"MANJU_RENDER_MAX_ATTEMPTS": "99"},
		"capacity":     {"MANJU_DAILY_RENDER_CAPACITY": "0"},
		"log level":    {"MANJU_LOG_LEVEL": "trace"},
		"short ttl":    {"MANJU_SESSION_TTL": "10s"},
		"short lease":  {"MANJU_RENDER_LEASE_TTL": "10ms"},
		"fast polling": {"MANJU_RENDER_POLL_INTERVAL": "1ms"},
	}
	for name, values := range cases {
		if _, err := Load(lookupFrom(values)); !apperr.IsCode(err, apperr.CodeInvalidArgument) {
			t.Fatalf("%s configuration reported %v", name, apperr.CodeOf(err))
		}
	}
}

func TestValidateCatchesEmptyRequiredValues(t *testing.T) {
	cfg, err := Load(lookupFrom(map[string]string{}))
	if err != nil {
		t.Fatalf("defaults were rejected: %v", err)
	}
	blankAddress := cfg
	blankAddress.HTTPAddr = "  "
	if err := blankAddress.Validate(); err == nil {
		t.Fatal("a blank address was accepted")
	}
	blankPath := cfg
	blankPath.DatabasePath = ""
	if err := blankPath.Validate(); err == nil {
		t.Fatal("a blank database path was accepted")
	}
	blankStudio := cfg
	blankStudio.StudioSlug = ""
	if err := blankStudio.Validate(); err == nil {
		t.Fatal("a blank studio slug was accepted")
	}
}

func TestRedactedOutputKeepsSecretsOut(t *testing.T) {
	cfg, err := Load(lookupFrom(map[string]string{
		"MANJU_DIRECTOR_PASSWORD":   "director-pass-2026",
		"MANJU_APPRENTICE_PASSWORD": "apprentice-pass-2026",
	}))
	if err != nil {
		t.Fatalf("configuration was rejected: %v", err)
	}
	redacted := cfg.Redacted()
	if strings.Contains(redacted, "director-pass-2026") || strings.Contains(redacted, "apprentice-pass-2026") {
		t.Fatalf("the redacted output leaked a credential: %s", redacted)
	}
	if !strings.Contains(redacted, "daily_capacity=") {
		t.Fatalf("the redacted output lost operational fields: %s", redacted)
	}
}
