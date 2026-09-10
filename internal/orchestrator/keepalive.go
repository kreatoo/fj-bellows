package orchestrator

import (
	"net"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Dispatch connections block in session reads for the whole job
// (`forgejo-runner one-job --wait`). A connection that dies without a TCP
// reset — kernel hang, silent network drop, NAT idle eviction — blocks those
// reads forever: the dispatch goroutine wedges, the node stays Busy past its
// reap time, and the VM bills per-second while holding a scale.max slot (the
// 2026-09-10 production wedge). A periodic want-reply keepalive detects the
// black hole and closes the connection, which wakes every stuck session read
// with an error so the normal dispatch failure path can reclaim the node.
const (
	// DefaultKeepAliveInterval is how often a keepalive probe is sent on an
	// established dispatch connection.
	DefaultKeepAliveInterval = 15 * time.Second

	// DefaultKeepAliveTimeout bounds how long a probe waits for its reply
	// before the connection is declared dead. Kept equal to the interval so
	// worst-case detection is interval + timeout (30s with the defaults) —
	// far below any real job length, fast enough to stop the billing bleed.
	DefaultKeepAliveTimeout = 15 * time.Second
)

// keepaliveIntervalOrDefault resolves the effective keepalive interval.
// Zero selects the default so a zero-value dispatcher is still protected; a
// negative value explicitly disables the watchdog.
func keepaliveIntervalOrDefault(v time.Duration) time.Duration {
	if v == 0 {
		return DefaultKeepAliveInterval
	}
	return v // may be negative: disabled
}

// keepaliveConn wraps the dispatch connection so the keepalive goroutine can
// observe closure — by the watchdog itself, or by client.Close() from any
// owner (RunJob's defer, WaitReady's probe loop, tests) — and exit promptly
// instead of lingering for one interval after the connection is gone.
type keepaliveConn struct {
	net.Conn
	once sync.Once
	done chan struct{}
}

func newKeepaliveConn(c net.Conn) *keepaliveConn {
	return &keepaliveConn{Conn: c, done: make(chan struct{})}
}

// Close closes the wrapped connection and releases the keepalive goroutine.
func (c *keepaliveConn) Close() error {
	c.once.Do(func() { close(c.done) })
	return c.Conn.Close()
}

// startKeepalive runs the connection watchdog: every interval it sends a
// want-reply "keepalive@openssh.com" request; when the reply does not arrive
// within timeout (or the request errors outright), the connection is closed
// so goroutines blocked on session reads wake up with an error. Returns
// immediately; the goroutine exits when the connection closes for any
// reason. interval <= 0 disables the watchdog (no goroutine).
func startKeepalive(conn *keepaliveConn, client *ssh.Client, interval, timeout time.Duration) {
	if interval <= 0 {
		return
	}
	if timeout <= 0 {
		timeout = interval
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-conn.done:
				return
			case <-ticker.C:
			}
			// SendRequest blocks until the reply (or a transport error), so
			// it runs on its own goroutine; the buffered channel guarantees
			// that goroutine never leaks even when the watchdog gives up on
			// it — closing the conn unblocks the request.
			reply := make(chan error, 1)
			go func() {
				_, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
				reply <- err
			}()
			select {
			case err := <-reply:
				if err != nil {
					_ = conn.Close() // transport dead: unblock stuck sessions
					return
				}
			case <-time.After(timeout):
				_ = conn.Close() // black hole: nobody will ever answer
				return
			case <-conn.done:
				return
			}
		}
	}()
}
