// Package observability exposes the retry service's operational surface.
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
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})).
		With(slog.String("service", service))
}

type Metrics struct {
	Requeued  *prometheus.CounterVec
	Exhausted *prometheus.CounterVec
	Reclaimed prometheus.Counter
	Claimed   prometheus.Counter
	CycleTime prometheus.Histogram

	// Backlog is the single most useful number for an on-call engineer: how
	// long the oldest event has been waiting. A rising value means deliveries
	// are failing faster than they are being retried.
	Backlog prometheus.Gauge

	registry *prometheus.Registry
}

func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()

	m := &Metrics{
		registry: reg,
		Requeued: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "notifications_retries_scheduled_total",
			Help: "Events put back on the delivery topic by the retry service.",
		}, []string{"event_type"}),

		Exhausted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "notifications_retries_exhausted_total",
			Help: "Events declared definitively failed after spending their retry budget.",
		}, []string{"event_type"}),

		Reclaimed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "notifications_deliveries_reclaimed_total",
			Help: "Deliveries abandoned mid-flight and returned to the retry queue.",
		}),

		Claimed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "notifications_retries_claimed_total",
			Help: "Events claimed for another attempt.",
		}),

		CycleTime: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "notifications_retry_cycle_seconds",
			Help:    "Duration of one retry polling cycle.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.5, 1, 5, 15},
		}),

		Backlog: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "notifications_oldest_retrying_age_seconds",
			Help: "Age of the oldest event still waiting to be retried.",
		}),
	}

	reg.MustRegister(m.Requeued, m.Exhausted, m.Reclaimed, m.Claimed, m.CycleTime, m.Backlog)
	return m
}

// Observer adapts the metric set to the interface the use case declares, so
// the application layer never mentions Prometheus.
type Observer struct{ m *Metrics }

func NewObserver(m *Metrics) *Observer { return &Observer{m: m} }

func (o *Observer) Requeued(eventType string) {
	o.m.Requeued.WithLabelValues(eventType).Inc()
}

func (o *Observer) Exhausted(eventType string) {
	o.m.Exhausted.WithLabelValues(eventType).Inc()
}

func (o *Observer) Reclaimed(n int) {
	o.m.Reclaimed.Add(float64(n))
}

func (o *Observer) BacklogAge(d time.Duration) {
	o.m.Backlog.Set(d.Seconds())
}

func (o *Observer) CycleFinished(claimed int, d time.Duration) {
	o.m.Claimed.Add(float64(claimed))
	o.m.CycleTime.Observe(d.Seconds())
}

type Server struct {
	http  *http.Server
	ready atomic.Bool
}

func NewServer(port int, m *Metrics, log *slog.Logger) *Server {
	s := &Server{}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

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

func (s *Server) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }
