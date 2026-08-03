package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/segmentio/kafka-go"

	"github.com/aikssen/notifications-platform/services/retry-service/internal/application/port"
)

type Publisher struct {
	writer *kafka.Writer
}

func NewPublisher(brokers []string, topic string) *Publisher {
	return &Publisher{
		writer: &kafka.Writer{
			Addr:  kafka.TCP(brokers...),
			Topic: topic,
			// Keyed by client, so every event for one client lands on the same
			// partition and keeps its order relative to the others.
			Balancer:     &kafka.Hash{},
			RequiredAcks: kafka.RequireAll,
			BatchTimeout: 20 * time.Millisecond,
		},
	}
}

func (p *Publisher) publish(ctx context.Context, key string, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode message: %w", err)
	}
	return p.writer.WriteMessages(ctx, kafka.Message{Key: []byte(key), Value: body})
}

func (p *Publisher) Close() error { return p.writer.Close() }

// EventPublisher puts events back on the delivery topic.
type EventPublisher struct{ p *Publisher }

func NewEventPublisher(p *Publisher) *EventPublisher { return &EventPublisher{p: p} }

func (e *EventPublisher) Publish(ctx context.Context, msg port.DispatchMessage) error {
	return e.p.publish(ctx, msg.ClientID, msg)
}

// DeadLetterPublisher records definitively failed events.
type DeadLetterPublisher struct{ p *Publisher }

func NewDeadLetterPublisher(p *Publisher) *DeadLetterPublisher {
	return &DeadLetterPublisher{p: p}
}

func (d *DeadLetterPublisher) Publish(ctx context.Context, record port.DeadLetterRecord) error {
	return d.p.publish(ctx, record.ClientID, record)
}

var (
	_ port.EventPublisher      = (*EventPublisher)(nil)
	_ port.DeadLetterPublisher = (*DeadLetterPublisher)(nil)
)
