package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DatabaseURL string

	KafkaBrokers       []string
	KafkaTopicDispatch string
	KafkaTopicDLQ      string

	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration

	PollInterval time.Duration
	BatchSize    int

	// Visibility is how long a claimed event stays hidden from other pollers.
	// It must comfortably exceed the time one cycle takes, or an event could
	// be claimed twice while still being processed.
	Visibility time.Duration

	// StalledAfter is how long an event may sit in DELIVERING before it is
	// treated as abandoned by a dispatcher that died mid-delivery.
	StalledAfter time.Duration

	MetricsPort int
	LogLevel    string
}

func Load() (Config, error) {
	cfg := Config{
		DatabaseURL:        os.Getenv("DATABASE_URL"),
		KafkaTopicDispatch: envOr("KAFKA_TOPIC_DISPATCH", "notifications.dispatch"),
		KafkaTopicDLQ:      envOr("KAFKA_TOPIC_DLQ", "notifications.dlq"),
		LogLevel:           envOr("LOG_LEVEL", "info"),
	}

	for _, b := range strings.Split(envOr("KAFKA_BROKERS", ""), ",") {
		if b = strings.TrimSpace(b); b != "" {
			cfg.KafkaBrokers = append(cfg.KafkaBrokers, b)
		}
	}

	var err error
	if cfg.MaxAttempts, err = intEnv("RETRY_MAX_ATTEMPTS", 5); err != nil {
		return cfg, err
	}
	if cfg.BaseDelay, err = secondsEnv("RETRY_BASE_DELAY_SECONDS", 10); err != nil {
		return cfg, err
	}
	if cfg.MaxDelay, err = secondsEnv("RETRY_MAX_DELAY_SECONDS", 900); err != nil {
		return cfg, err
	}
	if cfg.PollInterval, err = secondsEnv("RETRY_POLL_INTERVAL_SECONDS", 5); err != nil {
		return cfg, err
	}
	if cfg.Visibility, err = secondsEnv("RETRY_VISIBILITY_SECONDS", 60); err != nil {
		return cfg, err
	}
	if cfg.StalledAfter, err = secondsEnv("RETRY_STALLED_AFTER_SECONDS", 300); err != nil {
		return cfg, err
	}
	if cfg.BatchSize, err = intEnv("RETRY_BATCH_SIZE", 50); err != nil {
		return cfg, err
	}
	if cfg.MetricsPort, err = intEnv("RETRY_METRICS_PORT", 9102); err != nil {
		return cfg, err
	}

	return cfg, cfg.validate()
}

func (c Config) validate() error {
	var missing []string
	if c.DatabaseURL == "" {
		missing = append(missing, "DATABASE_URL")
	}
	if len(c.KafkaBrokers) == 0 {
		missing = append(missing, "KAFKA_BROKERS")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required configuration: %s", strings.Join(missing, ", "))
	}

	if c.BatchSize < 1 {
		return fmt.Errorf("RETRY_BATCH_SIZE must be at least 1")
	}
	if c.Visibility <= c.PollInterval {
		return fmt.Errorf(
			"RETRY_VISIBILITY_SECONDS (%v) must exceed RETRY_POLL_INTERVAL_SECONDS (%v), "+
				"otherwise an event can be claimed again while it is still being processed",
			c.Visibility, c.PollInterval)
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func intEnv(key string, fallback int) (int, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	return v, nil
}

func secondsEnv(key string, fallback int) (time.Duration, error) {
	v, err := intEnv(key, fallback)
	if err != nil {
		return 0, err
	}
	return time.Duration(v) * time.Second, nil
}
