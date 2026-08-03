// Command dispatcher is the notifications delivery worker.
//
// This file is the composition root, and it is the only place in the service
// where a concrete adapter is named. Everything above it depends on interfaces.
// There is no DI container and no reflection: the wiring is a file you read
// from top to bottom.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aikssen/notifications-platform/services/dispatch-service/internal/application/usecase"
	"github.com/aikssen/notifications-platform/services/dispatch-service/internal/infrastructure/config"
	"github.com/aikssen/notifications-platform/services/dispatch-service/internal/infrastructure/messaging"
	"github.com/aikssen/notifications-platform/services/dispatch-service/internal/infrastructure/observability"
	"github.com/aikssen/notifications-platform/services/dispatch-service/internal/infrastructure/postgres"
	"github.com/aikssen/notifications-platform/services/dispatch-service/internal/infrastructure/subscriptions"
	"github.com/aikssen/notifications-platform/services/dispatch-service/internal/infrastructure/system"
	"github.com/aikssen/notifications-platform/services/dispatch-service/internal/infrastructure/webhook"
)

// shutdownGrace bounds how long an in-flight delivery may take to finish once
// a termination signal arrives.
const shutdownGrace = 20 * time.Second

func main() {
	if err := run(); err != nil {
		slog.Error("dispatch service stopped", slog.Any("error", err))
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := observability.NewLogger(cfg.LogLevel, "dispatch-service")

	// Note what is logged, and what is not: the topic and the group, never the
	// database URL. A connection string in a log line is a leaked password.
	log.Info("starting",
		slog.String("topic", cfg.KafkaTopicDispatch),
		slog.String("consumer_group", cfg.KafkaConsumerGroup),
		slog.String("subscriptions_url", cfg.SubscriptionsBaseURL),
		slog.Bool("webhook_require_https", cfg.WebhookRequireHTTPS),
		slog.Bool("webhook_allow_private_networks", cfg.WebhookAllowPrivate),
	)

	if cfg.WebhookAllowPrivate || !cfg.WebhookRequireHTTPS {
		log.Warn("SSRF protections are relaxed — acceptable for a local demo, never for production")
	}

	// Signal handling first, so every long-lived component below shares one
	// cancellation.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	metrics := observability.NewMetrics()
	opsServer := observability.NewServer(cfg.MetricsPort, metrics, log)
	opsServer.Start(log)

	// ---------------------------------------------------------------
	// Driven adapters
	// ---------------------------------------------------------------

	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	log.Info("database connected")

	eventStore := postgres.NewEventStore(pool)

	subscriptionResolver := subscriptions.NewResolver(
		cfg.SubscriptionsBaseURL,
		cfg.SubscriptionsTimeout,
	)

	webhookSender := webhook.NewSender(
		webhook.NewGuard(cfg.WebhookRequireHTTPS, cfg.WebhookAllowPrivate),
		webhook.SenderOptions{
			Timeout:     cfg.WebhookTimeout,
			MaxAttempts: cfg.WebhookMaxAttempts,
			BaseDelay:   cfg.WebhookBaseDelay,
			MaxDelay:    cfg.WebhookMaxDelay,
		},
		log,
	)

	resultTopic := messaging.NewPublisher(cfg.KafkaBrokers, cfg.KafkaTopicResult)
	defer func() { _ = resultTopic.Close() }()

	dlqTopic := messaging.NewPublisher(cfg.KafkaBrokers, cfg.KafkaTopicDLQ)
	defer func() { _ = dlqTopic.Close() }()

	// Metrics are derived from the delivery-result stream, so the dashboard and
	// Grafana can never tell different stories.
	resultPublisher := observability.NewMeteredResultPublisher(
		messaging.NewResultPublisher(resultTopic),
		metrics,
	)

	clock := system.Clock{}
	ids := system.UUIDGenerator{}

	// ---------------------------------------------------------------
	// Use cases
	// ---------------------------------------------------------------

	ingest := usecase.NewIngestNotificationEvent(eventStore, clock, ids)

	dispatch := usecase.NewDispatchNotification(
		eventStore,
		subscriptionResolver,
		webhookSender,
		resultPublisher,
		clock,
		ids,
		log,
	)

	processor := usecase.NewProcessIncomingEvent(ingest, dispatch, ids, log)

	// ---------------------------------------------------------------
	// Driving adapter
	// ---------------------------------------------------------------

	consumer := messaging.NewConsumer(
		messaging.ConsumerConfig{
			Brokers:  cfg.KafkaBrokers,
			Topic:    cfg.KafkaTopicDispatch,
			GroupID:  cfg.KafkaConsumerGroup,
			ClientID: cfg.KafkaClientID,
		},
		processor,
		dlqTopic,
		log,
		metrics,
	)

	opsServer.MarkReady()

	// ---------------------------------------------------------------
	// Run until told to stop, then drain
	// ---------------------------------------------------------------

	runErr := consumer.Run(ctx)

	log.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()

	// Closing the consumer first stops new work arriving; the publishers are
	// closed after, so results from the last in-flight delivery still get out.
	if err := consumer.Close(); err != nil {
		log.Warn("consumer did not close cleanly", slog.Any("error", err))
	}
	if err := opsServer.Shutdown(shutdownCtx); err != nil {
		log.Warn("operational server did not close cleanly", slog.Any("error", err))
	}

	log.Info("stopped")
	return runErr
}
