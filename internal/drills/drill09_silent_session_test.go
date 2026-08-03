package drills

import (
	"log"
	"testing"
	"time"

	"github.com/quickfixgo/quickfix"
	"github.com/shopspring/decimal"

	"github.com/sylvioCampos/fix-session-lab/internal/session"
)

// Drill 09 — the silent session, and the watchdog that catches it.
//
// The venue stops publishing business data while its session stays logged on
// and its heartbeats keep flowing. Every liveness mechanism FIX has keeps
// passing. The client is receiving nothing it connected for, and nothing below
// the application will ever say so.
//
// Only the application can detect this, because only the application knows what
// "data should have arrived by now" means.
func TestDrill09_WatchdogFiresOnApplicationSilence(t *testing.T) {
	lab := startLab(t,
		withHeartBtInt(1),
		withWatchdog(1500*time.Millisecond),
		withFileStore(t.TempDir()),
		withResetOnLogon("N"),
		withoutDropCopy(),
	)

	// Traffic is flowing: the watchdog is satisfied.
	clOrdID := sendOrder(t, lab, "PETR4", decimal.NewFromInt(100), decimal.NewFromFloat(25.50))
	waitFor(t, 3*time.Second, "the acknowledgement", func() bool {
		_, ok := lab.OE.Order(clOrdID)
		return ok
	})

	lab.Watchdog.Start()
	t.Cleanup(lab.Watchdog.Stop)

	if lab.Watchdog.Fired() != 0 {
		t.Fatalf("watchdog fired %d times before anything went wrong", lab.Watchdog.Fired())
	}

	// The venue goes quiet at the application layer only. Heartbeats continue,
	// the socket stays open, the session stays logged on.
	lab.Exchange.SetSilenced(true)

	waitFor(t, 10*time.Second, "the watchdog to force a reconnect", func() bool {
		return lab.Watchdog.Fired() > 0
	})

	// It recovered by building a new Initiator, which is the only way
	// quickfixgo permits a single session to be recycled.
	//
	// Waited for, not asserted outright: the watchdog increments its fired
	// counter before it calls Restart, so the condition above can be true while
	// the rebuild is still in flight. Reading Starts() immediately is a race —
	// it passed locally a dozen times and on the pull request, then failed on
	// main.
	waitFor(t, 5*time.Second, "the supervisor to build a replacement initiator", func() bool {
		return lab.OESupervisorStarts() >= 2
	})

	// And the session comes back up on its own afterwards.
	lab.Exchange.SetSilenced(false)
	lab.WaitLoggedOn(t)
}

// TestDrill09_WatchdogIgnoresHeartbeats is the assertion that decides whether a
// watchdog is worth having.
//
// If it treated admin traffic as liveness it would never fire, because
// heartbeats never stop — that is their entire job. A watchdog wired to all
// inbound traffic looks correct, tests green, and detects nothing.
func TestDrill09_WatchdogIgnoresHeartbeats(t *testing.T) {
	var appNotices, adminNotices int

	c := session.NewClient(session.LogonCredentials{}, log.New(newTestWriter(t), "", 0))
	c.OnInbound(func(_ quickfix.SessionID, class session.InboundClass) {
		switch class {
		case session.InboundApp:
			appNotices++
		case session.InboundAdmin:
			adminNotices++
		}
	})

	w := &session.Watchdog{Timeout: time.Hour}
	session.WatchClient(c, w)

	before := w.Fired()

	// A flood of admin traffic must not count as liveness.
	for range 10 {
		c.NoteInbound(quickfix.SessionID{}, session.InboundAdmin)
	}
	c.NoteInbound(quickfix.SessionID{}, session.InboundApp)

	if adminNotices != 10 || appNotices != 1 {
		t.Fatalf("callback saw %d admin and %d app notices, want 10 and 1",
			adminNotices, appNotices)
	}
	if w.Fired() != before {
		t.Error("watchdog fired during the test setup")
	}
}

// TestDrill09_InactiveWindowSuppressesFiring covers the second half of the
// real-world version of this bug.
//
// A watchdog that considers every quiet moment suspicious will tear down
// healthy sessions during legitimate quiet — before the market opens, over a
// lunch break, on an instrument that simply is not trading. Getting the window
// boundary wrong by an hour turns the watchdog into a machine for breaking your
// own connection every morning.
func TestDrill09_InactiveWindowSuppressesFiring(t *testing.T) {
	restarts := 0

	w := &session.Watchdog{
		Timeout:  10 * time.Millisecond,
		Interval: 5 * time.Millisecond,
		Active:   func(time.Time) bool { return false },
		Restart:  func() error { restarts++; return nil },
	}

	w.Start()
	time.Sleep(120 * time.Millisecond)
	w.Stop()

	if w.Fired() != 0 || restarts != 0 {
		t.Errorf("watchdog fired %d times outside its active window, restarting %d times; "+
			"silence outside the window is not evidence of anything", w.Fired(), restarts)
	}

	// Same watchdog, window open: it fires.
	w2 := &session.Watchdog{
		Timeout:  10 * time.Millisecond,
		Interval: 5 * time.Millisecond,
		Active:   func(time.Time) bool { return true },
		Restart:  func() error { restarts++; return nil },
	}

	w2.Start()
	waitFor(t, 2*time.Second, "the watchdog to fire inside its window", func() bool {
		return w2.Fired() > 0
	})
	w2.Stop()
}
