// Package drills holds the integration tests behind docs/drills.
//
// Each test drives the same code path a reader drives with docker compose and
// curl: a real exchange acceptor, real initiator sessions, real FIX over a real
// TCP socket on loopback. Only two things differ from the compose stack — the
// message store is in memory, and the port is chosen at runtime.
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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quickfixgo/quickfix"

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

// lab is a running stack: one exchange with both acceptor sessions, one
// order-entry client, one drop-copy client.
type lab struct {
	Exchange *exchange.App
	Book     *exchange.Book
	OE       *client.OrderEntry
	DC       *client.DropCopy

	OESessionID quickfix.SessionID
	DCSessionID quickfix.SessionID

	// Wire captures every byte both sides logged, redacted, for golden-trace
	// assertions.
	Wire *wireCapture

	acceptor  *quickfix.Acceptor
	initiator []*quickfix.Initiator
}

// startLab brings up the whole stack and waits for both sessions to log on.
func startLab(t *testing.T) *lab {
	t.Helper()

	port := freePort(t)
	wire := &wireCapture{}
	logger := log.New(newTestWriter(t), "", 0)

	book := exchange.NewBook()
	app := exchange.New(book, logger)

	exSettings := parseSettings(t, exchangeSettings(port))
	if err := app.LoadKinds(exSettings); err != nil {
		t.Fatalf("load session kinds: %v", err)
	}

	acceptor, err := quickfix.NewAcceptor(
		app, quickfix.NewMemoryStoreFactory(), exSettings, fixlog.NewFactory(wire))
	if err != nil {
		t.Fatalf("new acceptor: %v", err)
	}
	if err := acceptor.Start(); err != nil {
		t.Fatalf("start acceptor: %v", err)
	}

	l := &lab{Exchange: app, Book: book, Wire: wire, acceptor: acceptor}
	t.Cleanup(l.stop)

	// The wire traces printed in docs/drills come from here. Run with
	// FIXLAB_DUMP_WIRE=1 to regenerate one after changing behavior.
	t.Cleanup(func() {
		if os.Getenv("FIXLAB_DUMP_WIRE") != "" {
			t.Logf("\n--- wire ---\n%s", l.Wire.String())
		}
	})

	oeSettings := parseSettings(t, clientSettings(port, oeCompID))
	l.OE = client.NewOrderEntry(session.LogonCredentials{AppID: "fixlab-oe/test"}, logger)
	l.OESessionID = l.startInitiator(t, l.OE, oeSettings, wire)

	dcSettings := parseSettings(t, clientSettings(port, dcCompID))
	l.DC = client.NewDropCopy(session.LogonCredentials{AppID: "fixlab-dc/test"}, logger)
	l.DCSessionID = l.startInitiator(t, l.DC, dcSettings, wire)

	l.waitLoggedOn(t)
	return l
}

func (l *lab) startInitiator(t *testing.T, app quickfix.Application,
	settings *quickfix.Settings, wire *wireCapture) quickfix.SessionID {
	t.Helper()

	init, err := quickfix.NewInitiator(app, quickfix.NewMemoryStoreFactory(), settings, fixlog.NewFactory(wire))
	if err != nil {
		t.Fatalf("new initiator: %v", err)
	}
	if err := init.Start(); err != nil {
		t.Fatalf("start initiator: %v", err)
	}
	l.initiator = append(l.initiator, init)

	sessionID, err := session.SoleSession(settings)
	if err != nil {
		t.Fatalf("sole session: %v", err)
	}
	return sessionID
}

func (l *lab) stop() {
	for _, i := range l.initiator {
		i.Stop()
	}
	if l.acceptor != nil {
		l.acceptor.Stop()
	}
}

// waitLoggedOn blocks until both client sessions report a completed Logon.
func (l *lab) waitLoggedOn(t *testing.T) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if l.OE.LoggedOn(l.OESessionID) && l.DC.LoggedOn(l.DCSessionID) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Dump the wire. A failed handshake is almost always legible from the
	// Logon and the Logout text the venue replied with.
	t.Fatalf("sessions did not log on within 5s; wire follows:\n%s", l.Wire.String())
}

// waitFor polls cond until it is true or the deadline passes.
//
// Polling rather than a fixed sleep keeps the tests fast when things go well
// and gives a useful failure message when they do not.
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

// freePort asks the kernel for an unused port. Hard-coding one would make the
// suite fail whenever a drill's compose stack happens to be running.
func freePort(t *testing.T) int {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func exchangeSettings(port int) string {
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

[session]
SenderCompID=%s
TargetCompID=%s
SessionKind=ORDER_ENTRY

[session]
SenderCompID=%s
TargetCompID=%s
SessionKind=DROP_COPY
`, port, exchangeCompID, oeCompID, exchangeCompID, dcCompID)
}

func clientSettings(port int, compID string) string {
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
ResetOnLogon=Y
UseDataDictionary=Y
DataDictionary=../../spec/FIX44-fixlab.xml
ValidateUserDefinedFields=Y
PersistMessages=Y
LogoutTimeout=1
CheckLatency=N
MaxMessagesInResendRequest=10000

[session]
SenderCompID=%s
TargetCompID=%s
`, port, compID, exchangeCompID)
}

// testWriter routes log output through t.Log so it only surfaces on failure.
type testWriter struct{ t *testing.T }

func newTestWriter(t *testing.T) *testWriter { return &testWriter{t: t} }

func (w *testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// wireCapture accumulates the redacted FIX traffic both sides logged. Three
// sessions write to it concurrently, so it locks.
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
