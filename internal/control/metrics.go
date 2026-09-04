package control

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
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
// answers in memory. Cache status is the exception: a provider may need to
// make a cloud API call to get its live VM state, so it is collected through
// cacheStatusCollector below rather than from a GaugeFunc.
//
// The registry is server-local (not the package's default) so unit tests
// running in parallel don't collide on the global registry.
type metrics struct {
	reg                  *prometheus.Registry
	eventsTotal          *prometheus.CounterVec
	reconcileTicksTotal  prometheus.Counter
	reconcileErrorsTotal prometheus.Counter
	jobsFailedTotal      prometheus.Counter
	operationErrorsTotal *prometheus.CounterVec
	reconcileDuration    prometheus.Observer
	provisionReady       prometheus.Observer
	jobDuration          prometheus.Observer
	destroyDuration      prometheus.Observer
	rpcDuration          *prometheus.HistogramVec
	rpcErrors            *prometheus.CounterVec
	streamDuration       *prometheus.HistogramVec
	streamsActive        prometheus.Gauge
	httpDuration         *prometheus.HistogramVec
	httpRequests         *prometheus.CounterVec
	cacheStatus          *cacheStatusCollector
}

var operationDurationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 300, 900}

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

	// CacheStatus can perform a provider API request (Linode currently does),
	// and Prometheus expects a scrape to complete quickly. The collector emits
	// the last snapshot and refreshes it asynchronously, at most once per
	// cacheStatusRefreshInterval. A slow provider therefore cannot hold up the
	// /metrics handler or make concurrent scrapes stampede the API.
	cacheStatus := newCacheStatusCollector(backend, now)
	reg.MustRegister(cacheStatus)

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
	operationErrorsTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "fjb_operation_errors_total",
		Help: "Failed orchestrator/provider operations, by low-cardinality operation.",
	}, []string{"operation"})
	reg.MustRegister(operationErrorsTotal)
	jobsFailedTotal := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "fjb_jobs_failed_total",
		Help: "Jobs whose runner registration or execution failed.",
	})
	reg.MustRegister(reconcileTicksTotal, reconcileErrorsTotal, jobsFailedTotal)
	reconcileDuration := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "fjb_reconcile_duration_seconds", Help: "Reconciliation pass duration.", Buckets: operationDurationBuckets,
	})
	provisionReady := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "fjb_provision_to_ready_seconds", Help: "Time from provision start until worker readiness.", Buckets: operationDurationBuckets,
	})
	jobDuration := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "fjb_job_duration_seconds", Help: "Time spent dispatching a job, including registration and execution.", Buckets: operationDurationBuckets,
	})
	destroyDuration := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "fjb_destroy_duration_seconds", Help: "Provider worker destruction duration.", Buckets: operationDurationBuckets,
	})
	reg.MustRegister(reconcileDuration, provisionReady, jobDuration, destroyDuration)
	rpcDuration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "fjb_control_rpc_duration_seconds", Help: "Control RPC duration by procedure and Connect status code.", Buckets: operationDurationBuckets,
	}, []string{"procedure", "code"})
	rpcErrors := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "fjb_control_rpc_errors_total", Help: "Control RPC errors by procedure and Connect status code.",
	}, []string{"procedure", "code"})
	streamDuration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "fjb_control_stream_duration_seconds", Help: "Control streaming RPC lifetime by procedure and terminal status code.", Buckets: operationDurationBuckets,
	}, []string{"procedure", "code"})
	streamsActive := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "fjb_control_streams_active", Help: "Control streaming RPCs currently open.",
	})
	reg.MustRegister(rpcDuration, rpcErrors, streamDuration, streamsActive)
	httpDuration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "fjb_http_request_duration_seconds", Help: "HTTP request duration by method and bounded route.", Buckets: operationDurationBuckets,
	}, []string{"method", "route"})
	httpRequests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "fjb_http_requests_total", Help: "HTTP requests by method, bounded route, and status class.",
	}, []string{"method", "route", "status_class"})
	reg.MustRegister(httpDuration, httpRequests)
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
		operationErrorsTotal: operationErrorsTotal,
		reconcileDuration:    reconcileDuration,
		provisionReady:       provisionReady,
		jobDuration:          jobDuration,
		destroyDuration:      destroyDuration,
		rpcDuration:          rpcDuration,
		rpcErrors:            rpcErrors,
		streamDuration:       streamDuration,
		streamsActive:        streamsActive,
		httpDuration:         httpDuration,
		httpRequests:         httpRequests,
		cacheStatus:          cacheStatus,
	}
}

func (m *metrics) interceptor() connect.Interceptor {
	return metricsInterceptor{m: m}
}

type metricsInterceptor struct{ m *metrics }

func (i metricsInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		started := time.Now()
		resp, err := next(ctx, req)
		code := rpcCode(err)
		procedure := req.Spec().Procedure
		i.m.rpcDuration.WithLabelValues(procedure, code).Observe(time.Since(started).Seconds())
		if err != nil {
			i.m.rpcErrors.WithLabelValues(procedure, code).Inc()
		}
		return resp, err
	}
}

func (i metricsInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		started := time.Now()
		i.m.streamsActive.Inc()
		defer i.m.streamsActive.Dec()
		err := next(ctx, conn)
		code := rpcCode(err)
		i.m.streamDuration.WithLabelValues(conn.Spec().Procedure, code).Observe(time.Since(started).Seconds())
		return err
	}
}

func (i metricsInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func rpcCode(err error) string {
	if err == nil {
		return "ok"
	}
	return connect.CodeOf(err).String()
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
				observeDuration(m.reconcileDuration, ev.Attrs["duration_ms"])
				if n, err := strconv.Atoi(ev.Attrs["errors"]); err == nil {
					for i := 0; i < n; i++ {
						m.reconcileErrorsTotal.Inc()
					}
				}
			case "reconcile_error", "worker_provision_failed", "worker_destroy_failed":
				operation := ev.Attrs["operation"]
				if operation == "" {
					operation = ev.Type
				}
				m.operationErrorsTotal.WithLabelValues(operation).Inc()
				if ev.Type == "worker_provision_failed" {
					observeDuration(m.provisionReady, ev.Attrs["duration_ms"])
				}
				if ev.Type == "worker_destroy_failed" {
					observeDuration(m.destroyDuration, ev.Attrs["duration_ms"])
				}
			case "job_failed":
				m.jobsFailedTotal.Inc()
				if operation := ev.Attrs["operation"]; operation != "" {
					m.operationErrorsTotal.WithLabelValues(operation).Inc()
				}
				observeDuration(m.jobDuration, ev.Attrs["duration_ms"])
			case "worker_ready":
				observeDuration(m.provisionReady, ev.Attrs["duration_ms"])
			case "job_complete":
				observeDuration(m.jobDuration, ev.Attrs["duration_ms"])
			case "worker_reaped":
				observeDuration(m.destroyDuration, ev.Attrs["duration_ms"])
			}
		}
	}
}

// runCachePoller refreshes the cache snapshot independently of scrapes. It
// starts immediately so a daemon with no Prometheus scrape still has a fresh
// value, then relies on the collector's single-flight guard and TTL. Keeping
// this loop under Run's context also stops the poller on shutdown.
func (m *metrics) runCachePoller(ctx context.Context, _ Backend, log *slog.Logger) {
	if ctx.Err() != nil {
		return
	}
	m.cacheStatus.refreshIfStale(ctx)
	ticker := time.NewTicker(cacheStatusRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.cacheStatus.refreshIfStale(ctx)
			if log != nil {
				log.Debug("metrics cache status refresh requested")
			}
		}
	}
}

func observeDuration(observer prometheus.Observer, raw string) {
	if observer == nil {
		return
	}
	ms, err := strconv.ParseFloat(raw, 64)
	if err == nil && ms >= 0 {
		observer.Observe(ms / 1000)
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

func (m *metrics) httpMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		sw := &metricsResponseWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		method := r.Method
		if method == "" {
			method = "other"
		}
		route := metricRoute(r.URL.Path)
		m.httpDuration.WithLabelValues(method, route).Observe(time.Since(started).Seconds())
		m.httpRequests.WithLabelValues(method, route, statusClass(sw.status)).Inc()
	})
}

type metricsResponseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *metricsResponseWriter) WriteHeader(status int) {
	if !w.wroteHeader {
		w.status = status
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *metricsResponseWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}
func (w *metricsResponseWriter) Flush() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (w *metricsResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func metricMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodHead, http.MethodOptions:
		return method
	default:
		return "other"
	}
}

func statusClass(status int) string {
	if status == 0 {
		status = http.StatusOK
	}
	return strconv.Itoa(status/100) + "xx"
}

func metricRoute(path string) string {
	switch path {
	case "/metrics", "/healthz":
		return path
	}
	if strings.HasPrefix(path, "/fjbellows.control.v1.ControlService/") {
		return "/connect"
	}
	return "/other"
}

func (m *metrics) handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{Registry: m.reg})
}

// cacheStatusRefreshInterval bounds how long the cache presence gauge may
// remain stale. CacheStatus is deliberately not called synchronously from
// Collect: provider implementations are allowed to query a remote API.
const (
	cacheStatusRefreshInterval = 30 * time.Second
	cacheStatusRequestTimeout  = 5 * time.Second
)

// cacheStatusCollector exposes managed-cache presence from a mutex-protected
// snapshot. A refresh is kicked off by Collect when the snapshot is absent or
// older than cacheStatusRefreshInterval, but Collect itself only takes the
// lock long enough to copy the snapshot. This is important because a provider
// API outage must not turn a Prometheus scrape into a hanging HTTP request.
//
// The initial value is absent (0), and a first scrape starts the refresh. A
// nil CacheStatus is cached as an absent cache, too, so an unconfigured
// provider does not cause one API call per scrape.
type cacheStatusCollector struct {
	backend Backend
	now     func() time.Time

	mu          sync.Mutex
	status      CacheStatus
	hasStatus   bool
	refreshedAt time.Time
	refreshing  bool
}

var (
	cachePresentDesc = prometheus.NewDesc(
		"fjb_cache_present",
		"1 if the managed cache VM is provisioned; 0 otherwise.",
		nil, nil,
	)
	cacheStatusAgeDesc = prometheus.NewDesc(
		"fjb_cache_status_age_seconds",
		"Seconds since cache status was refreshed; -1 if never.",
		nil, nil,
	)
	cacheRefreshingDesc = prometheus.NewDesc(
		"fjb_cache_status_refreshing",
		"1 while a cache status refresh is in flight; 0 otherwise.",
		nil, nil,
	)
)

func newCacheStatusCollector(backend Backend, now func() time.Time) *cacheStatusCollector {
	return &cacheStatusCollector{backend: backend, now: now}
}

func (c *cacheStatusCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- cachePresentDesc
	ch <- cacheStatusAgeDesc
	ch <- cacheRefreshingDesc
}

func (c *cacheStatusCollector) Collect(ch chan<- prometheus.Metric) {
	now := c.now()

	c.mu.Lock()
	status := c.status
	hasStatus := c.hasStatus
	stale := !hasStatus || now.Sub(c.refreshedAt) >= cacheStatusRefreshInterval
	if stale && !c.refreshing {
		c.refreshing = true
		go c.refresh(context.Background())
	}
	isRefreshing := c.refreshing
	c.mu.Unlock()

	present := 0.0
	if hasStatus && status.Present {
		present = 1
	}
	age := -1.0
	if hasStatus {
		age = now.Sub(c.refreshedAt).Seconds()
		if age < 0 {
			age = 0
		}
	}
	refreshing := 0.0
	if isRefreshing {
		refreshing = 1
	}
	ch <- prometheus.MustNewConstMetric(cachePresentDesc, prometheus.GaugeValue, present)
	ch <- prometheus.MustNewConstMetric(cacheStatusAgeDesc, prometheus.GaugeValue, age)
	ch <- prometheus.MustNewConstMetric(cacheRefreshingDesc, prometheus.GaugeValue, refreshing)
}

func (c *cacheStatusCollector) refreshIfStale(ctx context.Context) {
	now := c.now()
	c.mu.Lock()
	stale := !c.hasStatus || now.Sub(c.refreshedAt) >= cacheStatusRefreshInterval
	if stale && !c.refreshing {
		c.refreshing = true
		go c.refresh(ctx)
	}
	c.mu.Unlock()
}

func (c *cacheStatusCollector) refresh(ctx context.Context) {
	// CacheStatus has no error return, but providers should honor context
	// cancellation. The timeout protects implementations that do honor it;
	// Collect remains non-blocking even for implementations that do not.
	ctx, cancel := context.WithTimeout(ctx, cacheStatusRequestTimeout)
	status := c.backend.CacheStatus(ctx)
	cancel()
	refreshedAt := c.now()

	c.mu.Lock()
	if status != nil {
		c.status = *status
	} else {
		c.status = CacheStatus{}
	}
	c.hasStatus = true
	c.refreshedAt = refreshedAt
	c.refreshing = false
	c.mu.Unlock()
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
	"reconcile_error",
	"worker_provision_failed",
	"worker_destroy_failed",
	"job_failed",
	"reconciler_paused",
	"reconciler_resumed",
	"stream_opened",
}
