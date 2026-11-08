package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds runtime configuration. Only environment variables are used so a
// single binary can be dropped on a host and driven by a systemd unit.
type Config struct {
	Addr         string
	DataDir      string
	MasterKey    string
	LogLevel     string
	SessionTTL   time.Duration
	SessionIdle  time.Duration
	PollInterval time.Duration
	Workers      int
	SecureCookie bool
	TrustedProxy bool

	// Integration test hooks. Empty disables the real-host test suites.
	TestSSHHost    string
	TestSSHUser    string
	TestSSHKeyPath string
	TestSSHPort    int
}

func Load() (Config, error) {
	c := Config{
		Addr:         envOr("ONEMATE_ADDR", "127.0.0.1:8080"),
		DataDir:      envOr("ONEMATE_DATA_DIR", "./data"),
		MasterKey:    strings.TrimSpace(os.Getenv("ONEMATE_MASTER_KEY")),
		LogLevel:     envOr("ONEMATE_LOG_LEVEL", "info"),
		SessionTTL:   7 * 24 * time.Hour,
		SessionIdle:  12 * time.Hour,
		PollInterval: 30 * time.Second,
		Workers:      4,
		SecureCookie: boolEnv("ONEMATE_SECURE_COOKIE", false),
		TrustedProxy: boolEnv("ONEMATE_TRUSTED_PROXY", false),

		TestSSHHost:    os.Getenv("ONEMATE_TEST_SSH_HOST"),
		TestSSHUser:    os.Getenv("ONEMATE_TEST_SSH_USER"),
		TestSSHKeyPath: os.Getenv("ONEMATE_TEST_SSH_KEY"),
	}

	var err error
	if c.SessionTTL, err = durEnv("ONEMATE_SESSION_TTL", c.SessionTTL); err != nil {
		return c, err
	}
	if c.SessionIdle, err = durEnv("ONEMATE_SESSION_IDLE", c.SessionIdle); err != nil {
		return c, err
	}
	if c.PollInterval, err = durEnv("ONEMATE_POLL_INTERVAL", c.PollInterval); err != nil {
		return c, err
	}
	if v := os.Getenv("ONEMATE_WORKERS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 64 {
			return c, fmt.Errorf("ONEMATE_WORKERS must be between 1 and 64")
		}
		c.Workers = n
	}
	if v := os.Getenv("ONEMATE_TEST_SSH_PORT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return c, fmt.Errorf("ONEMATE_TEST_SSH_PORT must be a number")
		}
		c.TestSSHPort = n
	}
	if c.SessionIdle > c.SessionTTL {
		return c, fmt.Errorf("ONEMATE_SESSION_IDLE must not exceed ONEMATE_SESSION_TTL")
	}
	return c, nil
}

// InsecureBind reports whether the listen address can be reached from other
// hosts, which requires a TLS terminating proxy in front of it.
func (c Config) InsecureBind() bool {
	host := c.Addr
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	host = strings.Trim(host, "[]")
	switch host {
	case "127.0.0.1", "::1", "localhost":
		return false
	}
	return true
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func boolEnv(key string, def bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func durEnv(key string, def time.Duration) (time.Duration, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def, fmt.Errorf("%s: %w", key, err)
	}
	if d <= 0 {
		return def, fmt.Errorf("%s must be positive", key)
	}
	return d, nil
}
