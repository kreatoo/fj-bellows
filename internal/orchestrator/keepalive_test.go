package orchestrator

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// The keepalive tests run a minimal in-process SSH server against the real
// dispatcher dial path. Two failure modes matter:
//
//   - dead-but-open ("black hole"): the server accepts everything but never
//     replies to global requests and never terminates the exec — exactly the
//     wedged-worker incident this watchdog exists for;
//   - healthy: the server replies to keepalives and execs normally.
type exitStatusMsg struct{ Status uint32 }

// testSSHServer is a hermetic loopback SSH server for dispatcher tests.
type testSSHServer struct {
	t           *testing.T
	listener    net.Listener
	config      *ssh.ServerConfig
	replyGlobal bool // reply to want-reply global requests (keepalives)
	stallExec   bool // accept exec but never send exit-status

	globalReqs   atomic.Int32
	keepaliveReq atomic.Int32

	mu    sync.Mutex
	conns []net.Conn
}

func newTestSSHServer(t *testing.T, replyGlobal, stallExec bool) *testSSHServer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	hostKey, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("ssh.NewSignerFromKey: %v", err)
	}
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(hostKey)
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &testSSHServer{
		t:           t,
		listener:    ln,
		config:      config,
		replyGlobal: replyGlobal,
		stallExec:   stallExec,
	}
	go s.serve()
	t.Cleanup(s.close)
	return s
}

func (s *testSSHServer) port() int {
	_, portStr, _ := net.SplitHostPort(s.listener.Addr().String())
	port, _ := strconv.Atoi(portStr)
	return port
}

func (s *testSSHServer) close() {
	_ = s.listener.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		_ = c.Close()
	}
}

func (s *testSSHServer) track(c net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conns = append(s.conns, c)
}

func (s *testSSHServer) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.track(conn)
		go s.handleConn(conn)
	}
}

func (s *testSSHServer) handleConn(conn net.Conn) {
	sconn, chans, reqs, err := ssh.NewServerConn(conn, s.config)
	if err != nil {
		_ = conn.Close()
		return
	}
	defer func() { _ = sconn.Close() }()
	go s.handleReqs(reqs)
	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			_ = newCh.Reject(ssh.UnknownChannelType, "unsupported")
			continue
		}
		ch, chReqs, err := newCh.Accept()
		if err != nil {
			continue
		}
		go s.handleSession(ch, chReqs)
	}
}

// handleReqs drains global requests. When replyGlobal is false it silently
// drops want-reply requests — the black-hole behavior the keepalive
// watchdog must detect.
func (s *testSSHServer) handleReqs(reqs <-chan *ssh.Request) {
	for req := range reqs {
		if req.Type == "keepalive@openssh.com" {
			s.keepaliveReq.Add(1)
		}
		if req.WantReply && s.replyGlobal {
			_ = req.Reply(true, nil)
		}
		s.globalReqs.Add(1)
	}
}

func (s *testSSHServer) handleSession(ch ssh.Channel, chReqs <-chan *ssh.Request) {
	defer func() { _ = ch.Close() }()
	for req := range chReqs {
		if req.Type != "exec" {
			if req.WantReply {
				_ = req.Reply(true, nil)
			}
			continue
		}
		_ = req.Reply(true, nil)
		if s.stallExec {
			// Accepted but never finishes: CombinedOutput blocks forever
			// unless the keepalive watchdog closes the connection.
			continue
		}
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(exitStatusMsg{Status: 0}))
		return
	}
}

// testSSHUser is the user every test dispatcher dials with; a constant so
// the repeated literal doesn't trip goconst.
const testSSHUser = "root"

func testClientSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate client key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("ssh.NewSignerFromKey: %v", err)
	}
	return signer
}

// probeBlackHole asserts that the keepalive watchdog unblocks a session
// stuck on a black-holed server: runRemote must fail within the keepalive
// deadline instead of blocking forever. Shared by both dispatchers' black-
// hole tests.
func probeBlackHole(t *testing.T, client *ssh.Client) {
	t.Helper()
	errCh := make(chan error, 1)
	go func() {
		errCh <- runRemote(context.Background(), client, "true", nil)
	}()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("runRemote must fail after the watchdog closes a black-holed connection")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runRemote still blocked after keepalive deadline; watchdog did not fire")
	}
}

// TestKeepaliveDetectsBlackHole is the regression test for the wedged-worker
// incident: a dispatch connection whose peer vanishes without a TCP reset
// must be closed by the watchdog, unblocking the stuck session read.
func TestKeepaliveDetectsBlackHole(t *testing.T) {
	// 80ms interval + 80ms timeout: worst-case detection well under a second,
	// fast enough that the test cannot hang the suite.
	server := newTestSSHServer(t, false, true)
	d := &SSHDispatcher{
		User:              testSSHUser,
		Port:              server.port(),
		Signer:            testClientSigner(t),
		DialTimeout:       2 * time.Second,
		KeepAliveInterval: 80 * time.Millisecond,
		KeepAliveTimeout:  80 * time.Millisecond,
	}
	client, err := d.dial(context.Background(), "127.0.0.1")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = client.Close() }()

	probeBlackHole(t, client)
}

// TestKeepaliveHealthyConnectionSurvives pins the other side of the
// watchdog: a connection whose peer answers keepalives is never closed, and
// probes actually flow.
func TestKeepaliveHealthyConnectionSurvives(t *testing.T) {
	server := newTestSSHServer(t, true, false)
	d := &SSHDispatcher{
		User:              testSSHUser,
		Port:              server.port(),
		Signer:            testClientSigner(t),
		DialTimeout:       2 * time.Second,
		KeepAliveInterval: 50 * time.Millisecond,
		KeepAliveTimeout:  50 * time.Millisecond,
	}
	client, err := d.dial(context.Background(), "127.0.0.1")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = client.Close() }()

	// Long enough for several probe intervals on a healthy connection.
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		if err := runRemote(context.Background(), client, "true", nil); err != nil {
			t.Fatalf("runRemote on healthy connection: %v", err)
		}
		time.Sleep(30 * time.Millisecond)
	}
	if got := server.keepaliveReq.Load(); got < 2 {
		t.Fatalf("server saw %d keepalive probes over ~400ms at a 50ms interval; want >= 2", got)
	}
}

// TestKeepaliveDisabled pins the opt-out: a negative interval starts no
// watchdog (no keepalive probes reach the server).
func TestKeepaliveDisabled(t *testing.T) {
	server := newTestSSHServer(t, true, false)
	d := &SSHDispatcher{
		User:              testSSHUser,
		Port:              server.port(),
		Signer:            testClientSigner(t),
		DialTimeout:       2 * time.Second,
		KeepAliveInterval: -1,
	}
	client, err := d.dial(context.Background(), "127.0.0.1")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = client.Close() }()

	if err := runRemote(context.Background(), client, "true", nil); err != nil {
		t.Fatalf("runRemote: %v", err)
	}
	time.Sleep(120 * time.Millisecond) // several default-ish intervals would have fired
	if got := server.globalReqs.Load(); got != 0 {
		t.Fatalf("disabled keepalive still sent %d global requests", got)
	}
}

// TestCacheGatewayKeepaliveDetectsBlackHole covers the second dispatcher: the
// watchdog must work identically on the cache-gateway dial path.
func TestCacheGatewayKeepaliveDetectsBlackHole(t *testing.T) {
	server := newTestSSHServer(t, false, true)
	d := &CacheGatewayDispatcher{
		User:              testSSHUser,
		Port:              server.port(),
		Signer:            testClientSigner(t),
		DialTimeout:       2 * time.Second,
		KeepAliveInterval: 80 * time.Millisecond,
		KeepAliveTimeout:  80 * time.Millisecond,
	}
	client, err := d.dial(context.Background(), "127.0.0.1")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = client.Close() }()

	probeBlackHole(t, client)
}

// TestKeepaliveIntervalOrDefault pins the zero/negative resolution.
func TestKeepaliveIntervalOrDefault(t *testing.T) {
	if got := keepaliveIntervalOrDefault(0); got != DefaultKeepAliveInterval {
		t.Errorf("zero interval = %s, want default %s", got, DefaultKeepAliveInterval)
	}
	if got := keepaliveIntervalOrDefault(-time.Second); got != -time.Second {
		t.Errorf("negative interval = %s, want passthrough (disabled)", got)
	}
	if got := keepaliveIntervalOrDefault(45 * time.Second); got != 45*time.Second {
		t.Errorf("explicit interval = %s, want passthrough", got)
	}
}
