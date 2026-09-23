package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hstern/fj-bellows/internal/forgejo"
	omock "github.com/hstern/fj-bellows/internal/orchestrator/mock"
	"github.com/hstern/fj-bellows/internal/provider"
	pmock "github.com/hstern/fj-bellows/internal/provider/mock"
)

// A provider exposes the VM before returning from Provision (e.g. while
// waiting for its network). Neither adoption nor dispatch may bypass readiness.
func TestProvisionVisibleBeforeReturn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	visible := make(chan struct{})
	returnProvision := make(chan struct{})
	readyStarted := make(chan struct{})
	ready := make(chan struct{})
	inst := provider.Instance{ID: "creating", IPv4: testIP, CreatedAt: time.Now()}
	prov := &pmock.Provider{
		ProvisionFn: func(ctx context.Context, _ provider.Spec) (provider.Instance, error) {
			close(visible)
			select {
			case <-returnProvision:
				return inst, nil
			case <-ctx.Done():
				return provider.Instance{}, ctx.Err()
			}
		},
		ListFn: func(context.Context, string) ([]provider.Instance, error) {
			select {
			case <-visible:
				return []provider.Instance{inst}, nil
			default:
				return nil, nil
			}
		},
	}
	jobs := &omock.JobSource{WaitingJobsFn: func(context.Context) ([]forgejo.WaitingJob, error) { return nUbuntuJobs(1), nil }}
	disp := &omock.Dispatcher{
		WaitReadyFn: func(ctx context.Context, _, _ string) error {
			close(readyStarted)
			select {
			case <-ready:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
		RunJobFn: func(ctx context.Context, _, _ string, _ forgejo.Registration, _ forgejo.WaitingJob) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}
	o := New(baseConfig(), prov, jobs, disp, nil)
	t.Cleanup(func() { cancel(); o.wg.Wait() })
	o.Reconcile(ctx)
	waitFor(t, "provider exposes VM", func() bool {
		select {
		case <-visible:
			return true
		default:
			return false
		}
	})
	for range 3 {
		result := o.Reconcile(ctx)
		if result.Adopted != 0 || result.Dispatched != 0 || result.Provisioned != 0 || o.pool.Len() != 0 || o.PendingProvisions() != 1 {
			t.Fatalf("in-flight instance adopted or capacity miscounted: %+v", result)
		}
	}
	close(returnProvision)
	waitFor(t, "readiness starts", func() bool {
		select {
		case <-readyStarted:
			return true
		default:
			return false
		}
	})
	result := o.Reconcile(ctx)
	if result.Adopted != 0 || result.Dispatched != 0 || result.Provisioned != 0 {
		t.Fatalf("booting VM dispatched: %+v", result)
	}
	close(ready)
	waitFor(t, "ready", func() bool { return len(o.pool.ByState(StateIdle)) == 1 })
	if result := o.Reconcile(ctx); result.Dispatched != 1 {
		t.Fatalf("ready VM not dispatched: %+v", result)
	}
	if o.applyTeardown(ctx) != 0 || prov.DestroyCount() != 0 {
		t.Fatal("busy worker reaped")
	}
	if prov.ProvisionCount() != 1 {
		t.Fatal("extra VM provisioned")
	}
}

// Late readiness results (both success and failure, both create paths) must
// not revive removal or overwrite a busy worker and expose it to teardown.
func TestReadinessDoesNotOverwriteNewerState(t *testing.T) {
	for _, force := range []bool{false, true} {
		for _, state := range []NodeState{StateBusy, StateRemoving} {
			for _, failed := range []bool{false, true} {
				name := string(state)
				if force {
					name += "/force"
				} else {
					name += "/normal"
				}
				if failed {
					name += "/failure"
				} else {
					name += "/success"
				}
				t.Run(name, func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					started, finish := make(chan struct{}), make(chan struct{})
					prov := &pmock.Provider{ProvisionFn: func(context.Context, provider.Spec) (provider.Instance, error) {
						return provider.Instance{ID: "vm", IPv4: testIP}, nil
					}}
					disp := &omock.Dispatcher{WaitReadyFn: func(ctx context.Context, _, _ string) error {
						close(started)
						select {
						case <-finish:
						case <-ctx.Done():
							return ctx.Err()
						}
						if failed {
							return errors.New("readiness failed")
						}
						return nil
					}}
					o := New(baseConfig(), prov, &omock.JobSource{}, disp, nil)
					t.Cleanup(func() { cancel(); o.wg.Wait() })
					if force {
						if r := o.doForceProvision(ctx); r.err != nil {
							t.Fatal(r.err)
						}
					} else {
						o.provisionOne(ctx)
					}
					waitFor(t, "readiness starts", func() bool {
						select {
						case <-started:
							return true
						default:
							return false
						}
					})
					o.pool.SetState("vm", state)
					close(finish)
					o.wg.Wait()
					n, ok := o.pool.Get("vm")
					if !ok || n.State != state {
						t.Fatalf("late readiness changed worker: %+v, exists=%v", n, ok)
					}
					if prov.DestroyCount() != 0 {
						t.Fatal("late readiness destroyed worker")
					}
				})
			}
		}
	}
}

func TestAdoptionResumesAfterPendingCreate(t *testing.T) {
	o := New(baseConfig(), &pmock.Provider{}, &omock.JobSource{}, &omock.Dispatcher{}, nil)
	instances := []provider.Instance{{ID: "orphan", IPv4: testIP}}
	o.incPending()
	if adopted, _ := o.syncPool(instances); adopted != 0 {
		t.Fatal("adopted while create pending")
	}
	o.decPending()
	if adopted, _ := o.syncPool(instances); adopted != 1 {
		t.Fatal("orphan adoption did not resume")
	}
}

func TestPoolConditionalTransitions(t *testing.T) {
	for _, state := range []NodeState{StateProvisioning, StateIdle, StateBusy, StateDraining, StateRemoving} {
		t.Run(string(state), func(t *testing.T) {
			p := NewPool()
			p.Put(&Node{InstanceID: "vm", State: state})
			if got := p.MarkBusy("vm", "job", time.Now()); got != (state == StateIdle) {
				t.Fatalf("MarkBusy = %v", got)
			}
			p.Put(&Node{InstanceID: "vm", State: state})
			if got := p.CompareAndSwapState("vm", StateProvisioning, StateIdle); got != (state == StateProvisioning) {
				t.Fatalf("readiness transition = %v", got)
			}
		})
	}
	if NewPool().CompareAndSwapState("missing", StateProvisioning, StateIdle) {
		t.Fatal("transitioned missing node")
	}
}
