// Package config loads Airbrew runtime configuration from a .env file (if
// present) and then from AIRBREW_* environment variables.
package config

import (
	"fmt"
	"os"
	"time"

	"github.com/joho/godotenv"
)

// Config holds all runtime knobs for the server.
type Config struct {
	HTTPAddr       string
	DatabasePath   string
	DataDir        string
	PublicURL      string
	WebProxyTarget string
	SessionSecret  []byte
	SessionMaxAge  time.Duration
	LogLevel       string

	BootstrapAdminEmail    string
	BootstrapAdminPassword string
}

// LoadFile loads variables from .env / .env.local if they exist. Missing files
// are silently ignored so the server runs fine with only real env vars.
// It is safe to call multiple times; later calls do not overwrite variables
// that are already set in the real environment.
func LoadFile(paths ...string) {
	if len(paths) == 0 {
		paths = []string{".env.local", ".env"}
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			_ = godotenv.Load(p)
		}
	}
}

// Load reads configuration from AIRBREW_* environment variables (after .env
// has been merged in via LoadFile). Defaults point at ./data so the server
// is self-contained in the current working directory.
func Load() (Config, error) {
	dataDir := envStr("AIRBREW_DATA_DIR", "./data")

	cfg := Config{
		HTTPAddr:       envStr("AIRBREW_HTTP_ADDR", ":5050"),
		DatabasePath:   envStr("AIRBREW_DATABASE_PATH", dataDir+"/airbrew.db"),
		DataDir:        dataDir,
		PublicURL:      envStr("AIRBREW_PUBLIC_URL", "http://localhost:5050"),
		WebProxyTarget: envStr("AIRBREW_WEB_PROXY_TARGET", ""),
		LogLevel:       envStr("AIRBREW_LOG_LEVEL", "info"),
		SessionMaxAge:  envDuration("AIRBREW_SESSION_MAX_AGE", 14*24*time.Hour),

		BootstrapAdminEmail:    envStr("AIRBREW_BOOTSTRAP_ADMIN_EMAIL", "admin@airbrew.local"),
		BootstrapAdminPassword: os.Getenv("AIRBREW_BOOTSTRAP_ADMIN_PASSWORD"),
	}

	secret := os.Getenv("AIRBREW_SESSION_SECRET")
	if secret == "" {
		secret = "dev-insecure-session-secret-please-override-in-production"
	}
	if len(secret) < 32 {
		return Config{}, fmt.Errorf("AIRBREW_SESSION_SECRET must be at least 32 bytes (got %d)", len(secret))
	}
	cfg.SessionSecret = []byte(secret)
	return cfg, nil
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
