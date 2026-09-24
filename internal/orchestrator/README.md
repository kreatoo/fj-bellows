# internal/orchestrator

The always-on daemon core: the poll/reconcile loop, the node state machine, the
billing-model-aware teardown policy, and the SSH job dispatcher.

## Node state machine (`state.go`)

```
Provisioning -> Idle -> Busy -> Idle -> Draining -> Removing
```

- **Provisioning** — VM created, awaiting SSH readiness.
- **Idle** — ready and warm, no job assigned.
- **Busy** — a `one-job` run is in flight.
- **Draining / Removing** — teardown decided / `Destroy` issued.

`Pool` is the concurrency-safe node set. Stored nodes are copied in/out so
callers can't mutate shared state.

## Reconcile (`orchestrator.go`)

Each tick, under the single reconcile goroutine:

1. `provider.List(tag)` — ground truth. Adopt unknown instances (crash
   recovery, rebuilding billing timers from `CreatedAt`); drop vanished ones
   (Provisioning nodes are never dropped — a fresh VM may not be listed yet).
   Unknown-instance adoption waits while a create is pending: providers can
   list a VM before `Provision` returns its ID. Readiness completion only
   transitions a still-Provisioning node, never a Busy or Removing worker.
2. `WaitingJobs` — filter to jobs whose required labels this pool offers.
3. Dispatch each serviceable job to an Idle node; provision for the rest, capped
   by `MaxScale` (in-flight provisions count as `pending` so concurrent ticks
   don't over-provision).
4. Apply teardown to Idle nodes.
5. Reap zombie runners: `ListRunners` and delete any registration whose name
   carries our tag prefix but that we aren't currently running a job for (a VM
   that died after registering but before `one-job` finished). Deletion requires
   the runner to look orphaned for two consecutive ticks, closing the race
   against a freshly registered runner. This is the third reconcile source
   (Forgejo jobs + Forgejo runners + provider instances).

The reconcile loop is the **single writer of provisioning decisions**;
dispatch/teardown goroutines mutate only their own node's state.

## Teardown (`teardown.go`)

- `BillingPerSecond` — tear down after `IdleTimeout`.
- `BillingHourlyRoundUp` — tear down at the kill mark
  `created + (completedCycles+1)*BillingHour - HourMargin` (the `:55` rule with
  the 1h/5m defaults). Both `BillingHour` and `HourMargin` are configurable:
  defaults match the provider's actual hourly rounding, but operators can
  shorten `BillingHour` to reclaim idle VMs faster than the paid-hour boundary
  (the cloud still bills the whole hour — you trade the fill-the-paid-hour
  benefit for faster reclamation). E2E tests set them to seconds (e.g.
  `BillingHour=60s, HourMargin=10s` → kill at `created+50s`) to exercise idle
  teardown live without waiting an hour. Timers are **derived from `CreatedAt`
  each tick**, not stored, so they survive restarts.
- **Stale-busy safety net** -- `MaxJobRuntime` (config `poll.max_job_runtime`,
  default 6h) caps how long a node may stay Busy. A busy node past the cap has
  a wedged dispatch goroutine and can never return to Idle on its own; it
  would bill per-second forever while holding a scale.max slot. Such nodes are
  force-destroyed on the next tick. The dispatch start is stamped per job
  (`Node.BusySince`, not persisted -- adoption re-adds nodes as Idle), so the
  cap measures the job, not the idle gap before it. Zero or negative disables
  the net.

## Shutdown

Jobs run under a context independent of the shutdown signal, so `Run` can choose
how to stop. With `DrainOnShutdown` (default) it stops scheduling and waits for
in-flight goroutines to finish (bounded by `DrainTimeout`, `0` = wait
indefinitely); otherwise it cancels the job context to interrupt them. With
`DestroyOnExit` it tears down all owned VMs on the way out — default leaves warm
VMs for a restarted daemon to readopt. In-flight goroutines are tracked with a
`WaitGroup` (`wg.Go`).

## Dispatch (`dispatch.go`)

`Dispatcher` is an interface so the orchestrator is unit-testable without real
SSH **and so the dispatch mechanism can be selected per provider**. Its methods
take both the worker's provider `id` and its network `addr`: SSH dispatch uses
`addr`, while a mechanism like docker exec uses `id`. `SSHDispatcher` connects
in-process with `golang.org/x/crypto/ssh`, writes the one-shot token via stdin
(never the command line), and runs `forgejo-runner one-job ... --wait` to
completion. The composition root (`cmd/fj-bellows`) injects the dispatcher, so a
provider whose workers aren't reached over SSH supplies a different one.

Every dispatch connection runs an **SSH keepalive watchdog** (`keepalive.go`):
a want-reply `keepalive@openssh.com` request every `KeepAliveInterval` (default
15s), and the connection is closed when a probe goes unanswered for
`KeepAliveTimeout` (default 15s). Without it, a connection that dies without a
TCP reset (kernel hang, silent network drop) blocks the dispatch goroutine's
session read forever -- the wedged-Busy scenario the stale-busy net above
backstops. A negative `KeepAliveInterval` disables the watchdog.

Host keys are verified with **trust-on-first-use (TOFU) per-VM pinning**: fresh
per-hour VMs have no pre-known host key, so the first successful handshake to an
address records the presented key in an in-dispatcher pin store, and every later
dial to that address must present a byte-equal key (a mismatch is rejected as a
possible MITM). Different addresses are pinned independently. Residual risk: a
MITM present at the very first contact could still impersonate the VM; after
that, its identity is verified for the rest of its life.

### Worker → Forgejo path

`RunJob` opens a **reverse port-forward** on the dispatch SSH session: the
worker binds `127.0.0.1:<forgejo-port>`, and each accepted connection is relayed
to the orchestrator-side Forgejo using the orchestrator's own resolver. The
dispatch command prepends a `/etc/hosts` override (`127.0.0.1 <forgejo-host>`)
so the runner's lookup of the Forgejo hostname inside the worker lands on the
tunnel. TLS SNI sees the original hostname unchanged, so a public-CA cert
(e.g. via DNS-01) continues to validate end-to-end. This lets workers on a
public cloud reach a LAN-internal Forgejo whose hostname does not resolve from
the public internet — with no worker-side network configuration. The worker-side
hosts override is skipped for IP literals and for `localhost`. See #33.

The runner process is only half the story: each workflow step runs in a
spawned docker job container with its own network namespace and `/etc/hosts`.
`RunJob` also writes a forgejo-runner config (`runnerConfigYAML`) and passes
it via `--config /tmp/runner-cfg.yml`:

```yaml
container:
  docker_host: automount                              # always
  network: host                                       # hostname URLs only
  options: "--add-host=<forgejo-host>:127.0.0.1"      # hostname URLs only
```

- **`docker_host: automount`** bind-mounts `/var/run/docker.sock` into every
  spawned job container. Without it, forgejo-runner's default (`"-"`) finds a
  docker host for the runner process but does not mount the socket into job
  containers, so any `docker …` step fails with `no such file or directory`.
  This is also what makes `actions/setup-buildx`, image builds, and the rest
  of the DinD-shaped tooling work. The worker is single-tenant ephemeral and
  the job is treated as trusted (operator's own repos), matching every other
  CI runner stack. See #41.
- **`network: host`** makes the container's loopback the worker's loopback
  (where the tunnel listener sits); **`--add-host`** injects the hosts entry
  into every spawned container so the Forgejo hostname resolves to
  `127.0.0.1` there too. Without these, `actions/checkout` (and any other
  step that talks back to Forgejo) NXDOMAINs from inside a containerized step
  even when the runner process is on the tunnel. These two are skipped for
  IP-literal Forgejo URLs — hosts files can't redirect IPs to IPs; prefer a
  hostname. See #37.

Dependencies are interfaces (`JobSource`, `Dispatcher`, `provider.Provider`);
see [`mock`](mock) for the test doubles.

## Bounded task acquisition

Dispatch uses `forgejo-runner one-job` **without `--wait`**, with
`runner.fetch_timeout: 2m` in the staged runner config. This applies to SSH,
cache-gateway, and Docker dispatch. An empty or failed fetch exits with status
2 and follows the existing dispatch-error cleanup path. A later reconcile can
retry a job that is still queued. An empty response can return before two
minutes; this is one bounded fetch, not a two-minute retry loop.

This follows the pinned runner **v12.10.1** implementation:
[`internal/app/poll/single.go`](https://code.forgejo.org/forgejo/runner/src/tag/v12.10.1/internal/app/poll/single.go)
creates a timeout context only for `FetchSingleTask` / `FetchTask`, then runs
an acquired task with a separate task context. `--wait` retries empty and failed
fetches indefinitely, so setting `fetch_timeout` while retaining `--wait` does
not bound acquisition. There is no acquisition-timeout CLI flag in this version.
[`internal/app/cmd/cmd.go`](https://code.forgejo.org/forgejo/runner/src/tag/v12.10.1/internal/app/cmd/cmd.go)
maps no-task errors to exit status 2.

The two-minute bound covers the task-fetch RPC, not runner startup or readiness.
It does **not** cap job execution. The runner's normal execution timeout and
`poll.max_job_runtime` (default 6h) remain separate safety limits. Do not wrap
the whole runner command in a short shell or context timeout: that would kill
legitimate jobs. Host-key checks, token delivery over stdin, and transport
configuration are unchanged. The acquisition bound is currently fixed, not a
hot-reloadable operator setting.
