package orchestrator

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hstern/fj-bellows/internal/forgejo"
	omock "github.com/hstern/fj-bellows/internal/orchestrator/mock"
	"github.com/hstern/fj-bellows/internal/provider"
	pmock "github.com/hstern/fj-bellows/internal/provider/mock"
)

// wedgedFixture builds an orchestrator whose single node has a dispatch
// goroutine that never returns — the shape of the 2026-09-10 production
// wedge, where an SSH connection died without a reset and one-job --wait
// blocked forever. The provider stops listing the instance after the first
// reconcile so the stale-reap pass's destroy is observable without
// re-adoption.
type wedgedFixture struct {
	o         *Orchestrator
	destroyed atomic.Int32
	blockJob  chan struct{}
	listCalls atomic.Int32
	base      time.Time
}

func newWedgedFixture(t *testing.T, maxJobRuntime time.Duration) *wedgedFixture {
	t.Helper()
	f := &wedgedFixture{base: time.Now(), blockJob: make(chan struct{})}
	prov := &pmock.Provider{
		ListFn: func(context.Context, string) ([]provider.Instance, error) {
			// The instance keeps listing (it exists on the provider — that's
			// precisely why a wedged busy node survives syncPool) until the
			// destroy lands; then provider truth is "gone" so a follow-up
			// reconcile doesn't re-adopt it mid-test.
			if f.listCalls.Add(1) > 2 {
				return nil, nil
			}
			return []provider.Instance{{ID: "100", IPv4: testIP, CreatedAt: f.base}}, nil
		},
		DestroyFn: func(context.Context, string) error {
			f.destroyed.Add(1)
			return nil
		},
	}
	jobs := &omock.JobSource{
		WaitingJobsFn: func(context.Context) ([]forgejo.WaitingJob, error) {
			return []forgejo.WaitingJob{{Handle: "h1", Labels: []string{labelUbuntu}}}, nil
		},
	}
	disp := &omock.Dispatcher{
		RunJobFn: func(context.Context, string, string, forgejo.Registration, forgejo.WaitingJob) error {
			<-f.blockJob // wedged: nothing will ever return this node to Idle
			return nil
		},
	}
	cfg := baseConfig()
	cfg.Teardown.MaxJobRuntime = maxJobRuntime
	f.o = New(cfg, prov, jobs, disp, nil)
	t.Cleanup(func() { close(f.blockJob) })
	return f
}

func (f *wedgedFixture) dispatchWedgedJob(t *testing.T) {
	t.Helper()
	r := f.o.Reconcile(context.Background())
	if r.Dispatched != 1 {
		t.Fatalf("dispatched = %d, want 1", r.Dispatched)
	}
	waitFor(t, "node busy with dispatch stamp", func() bool {
		n, ok := f.o.pool.Get("100")
		return ok && n.State == StateBusy && !n.BusySince.IsZero() && n.CurrentJob == "h1"
	})
}

// TestStaleBusyWorkerForceReaped is the regression test for the production
// wedge: a busy node whose dispatch goroutine is hung past MaxJobRuntime is
// destroyed and dropped even though billing-policy teardown skips busy
// nodes and no goroutine will ever return it to Idle.
func TestStaleBusyWorkerForceReaped(t *testing.T) {
	f := newWedgedFixture(t, time.Hour)
	f.dispatchWedgedJob(t)

	// Two hours in, the job cannot still be legitimate: the dispatch
	// goroutine is wedged. The next tick must force-reap the node.
	f.o.now = func() time.Time { return f.base.Add(2 * time.Hour) }
	r := f.o.Reconcile(context.Background())
	if r.Reaped != 1 {
		t.Fatalf("reaped = %d, want 1", r.Reaped)
	}
	// startDestroy runs async — wait for the destroy to land and drop the node.
	waitFor(t, "stale busy node destroyed and dropped", func() bool {
		if f.destroyed.Load() != 1 {
			return false
		}
		_, ok := f.o.pool.Get("100")
		return !ok
	})
	if _, ok := f.o.pool.Get("100"); ok {
		t.Fatal("stale busy node must be dropped from the pool")
	}
}

// TestBusyNodeUnderCapSurvives pins that a legitimately running job is
// never touched by the safety net: under the cap, the busy node is left
// alone despite the clock having moved.
func TestBusyNodeUnderCapSurvives(t *testing.T) {
	f := newWedgedFixture(t, time.Hour)
	f.dispatchWedgedJob(t)

	f.o.now = func() time.Time { return f.base.Add(30 * time.Minute) }
	r := f.o.Reconcile(context.Background())
	if r.Reaped != 0 {
		t.Fatalf("reaped = %d, want 0", r.Reaped)
	}
	if f.destroyed.Load() != 0 {
		t.Fatalf("Destroy called %d times, want 0", f.destroyed.Load())
	}
	n, ok := f.o.pool.Get("100")
	if !ok || n.State != StateBusy {
		t.Fatalf("busy node must survive under the cap; state=%v ok=%v", n.State, ok)
	}
}

// TestStaleBusyDisabledByZeroCap pins the opt-out: with MaxJobRuntime zero
// (orchestrator-level "disabled"; config.Load supplies the 6h default), a
// busy node is never force-reaped however far the clock runs.
func TestStaleBusyDisabledByZeroCap(t *testing.T) {
	f := newWedgedFixture(t, 0)
	f.dispatchWedgedJob(t)

	f.o.now = func() time.Time { return f.base.Add(1000 * time.Hour) }
	r := f.o.Reconcile(context.Background())
	if r.Reaped != 0 {
		t.Fatalf("reaped = %d, want 0 (cap disabled)", r.Reaped)
	}
	if _, ok := f.o.pool.Get("100"); !ok {
		t.Fatal("busy node must survive with the cap disabled")
	}
}

// TestMarkBusyMarkIdleRoundTrip pins the pool-level state machine the
// stale-busy reap depends on: MarkBusy stamps dispatch time; MarkIdle
// clears it and moves LastBusy to the completion instant.
func TestMarkBusyMarkIdleRoundTrip(t *testing.T) {
	p := NewPool()
	p.Put(&Node{InstanceID: "100", State: StateIdle, LastBusy: time.Unix(0, 0)})

	busyAt := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	if !p.MarkBusy("100", "h1", busyAt) {
		t.Fatal("MarkBusy on a known node should succeed")
	}
	n, ok := p.Get("100")
	if !ok || n.State != StateBusy || n.CurrentJob != "h1" || !n.BusySince.Equal(busyAt) {
		t.Fatalf("after MarkBusy: %+v ok=%v", n, ok)
	}

	idleAt := busyAt.Add(5 * time.Minute)
	if !p.MarkIdle("100", idleAt) {
		t.Fatal("MarkIdle on a known node should succeed")
	}
	n, ok = p.Get("100")
	if !ok || n.State != StateIdle || n.CurrentJob != "" || !n.BusySince.IsZero() || !n.LastBusy.Equal(idleAt) {
		t.Fatalf("after MarkIdle: %+v ok=%v", n, ok)
	}

	// Unknown ids report false and mutate nothing.
	if p.MarkBusy("ghost", "h2", busyAt) {
		t.Fatal("MarkBusy on an unknown node should report false")
	}
	if p.MarkIdle("ghost", idleAt) {
		t.Fatal("MarkIdle on an unknown node should report false")
	}
}
