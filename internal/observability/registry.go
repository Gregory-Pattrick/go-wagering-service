// Package observability contains process telemetry outside the financial domain.
package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"net/http"
	"strconv"
	"time"
)

type Registry struct {
	Prom         *prometheus.Registry
	Outcomes     *prometheus.CounterVec
	Failures     *prometheus.CounterVec
	Retries      *prometheus.CounterVec
	Steps        *prometheus.CounterVec
	Duration     *prometheus.HistogramVec
	HTTP         *prometheus.CounterVec
	HTTPDuration *prometheus.HistogramVec
	Dependencies *prometheus.GaugeVec
	Facts        *prometheus.GaugeVec
	Queues       *prometheus.GaugeVec
	Sample       prometheus.Gauge
}

func NewRegistry() *Registry {
	r := &Registry{Prom: prometheus.NewRegistry()}
	r.Outcomes = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "wagering_financial_outcomes_total", Help: "Committed application outcomes, including labeled replays."}, []string{"transport", "status", "replay"})
	r.Failures = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "wagering_operation_errors_total", Help: "Observed operation errors by bounded category."}, []string{"operation", "category"})
	r.Retries = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "wagering_retries_total", Help: "Observed retries or successful rescheduling by operation."}, []string{"operation"})
	r.Steps = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "wagering_worker_steps_total", Help: "Successful worker actions; publication means database acknowledgment."}, []string{"operation"})
	r.Duration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "wagering_operation_duration_seconds", Help: "Operation duration including database lock waits.", Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 15}}, []string{"operation"})
	r.HTTP = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "wagering_http_requests_total", Help: "HTTP requests by bounded route and response class."}, []string{"route", "class"})
	r.HTTPDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "wagering_http_duration_seconds", Help: "HTTP latency by bounded route.", Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}}, []string{"route"})
	r.Dependencies = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "wagering_dependency_up", Help: "Last dependency probe result; zero on failure."}, []string{"dependency"})
	r.Facts = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "wagering_database_state", Help: "Global database snapshot, API process only; NaN when unavailable."}, []string{"measure"})
	r.Queues = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "wagering_sqs_messages", Help: "Broker-reported approximate queue depth; DLQ depth is not a redrive counter."}, []string{"queue", "state"})
	r.Sample = prometheus.NewGauge(prometheus.GaugeOpts{Name: "wagering_dependency_sample_timestamp_seconds", Help: "Unix time of latest completed dependency probe."})
	r.Prom.MustRegister(r.Outcomes, r.Failures, r.Retries, r.Steps, r.Duration, r.HTTP, r.HTTPDuration, r.Dependencies, r.Facts, r.Queues, r.Sample, collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return r
}
func (r *Registry) Handler() http.Handler { return promhttp.HandlerFor(r.Prom, promhttp.HandlerOpts{}) }
func (r *Registry) FinancialCompleted(transport, status string, replay bool) {
	r.Outcomes.WithLabelValues(transport, status, strconv.FormatBool(replay)).Inc()
}
func (r *Registry) FinancialError(transport, category string) {
	r.Failures.WithLabelValues(transport, category).Inc()
}
func (r *Registry) Elapsed(operation string, start time.Time) {
	r.Duration.WithLabelValues(operation).Observe(time.Since(start).Seconds())
}
