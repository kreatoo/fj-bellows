package control_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	controlv1 "github.com/hstern/fj-bellows/gen/fjbellows/control/v1"
	"github.com/hstern/fj-bellows/internal/control"
	controlevents "github.com/hstern/fj-bellows/internal/control/events"
	mockctl "github.com/hstern/fj-bellows/internal/control/mock"
)

func TestMetrics_ExposesPulledGauges(t *testing.T) {
	now := time.Now()
	be := &mockctl.Backend{}
	be.SetHealth(func(context.Context) control.HealthStatus {
		return control.HealthStatus{Healthy: true, LastTickAt: now.Add(-2 * time.Second)}
	})
	const stateIdle, stateBusy = "idle", "busy"
	be.SetPoolSnapshot(func() []control.WorkerView {
		return []control.WorkerView{
			{InstanceID: "a", State: stateIdle},
			{InstanceID: "b", State: stateBusy},
			{InstanceID: "c", State: stateBusy},
		}
	})
	be.SetCacheStatus(func(context.Context) *control.CacheStatus {
		return &control.CacheStatus{Present: true}
	})

	hs, _ := newTestServer(t, be)
	// Cache status is refreshed asynchronously so a provider API call can
	// never block a scrape. Wait for the first refresh before asserting its
	// value; subsequent scrapes use the cached snapshot.
	body := scrapeUntilContains(t, hs.Client(), hs.URL, `fjb_cache_present 1`)

	mustContain(t, body, `fjb_healthy 1`)
	mustContain(t, body, `fjb_workers_total 3`)
	mustContain(t, body, `fjb_workers{state="idle"} 1`)
	mustContain(t, body, `fjb_workers{state="busy"} 2`)
	mustContain(t, body, `fjb_workers{state="provisioning"} 0`) // pre-seeded
	mustContain(t, body, `fjb_cache_present 1`)
	mustContain(t, body, `fjb_last_provider_list_age_seconds -1`)
	mustContain(t, body, `fjb_last_forgejo_poll_age_seconds -1`)
	mustContain(t, body, `fjb_paused 0`)
	mustContain(t, body, `fjb_pending_provisions 0`)
}

func TestMetrics_CacheIsSnapshotNotScrapeCall(t *testing.T) {
	be := &mockctl.Backend{}
	be.SetHealth(func(context.Context) control.HealthStatus { return control.HealthStatus{} })
	be.SetPoolSnapshot(func() []control.WorkerView { return nil })
	be.SetCacheStatus(func(context.Context) *control.CacheStatus { return &control.CacheStatus{Present: true} })
	hs, _ := newTestServer(t, be)
	// The first scrape starts an asynchronous refresh; wait until it has
	// completed, then verify subsequent scrapes do not call the backend again.
	_ = scrapeMetrics(t, hs.Client(), hs.URL)
	for i := 0; i < 1000 && be.CacheStatusCalls() == 0; i++ {
		time.Sleep(time.Millisecond)
	}
	if got := be.CacheStatusCalls(); got != 1 {
		t.Fatalf("initial CacheStatus calls = %d, want 1", got)
	}
	body := scrapeMetrics(t, hs.Client(), hs.URL)
	mustContain(t, body, `fjb_cache_present 1`)
	_ = scrapeMetrics(t, hs.Client(), hs.URL)
	if got := be.CacheStatusCalls(); got != 1 {
		t.Fatalf("scrape CacheStatus calls = %d, want 1", got)
	}
}

func TestMetrics_ControlRPCLatency(t *testing.T) {
	be := &mockctl.Backend{}
	be.SetHealth(func(context.Context) control.HealthStatus { return control.HealthStatus{} })
	be.SetPoolSnapshot(func() []control.WorkerView { return nil })
	be.SetCacheStatus(func(context.Context) *control.CacheStatus { return nil })
	hs, client := newTestServer(t, be)
	if _, err := client.Health(t.Context(), connect.NewRequest(&controlv1.HealthRequest{})); err != nil {
		t.Fatalf("Health RPC: %v", err)
	}
	_ = scrapeMetrics(t, hs.Client(), hs.URL) // account for the request itself
	body := scrapeMetrics(t, hs.Client(), hs.URL)
	mustContain(t, body, `fjb_control_rpc_duration_seconds_count{code="ok",procedure="/fjbellows.control.v1.ControlService/Health"} 1`)
	mustContain(t, body, `fjb_http_requests_total{method="GET",route="/metrics",status_class="2xx"}`)
	mustContain(t, body, `fjb_http_request_duration_seconds_count{method="POST",route="/connect"}`)
}

func TestMetrics_EventTeeRecordsDurations(t *testing.T) {
	be := &mockctl.Backend{}
	be.SetHealth(func(context.Context) control.HealthStatus { return control.HealthStatus{} })
	be.SetPoolSnapshot(func() []control.WorkerView { return nil })
	be.SetCacheStatus(func(context.Context) *control.CacheStatus { return nil })
	bus := controlevents.New()
	be.SetSubscribe(bus.Subscribe)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	srv := control.NewServer(addr, be, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- srv.Run(ctx) }()
	client := &http.Client{Timeout: time.Second}
	base := "http://" + addr
	for deadline := time.Now().Add(time.Second); ; {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
		resp, reqErr := client.Do(req)
		if reqErr == nil {
			_ = resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not start: %v", reqErr)
		}
		time.Sleep(time.Millisecond)
	}
	bus.Publish(controlevents.Event{Type: "job_complete", Attrs: map[string]string{"duration_ms": "12"}})
	var body string
	for deadline := time.Now().Add(time.Second); ; {
		resp, reqErr := client.Get(base + "/metrics")
		if reqErr == nil {
			b, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			body = string(b)
			if strings.Contains(body, `fjb_events_total{type="job_complete"} 1`) {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("event was not recorded: %s", body)
		}
		time.Sleep(time.Millisecond)
	}
	mustContain(t, body, `fjb_job_duration_seconds_count 1`)
	cancel()
	select {
	case <-runErr:
	case <-time.After(time.Second):
		t.Fatal("server did not stop")
	}
}

func TestMetrics_LastTickAge_NegativeBeforeFirstTick(t *testing.T) {
	be := &mockctl.Backend{}
	be.SetHealth(func(context.Context) control.HealthStatus {
		return control.HealthStatus{} // zero LastTickAt
	})
	be.SetPoolSnapshot(func() []control.WorkerView { return nil })
	be.SetCacheStatus(func(context.Context) *control.CacheStatus { return nil })

	hs, _ := newTestServer(t, be)
	body := scrapeMetrics(t, hs.Client(), hs.URL)
	mustContain(t, body, `fjb_last_tick_age_seconds -1`)
}

func TestMetrics_EventsTotal_Registered(t *testing.T) {
	// fjb_events_total is a CounterVec wired in newMetrics. The actual
	// counter increment lives in runEventTee, which is started inside
	// Server.Run and isn't exercised by newTestServer here. This test
	// verifies the metric is registered (HELP line present) so a Prom
	// scrape against a freshly-started daemon shows it before any events.
	// The end-to-end "publish-then-scrape-shows-the-bump" path is covered
	// indirectly by the Linode e2e.
	be := &mockctl.Backend{}
	be.SetPoolSnapshot(func() []control.WorkerView { return nil })
	be.SetHealth(func(context.Context) control.HealthStatus { return control.HealthStatus{} })
	be.SetCacheStatus(func(context.Context) *control.CacheStatus { return nil })

	hs, _ := newTestServer(t, be)
	body := scrapeMetrics(t, hs.Client(), hs.URL)
	mustContain(t, body, "# HELP fjb_events_total")
}

func scrapeUntilContains(t *testing.T, hc *http.Client, baseURL, needle string) string {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		body := scrapeMetrics(t, hc, baseURL)
		if strings.Contains(body, needle) {
			return body
		}
		if time.Now().After(deadline) {
			t.Fatalf("metrics body did not contain %q within timeout; last body:\n%s", needle, body)
		}
		time.Sleep(time.Millisecond)
	}
}

// scrapeMetrics issues a GET /metrics and returns the body as a string.
func scrapeMetrics(t *testing.T, hc *http.Client, baseURL string) string {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/metrics", nil)
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: want 200 got %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func mustContain(t *testing.T, body, needle string) {
	t.Helper()
	if !strings.Contains(body, needle) {
		t.Fatalf("metrics body missing %q\nfull body:\n%s", needle, body)
	}
}

func TestMetrics_CacheStatusSlowProviderDoesNotBlockScrape(t *testing.T) {
	be := &mockctl.Backend{}
	be.SetHealth(func(context.Context) control.HealthStatus { return control.HealthStatus{} })
	be.SetPoolSnapshot(func() []control.WorkerView { return nil })
	started := make(chan struct{})
	release := make(chan struct{})
	be.SetCacheStatus(func(context.Context) *control.CacheStatus {
		close(started)
		<-release
		return &control.CacheStatus{Present: true}
	})

	hs, _ := newTestServer(t, be)
	done := make(chan string, 1)
	go func() { done <- scrapeMetrics(t, hs.Client(), hs.URL) }()
	select {
	case body := <-done:
		mustContain(t, body, `fjb_cache_present 0`)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("scrape blocked on CacheStatus")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("cache refresh did not start")
	}

	close(release)
	deadline := time.Now().Add(time.Second)
	for {
		body := scrapeMetrics(t, hs.Client(), hs.URL)
		if strings.Contains(body, `fjb_cache_present 1`) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("cache status was not refreshed; last body:\n%s", body)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestMetrics_CacheStatusConcurrentScrapesRefreshOnce(t *testing.T) {
	be := &mockctl.Backend{}
	be.SetHealth(func(context.Context) control.HealthStatus { return control.HealthStatus{} })
	be.SetPoolSnapshot(func() []control.WorkerView { return nil })
	started := make(chan struct{})
	release := make(chan struct{})
	be.SetCacheStatus(func(context.Context) *control.CacheStatus {
		close(started)
		<-release
		return &control.CacheStatus{Present: true}
	})

	hs, _ := newTestServer(t, be)
	const scrapes = 8
	done := make(chan struct{}, scrapes)
	for i := 0; i < scrapes; i++ {
		go func() {
			_ = scrapeMetrics(t, hs.Client(), hs.URL)
			done <- struct{}{}
		}()
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("cache refresh did not start")
	}
	for i := 0; i < scrapes; i++ {
		select {
		case <-done:
		case <-time.After(200 * time.Millisecond):
			t.Fatal("concurrent scrape blocked on CacheStatus")
		}
	}
	if got := be.CacheStatusCalls(); got != 1 {
		t.Fatalf("CacheStatus calls while refresh in flight: want 1 got %d", got)
	}
	close(release)
}
