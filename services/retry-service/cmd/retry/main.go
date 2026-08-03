// Command retry is the asynchronous retry worker.
//
// It owns one decision: given an event whose delivery failed, should we try
// again, and when? The dispatcher does not answer that — it only reports
// whether a delivery worked. Keeping the policy in one service is what stops
// "how many retries do we allow" from being answered differently in three
// places.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aikssen/notifications-platform/services/retry-service/internal/application/usecase"
	"github.com/aikssen/notifications-platform/services/retry-service/internal/domain"
	"github.com/aikssen/notifications-platform/services/retry-service/internal/infrastructure/config"
	"github.com/aikssen/notifications-platform/services/retry-service/internal/infrastructure/messaging"
	"github.com/aikssen/notifications-platform/services/retry-service/internal/infrastructure/observability"
	"github.com/aikssen/notifications-platform/services/retry-service/internal/infrastructure/postgres"
	"github.com/aikssen/notifications-platform/services/retry-service/internal/infrastructure/system"
)

const shutdownGrace = 15 * time.Second

// reclaimEvery bounds how often the stalled-delivery sweep runs. It is far
// cheaper than the retry poll and does not need the same cadence.
const reclaimEvery = time.Minute

func main() {
	if err := run(); err != nil {
		slog.Error("retry service stopped", slog.Any("error", err))
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := observability.NewLogger(cfg.LogLevel, "retry-service")

	policy, err := domain.NewRetryPolicy(cfg.MaxAttempts, cfg.BaseDelay, cfg.MaxDelay)
	if err != nil {
		return err
	}

	log.Info("starting",
		slog.Int("max_attempts", policy.MaxAttempts),
		slog.Duration("base_delay", policy.BaseDelay),
		slog.Duration("max_delay", policy.MaxDelay),
		slog.Duration("poll_interval", cfg.PollInterval),
		slog.Duration("visibility", cfg.Visibility),
		slog.Int("batch_size", cfg.BatchSize),
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	metrics := observability.NewMetrics()
	ops := observability.NewServer(cfg.MetricsPort, metrics, log)
	ops.Start(log)

	// ---------------------------------------------------------------
	// Driven adapters
	// ---------------------------------------------------------------

	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	log.Info("database connected")

	store := postgres.NewRetryStore(pool)

	dispatchTopic := messaging.NewPublisher(cfg.KafkaBrokers, cfg.KafkaTopicDispatch)
	defer func() { _ = dispatchTopic.Close() }()

	dlqTopic := messaging.NewPublisher(cfg.KafkaBrokers, cfg.KafkaTopicDLQ)
	defer func() { _ = dlqTopic.Close() }()

	// ---------------------------------------------------------------
	// Use case
	// ---------------------------------------------------------------

	process := usecase.NewProcessDueRetries(
		store,
		messaging.NewEventPublisher(dispatchTopic),
		messaging.NewDeadLetterPublisher(dlqTopic),
		system.Clock{},
		system.Randomizer{},
		usecase.Options{
			Policy:     policy,
			Visibility: cfg.Visibility,
			BatchSize:  cfg.BatchSize,
		},
		log,
		observability.NewObserver(metrics),
	)

	ops.MarkReady()

	// ---------------------------------------------------------------
	// Poll until told to stop
	// ---------------------------------------------------------------

	runErr := poll(ctx, process, cfg.PollInterval, cfg.StalledAfter, log)

	log.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := ops.Shutdown(shutdownCtx); err != nil {
		log.Warn("operational server did not close cleanly", slog.Any("error", err))
	}

	log.Info("stopped")
	return runErr
}

// poll drives the retry loop.
//
// When a cycle comes back full, the next one starts immediately instead of
// waiting out the interval: a backlog should drain at the speed the database
// allows, not at the speed of a timer meant for the idle case.
func poll(
	ctx context.Context,
	process *usecase.ProcessDueRetries,
	interval, stalledAfter time.Duration,
	log *slog.Logger,
) error {
	lastReclaim := time.Now()

	for {
		claimed, err := process.Execute(ctx)
		if err != nil && ctx.Err() == nil {
			// A failing cycle is not fatal: the database may be briefly
			// unavailable, and the events are still there when it returns.
			log.Error("retry cycle failed", slog.Any("error", err))
		}

		if err := process.ReportBacklog(ctx); err != nil && ctx.Err() == nil {
			log.Warn("could not measure the retry backlog", slog.Any("error", err))
		}

		if time.Since(lastReclaim) >= reclaimEvery {
			lastReclaim = time.Now()
			if err := process.ReclaimStalled(ctx, stalledAfter); err != nil && ctx.Err() == nil {
				log.Error("stalled delivery sweep failed", slog.Any("error", err))
			}
		}

		wait := interval
		if claimed > 0 {
			wait = 0
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
	}
}
