package observability

import (
	"context"

	"github.com/aikssen/notifications-platform/services/dispatch-service/internal/application/port"
)

// MeteredResultPublisher derives metrics from the same delivery-result stream
// the operations dashboard consumes.
//
// Instrumenting here rather than inside the use case keeps the core free of a
// metrics library, and means the numbers on the Grafana board and the events on
// the dashboard can never disagree: they come from one fact, emitted once.
type MeteredResultPublisher struct {
	next    port.ResultPublisher
	metrics *Metrics
}

func NewMeteredResultPublisher(next port.ResultPublisher, m *Metrics) *MeteredResultPublisher {
	return &MeteredResultPublisher{next: next, metrics: m}
}

func (p *MeteredResultPublisher) Publish(ctx context.Context, result port.DeliveryResult) error {
	p.metrics.DeliveryAttempts.WithLabelValues(
		result.Status.String(),
		result.DispatchSource.String(),
		result.EventType,
	).Inc()

	p.metrics.DeliveryDuration.
		WithLabelValues(result.Status.String()).
		Observe(float64(result.DurationMS) / 1000)

	return p.next.Publish(ctx, result)
}

var _ port.ResultPublisher = (*MeteredResultPublisher)(nil)
