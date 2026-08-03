package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is resolved once at startup and never read from the environment
// again, so every component receives explicit values instead of reaching for
// globals.
type Config struct {
	DatabaseURL string

	KafkaBrokers       []string
	KafkaTopicDispatch string
	KafkaTopicResult   string
	KafkaTopicDLQ      string
	KafkaConsumerGroup string
	KafkaClientID      string

	SubscriptionsBaseURL string
	SubscriptionsTimeout time.Duration

	WebhookTimeout      time.Duration
	WebhookMaxAttempts  int
	WebhookBaseDelay    time.Duration
	WebhookMaxDelay     time.Duration
	WebhookRequireHTTPS bool
	WebhookAllowPrivate bool

	MetricsPort int
	LogLevel    string
}

// Load reads configuration and fails fast on anything missing.
//
// Note what is absent from the startup logs elsewhere in this service: the
// database URL. Logging a connection string logs its password.
func Load() (Config, error) {
	cfg := Config{
		DatabaseURL:          os.Getenv("DATABASE_URL"),
		KafkaTopicDispatch:   envOr("KAFKA_TOPIC_DISPATCH", "notifications.dispatch"),
		KafkaTopicResult:     envOr("KAFKA_TOPIC_RESULT", "notifications.delivery-result"),
		KafkaTopicDLQ:        envOr("KAFKA_TOPIC_DLQ", "notifications.dlq"),
		KafkaConsumerGroup:   envOr("KAFKA_CONSUMER_GROUP", "notifications-dispatch"),
		KafkaClientID:        envOr("KAFKA_CLIENT_ID", "notifications-dispatch-service"),
		SubscriptionsBaseURL: os.Getenv("SUBSCRIPTIONS_BASE_URL"),
		LogLevel:             envOr("LOG_LEVEL", "info"),
	}

	brokers := envOr("KAFKA_BROKERS", "")
	for _, b := range strings.Split(brokers, ",") {
		if b = strings.TrimSpace(b); b != "" {
			cfg.KafkaBrokers = append(cfg.KafkaBrokers, b)
		}
	}

	var err error
	if cfg.SubscriptionsTimeout, err = durationMS("SUBSCRIPTIONS_TIMEOUT_MS", 3000); err != nil {
		return cfg, err
	}
	if cfg.WebhookTimeout, err = durationMS("WEBHOOK_TIMEOUT_MS", 5000); err != nil {
		return cfg, err
	}
	if cfg.WebhookBaseDelay, err = durationMS("WEBHOOK_SYNC_BASE_DELAY_MS", 200); err != nil {
		return cfg, err
	}
	if cfg.WebhookMaxDelay, err = durationMS("WEBHOOK_SYNC_MAX_DELAY_MS", 2000); err != nil {
		return cfg, err
	}
	if cfg.WebhookMaxAttempts, err = intEnv("WEBHOOK_SYNC_MAX_ATTEMPTS", 3); err != nil {
		return cfg, err
	}
	if cfg.MetricsPort, err = intEnv("DISPATCH_METRICS_PORT", 9101); err != nil {
		return cfg, err
	}

	cfg.WebhookRequireHTTPS = boolEnv("WEBHOOK_REQUIRE_HTTPS", true)
	cfg.WebhookAllowPrivate = boolEnv("WEBHOOK_ALLOW_PRIVATE_NETWORKS", false)

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
	if c.SubscriptionsBaseURL == "" {
		missing = append(missing, "SUBSCRIPTIONS_BASE_URL")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required configuration: %s", strings.Join(missing, ", "))
	}
	if c.WebhookMaxAttempts < 1 {
		return fmt.Errorf("WEBHOOK_SYNC_MAX_ATTEMPTS must be at least 1")
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

func durationMS(key string, fallbackMS int) (time.Duration, error) {
	v, err := intEnv(key, fallbackMS)
	if err != nil {
		return 0, err
	}
	return time.Duration(v) * time.Millisecond, nil
}

func boolEnv(key string, fallback bool) bool {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch raw {
	case "":
		return fallback
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
