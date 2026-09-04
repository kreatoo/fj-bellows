package control

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// MetricsStatsProvider is an optional backend extension exposing cheap
// in-memory lifecycle counters. Keeping it optional preserves compatibility
// with lightweight control-plane fakes and third-party backends.
type MetricsStatsProvider interface {
	PendingProvisions() int
	ActiveJobs() int
	DispatchingJobs() int
	Destroying() int
}

// metrics owns the per-server Prometheus registry plus an event-bus
// subscriber that tees events into a counter. Pulled gauges query the
// backend on every scrape — cheap because the backend already keeps the
// answers in memory.
//
// The registry is server-local (not the package's default) so unit tests
// running in parallel don't collide on the global registry.
type metrics struct {
	reg                  *prometheus.Registry
	eventsTotal          *prometheus.CounterVec
	reconcileTicksTotal  prometheus.Counter
	reconcileErrorsTotal prometheus.Counter
	jobsFailedTotal      prometheus.Counter
}

func newMetrics(backend Backend, now func() time.Time) *metrics {
	reg := prometheus.NewRegistry()

	// Workers: emit fjb_workers_total + fjb_workers{state} in one atomic
	// Collect call so a scrape sees a coherent snapshot. A separate GaugeVec
	// updated as a side effect of a GaugeFunc would lag by one scrape because
	// of collection-order ordering.
	reg.MustRegister(&workerCollector{backend: backend})

	reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "fjb_healthy",
		Help: "1 if reconcile + upstream probes are fresh; 0 otherwise.",
	}, func() float64 {
		if backend.Health(context.Background()).Healthy {
			return 1
		}
		return 0
	}))

	reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "fjb_last_tick_age_seconds",
		Help: "Seconds since the orchestrator's most recent reconcile completed; -1 if never.",
	}, func() float64 {
		s := backend.Health(context.Background())
		return healthAge(now, s.LastTickAt)
	}))

	reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "fjb_last_provider_list_age_seconds",
		Help: "Seconds since the provider instance list last succeeded; -1 if never.",
	}, func() float64 {
		return healthAge(now, backend.Health(context.Background()).LastProviderListAt)
	}))

	reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "fjb_last_forgejo_poll_age_seconds",
		Help: "Seconds since the Forgejo waiting-job poll last succeeded; -1 if never.",
	}, func() float64 {
		return healthAge(now, backend.Health(context.Background()).LastForgejoPollAt)
	}))

	reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "fjb_paused",
		Help: "1 if automatic reconciliation is paused; 0 otherwise.",
	}, func() float64 {
		if backend.Health(context.Background()).Paused {
			return 1
		}
		return 0
	}))

	reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "fjb_cache_present",
		Help: "1 if the managed pull-through cache VM is provisioned; 0 otherwise.",
	}, func() float64 {
		s := backend.CacheStatus(context.Background())
		if s != nil && s.Present {
			return 1
		}
		return 0
	}))

	if stats, ok := backend.(MetricsStatsProvider); ok {
		reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "fjb_pending_provisions",
			Help: "Provision operations currently in flight.",
		}, func() float64 { return float64(stats.PendingProvisions()) }))
		reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "fjb_active_jobs",
			Help: "Jobs currently assigned to workers.",
		}, func() float64 { return float64(stats.ActiveJobs()) }))
		reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "fjb_dispatching_jobs",
			Help: "Job handles currently being dispatched.",
		}, func() float64 { return float64(stats.DispatchingJobs()) }))
		reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "fjb_destroying_workers",
			Help: "Worker destroy operations currently in flight.",
		}, func() float64 { return float64(stats.Destroying()) }))
	}

	eventsTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "fjb_events_total",
		Help: "State-transition events emitted by the orchestrator, by type.",
	}, []string{"type"})
	reg.MustRegister(eventsTotal)
	reconcileTicksTotal := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "fjb_reconcile_ticks_total",
		Help: "Completed reconciliation passes.",
	})
	reconcileErrorsTotal := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "fjb_reconcile_errors_total",
		Help: "Top-level reconciliation steps that failed.",
	})
	jobsFailedTotal := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "fjb_jobs_failed_total",
		Help: "Jobs whose runner registration or execution failed.",
	})
	reg.MustRegister(reconcileTicksTotal, reconcileErrorsTotal, jobsFailedTotal)
	// Pre-initialize the well-known event types so a fresh-start scrape
	// shows zero rows instead of omitting the series entirely (Prom only
	// emits TYPE/HELP for metrics with at least one observed labelset).
	for _, t := range knownEventTypes {
		eventsTotal.WithLabelValues(t).Add(0)
	}

	return &metrics{
		reg:                  reg,
		eventsTotal:          eventsTotal,
		reconcileTicksTotal:  reconcileTicksTotal,
		reconcileErrorsTotal: reconcileErrorsTotal,
		jobsFailedTotal:      jobsFailedTotal,
	}
}

// runEventTee subscribes to the backend's event stream and increments the
// per-type counter for each event. Returns when ctx is cancelled, or when
// the bus drops the subscriber (logged; shouldn't happen for the small
// fan-out we generate here).
func (m *metrics) runEventTee(ctx context.Context, backend Backend, log *slog.Logger) {
	ch, cancel := backend.Subscribe()
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				if log != nil {
					log.Warn("metrics event tee: bus dropped subscriber")
				}
				return
			}
			m.eventsTotal.WithLabelValues(ev.Type).Inc()
			switch ev.Type {
			case "reconcile_tick":
				m.reconcileTicksTotal.Inc()
				if n, err := strconv.Atoi(ev.Attrs["errors"]); err == nil {
					for i := 0; i < n; i++ {
						m.reconcileErrorsTotal.Inc()
					}
				}
			case "job_failed":
				m.jobsFailedTotal.Inc()
			}
		}
	}
}

func healthAge(now func() time.Time, at time.Time) float64 {
	if at.IsZero() {
		return -1
	}
	age := now().Sub(at).Seconds()
	// A clock adjustment should not make a future success look infinitely
	// healthy; expose it as zero age while Health remains authoritative.
	if age < 0 {
		return 0
	}
	return age
}

func (m *metrics) handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{Registry: m.reg})
}

// workerCollector emits fjb_workers_total and fjb_workers{state} in one
// coherent Collect pass so the by-state values stay in sync with the total.
type workerCollector struct {
	backend Backend
}

var (
	workersTotalDesc = prometheus.NewDesc(
		"fjb_workers_total",
		"Total worker VMs currently in the pool (sum across all states).",
		nil, nil,
	)
	workersByStateDesc = prometheus.NewDesc(
		"fjb_workers",
		"Worker VMs currently in the pool, by state.",
		[]string{"state"}, nil,
	)
)

func (c *workerCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- workersTotalDesc
	ch <- workersByStateDesc
}

func (c *workerCollector) Collect(ch chan<- prometheus.Metric) {
	view := c.backend.PoolSnapshot()
	byState := map[string]int{}
	for _, w := range view {
		byState[w.State]++
	}
	ch <- prometheus.MustNewConstMetric(workersTotalDesc, prometheus.GaugeValue, float64(len(view)))
	for _, s := range knownStates {
		ch <- prometheus.MustNewConstMetric(workersByStateDesc, prometheus.GaugeValue, float64(byState[s]), s)
	}
}

// knownStates is the closed set of NodeState values the orchestrator emits.
// Pre-seeding ensures every scrape shows the full label set rather than
// disappearing labels between transitions.
var knownStates = []string{"provisioning", "idle", "busy", "draining", "removing"}

// knownEventTypes is the closed set of event Type slugs the orchestrator
// emits. Pre-seeding the counter at zero for each so the HELP/TYPE lines
// are present on a fresh-start scrape.
var knownEventTypes = []string{
	"worker_provisioned",
	"worker_ready",
	"worker_busy",
	"worker_idle",
	"worker_reaped",
	"worker_adopted",
	"worker_dropped",
	"job_dispatched",
	"job_complete",
	"zombie_reaped",
	"reconcile_tick",
	"job_failed",
	"reconciler_paused",
	"reconciler_resumed",
	"stream_opened",
}
