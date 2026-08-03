// Package drills holds the integration tests behind docs/drills.
//
// Each test drives the same code path a reader drives with docker compose and
// curl: a real exchange acceptor, real initiator sessions, real FIX over a real
// TCP socket on loopback. Only two things differ from the compose stack — the
// port is chosen at runtime, and drills that do not restart a process use an
// in-memory store.
//
// The tests do not run in parallel. quickfixgo keeps its session registry in a
// package-level map keyed by SessionID, so two tests using the same CompIDs at
// the same time would collide.
package drills

import (
	"bytes"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quickfixgo/quickfix"
	filestore "github.com/quickfixgo/quickfix/store/file"

	"github.com/sylvioCampos/fix-session-lab/internal/client"
	"github.com/sylvioCampos/fix-session-lab/internal/exchange"
	"github.com/sylvioCampos/fix-session-lab/internal/fixlog"
	"github.com/sylvioCampos/fix-session-lab/internal/session"
)

const (
	exchangeCompID = "FIXLABEX"
	oeCompID       = "OECLIENT"
	dcCompID       = "DCCLIENT"
)

// config controls how a lab is brought up.
type config struct {
	// storeDir, when set, gives every session a file store rooted there.
	// Required by any drill that stops and restarts a process: a memory store
	// is created fresh per session, so a restart would silently reset the
	// sequence numbers and erase the very gap the drill is about.
	storeDir string

	// resetOnLogon is the clients' ResetOnLogon setting. Y is right for a
	// first connect and wrong for recovery — see drill 06.
	resetOnLogon string

	// withDropCopy starts the drop-copy client. Drills that only exercise the
	// order-entry session leave it off to keep the wire trace readable.
	withDropCopy bool
}

type option func(*config)

func withFileStore(dir string) option  { return func(c *config) { c.storeDir = dir } }
func withResetOnLogon(v string) option { return func(c *config) { c.resetOnLogon = v } }
func withoutDropCopy() option          { return func(c *config) { c.withDropCopy = false } }

// lab is a running stack: one exchange with both acceptor sessions, an
// order-entry client, and optionally a drop-copy client.
type lab struct {
	Exchange *exchange.App
	Book     *exchange.Book
	OE       *client.OrderEntry
	DC       *client.DropCopy

	OESessionID quickfix.SessionID
	DCSessionID quickfix.SessionID

	// Wire captures every byte every session logged, redacted.
	Wire *wireCapture

	cfg      config
	port     int
	logger   *log.Logger
	acceptor *quickfix.Acceptor

	oeInitiator *quickfix.Initiator
	dcInitiator *quickfix.Initiator
}

func startLab(t *testing.T, opts ...option) *lab {
	t.Helper()

	cfg := config{resetOnLogon: "Y", withDropCopy: true}
	for _, o := range opts {
		o(&cfg)
	}

	l := &lab{
		cfg:    cfg,
		port:   freePort(t),
		Wire:   &wireCapture{},
		logger: log.New(newTestWriter(t), "", 0),
	}

	l.Book = exchange.NewBook()
	l.Exchange = exchange.New(l.Book, l.logger)

	exSettings := parseSettings(t, l.exchangeSettings())
	if err := l.Exchange.LoadKinds(exSettings); err != nil {
		t.Fatalf("load session kinds: %v", err)
	}

	acceptor, err := quickfix.NewAcceptor(
		l.Exchange, l.storeFactory(exSettings), exSettings, fixlog.NewFactory(l.Wire))
	if err != nil {
		t.Fatalf("new acceptor: %v", err)
	}
	if err := acceptor.Start(); err != nil {
		t.Fatalf("start acceptor: %v", err)
	}
	l.acceptor = acceptor

	t.Cleanup(l.stop)
	t.Cleanup(func() {
		// The wire traces printed in docs/drills come from here. Run with
		// FIXLAB_DUMP_WIRE=1 to regenerate one after changing behavior.
		if os.Getenv("FIXLAB_DUMP_WIRE") != "" {
			t.Logf("\n--- wire ---\n%s", l.Wire.String())
		}
	})

	l.StartOE(t)
	if cfg.withDropCopy {
		l.startDC(t)
	}

	l.WaitLoggedOn(t)
	return l
}

// storeFactory picks a message store. File-backed when the drill restarts a
// process, in memory otherwise.
func (l *lab) storeFactory(settings *quickfix.Settings) quickfix.MessageStoreFactory {
	if l.cfg.storeDir == "" {
		return quickfix.NewMemoryStoreFactory()
	}
	return filestore.NewStoreFactory(settings)
}

// StartOE brings up the order-entry client. Calling it after StopOE is how a
// drill reconnects, and it is the only way to do so: quickfixgo has no
// per-session disconnect, and Initiator.Stop() unregisters the sessions it
// owns, so Start() on the same Initiator would leave them unroutable. A
// reconnect means a brand new Initiator over the same store.
func (l *lab) StartOE(t *testing.T) {
	t.Helper()

	settings := parseSettings(t, l.clientSettings(oeCompID))

	// A fresh application on reconnect, deliberately. A real client restarting
	// its process loses in-memory order state and rebuilds it from what the
	// venue replays — which is exactly what drill 07 is checking.
	l.OE = client.NewOrderEntry(session.LogonCredentials{AppID: "fixlab-oe/test"}, l.logger)

	init, err := quickfix.NewInitiator(
		l.OE, l.storeFactory(settings), settings, fixlog.NewFactory(l.Wire))
	if err != nil {
		t.Fatalf("new order-entry initiator: %v", err)
	}
	if err := init.Start(); err != nil {
		t.Fatalf("start order-entry initiator: %v", err)
	}
	l.oeInitiator = init

	l.OESessionID = soleSession(t, settings)
}

// StopOE disconnects the order-entry client and waits for the venue to notice.
func (l *lab) StopOE(t *testing.T) {
	t.Helper()

	if l.oeInitiator == nil {
		return
	}
	l.oeInitiator.Stop()
	l.oeInitiator = nil

	waitFor(t, 5*time.Second, "the venue to see the order-entry session drop", func() bool {
		for _, s := range l.Exchange.Sessions() {
			if s.Kind == exchange.KindOrderEntry {
				return !s.LoggedOn
			}
		}
		return false
	})
}

func (l *lab) startDC(t *testing.T) {
	t.Helper()

	settings := parseSettings(t, l.clientSettings(dcCompID))
	l.DC = client.NewDropCopy(session.LogonCredentials{AppID: "fixlab-dc/test"}, l.logger)

	init, err := quickfix.NewInitiator(
		l.DC, l.storeFactory(settings), settings, fixlog.NewFactory(l.Wire))
	if err != nil {
		t.Fatalf("new drop-copy initiator: %v", err)
	}
	if err := init.Start(); err != nil {
		t.Fatalf("start drop-copy initiator: %v", err)
	}
	l.dcInitiator = init

	l.DCSessionID = soleSession(t, settings)
}

func (l *lab) stop() {
	if l.oeInitiator != nil {
		l.oeInitiator.Stop()
	}
	if l.dcInitiator != nil {
		l.dcInitiator.Stop()
	}
	if l.acceptor != nil {
		l.acceptor.Stop()
	}
}

// WaitLoggedOn blocks until every started client session reports a Logon.
func (l *lab) WaitLoggedOn(t *testing.T) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		ok := l.OE.LoggedOn(l.OESessionID)
		if l.cfg.withDropCopy {
			ok = ok && l.DC.LoggedOn(l.DCSessionID)
		}
		if ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	// A failed handshake is almost always legible from the Logon and whatever
	// the venue replied with, so dump the wire rather than just the timeout.
	t.Fatalf("sessions did not log on within 10s; wire follows:\n%s", l.Wire.String())
}

// waitFor polls cond until it is true or the deadline passes.
func waitFor(t *testing.T, limit time.Duration, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", limit, what)
}

func parseSettings(t *testing.T, cfg string) *quickfix.Settings {
	t.Helper()

	settings, err := quickfix.ParseSettings(strings.NewReader(cfg))
	if err != nil {
		t.Fatalf("parse settings: %v", err)
	}
	return settings
}

func soleSession(t *testing.T, settings *quickfix.Settings) quickfix.SessionID {
	t.Helper()

	id, err := session.SoleSession(settings)
	if err != nil {
		t.Fatalf("sole session: %v", err)
	}
	return id
}

// freePort asks the kernel for an unused port. Hard-coding one would make the
// suite fail whenever a drill's compose stack happens to be running.
func freePort(t *testing.T) int {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func (l *lab) storeLine(name string) string {
	if l.cfg.storeDir == "" {
		return ""
	}
	return "FileStorePath=" + filepath.Join(l.cfg.storeDir, name) + "\n"
}

func (l *lab) exchangeSettings() string {
	return fmt.Sprintf(`
[default]
ConnectionType=acceptor
BeginString=FIX.4.4
SocketAcceptPort=%d
SocketAcceptHost=127.0.0.1
StartTime=00:00:00
EndTime=00:00:00
NonStopSession=Y
HeartBtInt=30
ResetOnLogon=N
UseDataDictionary=Y
DataDictionary=../../spec/FIX44-fixlab.xml
ValidateUserDefinedFields=Y
PersistMessages=Y
%s
[session]
SenderCompID=%s
TargetCompID=%s
SessionKind=ORDER_ENTRY

[session]
SenderCompID=%s
TargetCompID=%s
SessionKind=DROP_COPY
`, l.port, l.storeLine("exchange"), exchangeCompID, oeCompID, exchangeCompID, dcCompID)
}

func (l *lab) clientSettings(compID string) string {
	return fmt.Sprintf(`
[default]
ConnectionType=initiator
BeginString=FIX.4.4
SocketConnectHost=127.0.0.1
SocketConnectPort=%d
ReconnectInterval=1
HeartBtInt=30
StartTime=00:00:00
EndTime=00:00:00
NonStopSession=Y
ResetOnLogon=%s
UseDataDictionary=Y
DataDictionary=../../spec/FIX44-fixlab.xml
ValidateUserDefinedFields=Y
PersistMessages=Y
LogoutTimeout=1
CheckLatency=N
MaxMessagesInResendRequest=10000
%s
[session]
SenderCompID=%s
TargetCompID=%s
`, l.port, l.cfg.resetOnLogon, l.storeLine(compID), compID, exchangeCompID)
}

// testWriter routes log output through t.Log so it only surfaces on failure.
type testWriter struct{ t *testing.T }

func newTestWriter(t *testing.T) *testWriter { return &testWriter{t: t} }

func (w *testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// wireCapture accumulates the redacted FIX traffic every session logged.
// Several sessions write to it concurrently, so it locks.
type wireCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *wireCapture) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *wireCapture) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}
