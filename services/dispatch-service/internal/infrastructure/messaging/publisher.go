package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/segmentio/kafka-go"

	"github.com/aikssen/notifications-platform/services/dispatch-service/internal/application/port"
)

// Publisher writes to a Kafka topic.
type Publisher struct {
	writer *kafka.Writer
}

func NewPublisher(brokers []string, topic string) *Publisher {
	return &Publisher{
		writer: &kafka.Writer{
			Addr:         kafka.TCP(brokers...),
			Topic:        topic,
			Balancer:     &kafka.Hash{},
			RequiredAcks: kafka.RequireAll,
			BatchTimeout: 20 * time.Millisecond,
			Async:        false,
		},
	}
}

func (p *Publisher) PublishJSON(ctx context.Context, key string, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode message: %w", err)
	}
	return p.writer.WriteMessages(ctx, kafka.Message{
		Key:   []byte(key),
		Value: body,
	})
}

func (p *Publisher) Close() error { return p.writer.Close() }

// ResultPublisher emits delivery outcomes to the delivery-result topic.
//
// This stream is what the monitoring service consumes. Publishing the outcome
// as an event, rather than having the dashboard poll the database, is what
// keeps observability from becoming a dependency of the delivery path.
type ResultPublisher struct {
	publisher *Publisher
}

func NewResultPublisher(p *Publisher) *ResultPublisher {
	return &ResultPublisher{publisher: p}
}

func (r *ResultPublisher) Publish(ctx context.Context, result port.DeliveryResult) error {
	// Keyed by client so a single client's outcomes stay ordered.
	return r.publisher.PublishJSON(ctx, result.ClientID, result)
}

var _ port.ResultPublisher = (*ResultPublisher)(nil)
