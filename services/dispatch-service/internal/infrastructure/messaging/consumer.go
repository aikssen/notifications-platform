package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/segmentio/kafka-go"

	"github.com/aikssen/notifications-platform/services/dispatch-service/internal/application/port"
	"github.com/aikssen/notifications-platform/services/dispatch-service/internal/domain"
)

// dispatchMessage is the wire contract of the notifications.dispatch topic.
type dispatchMessage struct {
	EventID        string          `json:"event_id"`
	ClientID       string          `json:"client_id"`
	EventType      string          `json:"event_type"`
	EventPayload   json.RawMessage `json:"event_payload"`
	DispatchSource string          `json:"dispatch_source"`
	CorrelationID  string          `json:"correlation_id,omitempty"`
}

// ConsumerObserver reports what the consumer is doing, so the loop itself does
// not have to depend on a metrics library.
type ConsumerObserver interface {
	MessageProcessed(outcome string, d time.Duration)
}

// Consumer reads the dispatch topic and hands each message to the use case.
//
// Offsets are committed manually, only after processing succeeds. The previous
// implementation caught every error inside the message handler and returned
// normally, which let the client library commit the offset for a message that
// was never processed — a silent, permanent loss of a payment notification.
type Consumer struct {
	reader    *kafka.Reader
	processor port.EventProcessor
	dlq       *Publisher
	log       *slog.Logger
	observer  ConsumerObserver

	retryBase time.Duration
	retryMax  time.Duration
}

type ConsumerConfig struct {
	Brokers  []string
	Topic    string
	GroupID  string
	ClientID string
}

func NewConsumer(
	cfg ConsumerConfig,
	processor port.EventProcessor,
	dlq *Publisher,
	log *slog.Logger,
	observer ConsumerObserver,
) *Consumer {
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers: cfg.Brokers,
		Topic:   cfg.Topic,
		GroupID: cfg.GroupID,
		// Explicit: nothing here may auto-commit on our behalf.
		CommitInterval: 0,
		MinBytes:       1,
		MaxBytes:       10e6,
		MaxWait:        500 * time.Millisecond,
		StartOffset:    kafka.FirstOffset,
	})

	return &Consumer{
		reader:    reader,
		processor: processor,
		dlq:       dlq,
		log:       log,
		observer:  observer,
		retryBase: 500 * time.Millisecond,
		retryMax:  30 * time.Second,
	}
}

// Run consumes until the context is cancelled.
func (c *Consumer) Run(ctx context.Context) error {
	c.log.Info("consumer started",
		slog.String("topic", c.reader.Config().Topic),
		slog.String("group_id", c.reader.Config().GroupID),
	)

	for {
		msg, err := c.reader.FetchMessage(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				c.log.Info("consumer stopping")
				return nil
			}
			return fmt.Errorf("fetch message: %w", err)
		}

		if err := c.handle(ctx, msg); err != nil {
			// The context was cancelled mid-flight during shutdown. Leaving the
			// offset uncommitted is correct: the message will be redelivered.
			return err
		}
	}
}

func (c *Consumer) handle(ctx context.Context, msg kafka.Message) error {
	started := time.Now()

	in, err := decode(msg.Value)
	if err != nil {
		// A message we cannot even parse will never succeed, no matter how
		// often it is retried. Blocking the partition on it would stall every
		// other client's notifications, so it goes to the dead letter queue and
		// the offset moves on.
		c.log.Error("undeliverable message sent to the dead letter queue",
			slog.Any("error", err),
			slog.String("raw", truncate(string(msg.Value), 512)),
			slog.Int("partition", msg.Partition),
			slog.Int64("offset", msg.Offset),
		)
		c.toDLQ(ctx, msg, err)
		c.observe("poison", started)
		return c.commit(ctx, msg)
	}

	log := c.log.With(
		slog.String("event_id", in.EventID),
		slog.String("client_id", in.ClientID),
		slog.String("event_type", in.EventType),
		slog.String("correlation_id", in.CorrelationID),
		slog.Int("partition", msg.Partition),
		slog.Int64("offset", msg.Offset),
	)

	// Processing failures are infrastructure failures: the database is down,
	// the subscription service is unreachable. Retrying in place applies
	// backpressure and lets consumer lag show the backlog, which is far better
	// than skipping the message and losing the notification.
	backoff := c.retryBase
	for attempt := 1; ; attempt++ {
		err := c.processor.Process(ctx, in)
		if err == nil {
			c.observe("processed", started)
			return c.commit(ctx, msg)
		}

		if ctx.Err() != nil {
			log.Warn("shutting down with the message uncommitted, it will be redelivered")
			return nil
		}

		log.Error("processing failed, retrying without committing the offset",
			slog.Any("error", err),
			slog.Int("attempt", attempt),
			slog.Duration("retry_in", backoff),
		)
		c.observe("retry", started)

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}

		if backoff *= 2; backoff > c.retryMax {
			backoff = c.retryMax
		}
	}
}

func (c *Consumer) commit(ctx context.Context, msg kafka.Message) error {
	if err := c.reader.CommitMessages(ctx, msg); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		// Not fatal: the message was processed, and processing is idempotent,
		// so a redelivery is harmless.
		c.log.Warn("could not commit offset, the message may be redelivered",
			slog.Any("error", err),
			slog.Int64("offset", msg.Offset),
		)
	}
	return nil
}

func (c *Consumer) toDLQ(ctx context.Context, msg kafka.Message, cause error) {
	if c.dlq == nil {
		return
	}
	record := map[string]any{
		"reason":       cause.Error(),
		"topic":        msg.Topic,
		"partition":    msg.Partition,
		"offset":       msg.Offset,
		"raw":          string(msg.Value),
		"dead_at":      time.Now().UTC(),
		"failed_stage": "decode",
	}
	if err := c.dlq.PublishJSON(ctx, string(msg.Key), record); err != nil {
		c.log.Error("could not write to the dead letter queue", slog.Any("error", err))
	}
}

func (c *Consumer) observe(outcome string, started time.Time) {
	if c.observer != nil {
		c.observer.MessageProcessed(outcome, time.Since(started))
	}
}

func (c *Consumer) Close() error { return c.reader.Close() }

func decode(raw []byte) (port.IncomingEvent, error) {
	var msg dispatchMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return port.IncomingEvent{}, fmt.Errorf("malformed dispatch message: %w", err)
	}

	switch {
	case msg.EventID == "":
		return port.IncomingEvent{}, errors.New("dispatch message is missing event_id")
	case msg.ClientID == "":
		return port.IncomingEvent{}, errors.New("dispatch message is missing client_id")
	case msg.EventType == "":
		return port.IncomingEvent{}, errors.New("dispatch message is missing event_type")
	case len(msg.EventPayload) == 0:
		return port.IncomingEvent{}, errors.New("dispatch message is missing event_payload")
	}

	// An unrecognised source defaults to SYSTEM rather than rejecting the
	// message: the origin label is for traceability, and losing a real payment
	// notification over it would be the wrong trade.
	source := domain.SourceSystem
	if msg.DispatchSource != "" {
		parsed, err := domain.ParseDispatchSource(msg.DispatchSource)
		if err == nil {
			source = parsed
		}
	}

	return port.IncomingEvent{
		EventID:        msg.EventID,
		ClientID:       msg.ClientID,
		EventType:      msg.EventType,
		Payload:        msg.EventPayload,
		DispatchSource: source,
		CorrelationID:  msg.CorrelationID,
	}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
