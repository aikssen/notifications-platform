// Package observability answers the case statement's fifth delivery
// requirement: the capability has to be observable in near real time, so that
// an internal team can spot behaviour deviation and answer a client complaint
// about a specific notification.
//
// It provides the aggregate half of that — metrics and health. The per-event
// half is served by the delivery-result stream and the monitor service.
package observability

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// NewLogger builds the structured logger used across the service.
func NewLogger(level, service string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	return slog.New(handler).With(slog.String("service", service))
}

// Metrics holds every series this service exports.
type Metrics struct {
	EventsIngested   *prometheus.CounterVec
	DeliveryAttempts *prometheus.CounterVec
	DeliveryDuration *prometheus.HistogramVec
	MessagesConsumed *prometheus.CounterVec
	ProcessDuration  prometheus.Histogram

	registry *prometheus.Registry
}

func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()

	m := &Metrics{
		registry: reg,
		EventsIngested: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "notifications_events_ingested_total",
			Help: "Notification events ingested from the platform.",
		}, []string{"event_type"}),

		// Deliberately NOT labelled by client_id. Client identifiers are
		// unbounded, and an unbounded label creates one time series per client
		// per outcome — the classic way to take down a Prometheus instance.
		//
		// "Which of my client's events failed?" is a per-event question, and it
		// is answered by the delivery-result stream and the operations
		// dashboard. Metrics answer the aggregate question: is the platform
		// deviating from its normal behaviour?
		DeliveryAttempts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "notifications_delivery_attempts_total",
			Help: "Delivery cycles by outcome, origin and event type.",
		}, []string{"status", "dispatch_source", "event_type"}),

		DeliveryDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "notifications_delivery_duration_seconds",
			Help:    "Wall-clock time of a delivery cycle, including in-process retries.",
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
		}, []string{"status"}),

		MessagesConsumed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "notifications_consumer_messages_total",
			Help: "Kafka messages handled, by outcome.",
		}, []string{"outcome"}),

		ProcessDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "notifications_message_processing_seconds",
			Help:    "Time to process one Kafka message end to end.",
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
		}),
	}

	reg.MustRegister(
		m.EventsIngested, m.DeliveryAttempts, m.DeliveryDuration,
		m.MessagesConsumed, m.ProcessDuration,
	)
	return m
}

// MessageProcessed satisfies the consumer's observer interface, so the
// consumer never imports Prometheus.
func (m *Metrics) MessageProcessed(outcome string, d time.Duration) {
	m.MessagesConsumed.WithLabelValues(outcome).Inc()
	m.ProcessDuration.Observe(d.Seconds())
}

// Server exposes /healthz, /readyz and /metrics.
//
// A worker with no public API still needs an operational surface: without it,
// an orchestrator cannot tell a process that is starting up from one that is
// wedged.
type Server struct {
	http  *http.Server
	ready atomic.Bool
}

func NewServer(port int, m *Metrics, log *slog.Logger) *Server {
	s := &Server{}

	mux := http.NewServeMux()

	// Liveness: the process is running. Deliberately not checking dependencies
	// — a database outage should not make Kubernetes restart a healthy worker
	// in a loop.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	// Readiness: dependencies were reachable at startup and the consumer is
	// running.
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !s.ready.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"starting"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	})

	mux.Handle("GET /metrics", promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{}))

	s.http = &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Info("operational endpoints listening", slog.Int("port", port))
	return s
}

func (s *Server) MarkReady() { s.ready.Store(true) }

func (s *Server) Start(log *slog.Logger) {
	go func() {
		if err := s.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("operational server stopped", slog.Any("error", err))
		}
	}()
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}
