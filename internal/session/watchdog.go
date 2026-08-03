package session

import (
	"log"
	"sync"
	"time"

	"github.com/quickfixgo/quickfix"
)

// Watchdog forces a reconnect when a session stops delivering business data
// while still appearing perfectly healthy.
//
// # The failure it exists for
//
// A FIX session can be logged on, with an open socket and heartbeats flowing in
// both directions, and deliver no application messages at all. Every engine
// will report that session as healthy, because by the protocol's definition it
// is: heartbeats are the liveness mechanism, and they are working. Nothing
// below the application layer will ever notice, and nothing will recover it.
//
// Detecting this is the application's job. Nobody else can do it, because only
// the application knows what "data should be arriving by now" means.
//
// # Why it rebuilds the whole Initiator
//
// quickfixgo offers no way to disconnect one session. The session type is
// unexported, and Initiator.Stop() unregisters every session the initiator
// owns, so calling Start() afterwards leaves them registered nowhere and
// unroutable. The only reliable recovery is to tear the Initiator down and
// construct a new one — which is why every client in this lab owns exactly one
// session.
//
// If you are arriving from QuickFIX/J: there, the equivalent code has a
// different trap. Session.logout() and Session.disconnect(reason, true) look
// interchangeable and are opposites — logout() is a graceful, protocol-level
// close, so the initiator treats it as deliberate and suppresses auto-reconnect
// entirely. A watchdog built on logout() makes a stuck session permanently
// stuck: strictly worse than having no watchdog. In Go you cannot make that
// mistake, because you have no per-session handle to make it with.
type Watchdog struct {
	// Timeout is how long application silence may last before the session is
	// considered stuck. It must comfortably exceed the longest legitimate quiet
	// period, or the watchdog will tear down healthy sessions.
	Timeout time.Duration

	// Interval is how often the check runs. Defaults to Timeout/4.
	Interval time.Duration

	// Active reports whether silence is suspicious right now. Nil means always.
	//
	// This is where the second half of the real-world bug lives. A window that
	// opens before the venue actually starts publishing turns ordinary
	// pre-open quiet into a "stuck session", and the watchdog then reconnects
	// a session that was fine. Get the boundary wrong by an hour and you have
	// built a machine for breaking your own connection every morning.
	Active func(time.Time) bool

	// Restart tears down the current Initiator and builds a new one.
	Restart func() error

	Log *log.Logger

	mu       sync.Mutex
	lastApp  time.Time
	fired    int
	stopping chan struct{}
	stopped  chan struct{}
}

// Notify records that application traffic arrived. Wire it to a Client's
// OnInbound and pass only InboundApp — feeding it admin traffic defeats the
// entire purpose, since heartbeats never stop.
func (w *Watchdog) Notify() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lastApp = time.Now()
}

// Fired is how many times the watchdog has forced a reconnect.
func (w *Watchdog) Fired() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.fired
}

// Start begins watching. Call Stop to end it.
func (w *Watchdog) Start() {
	w.mu.Lock()
	w.lastApp = time.Now()
	w.stopping = make(chan struct{})
	w.stopped = make(chan struct{})
	stopping, stopped := w.stopping, w.stopped
	interval := w.Interval
	if interval <= 0 {
		interval = w.Timeout / 4
	}
	if interval <= 0 {
		interval = time.Second
	}
	w.mu.Unlock()

	go func() {
		defer close(stopped)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-stopping:
				return
			case now := <-ticker.C:
				w.check(now)
			}
		}
	}()
}

// Stop ends the watch and waits for the goroutine to exit.
func (w *Watchdog) Stop() {
	w.mu.Lock()
	stopping, stopped := w.stopping, w.stopped
	w.stopping = nil
	w.mu.Unlock()

	if stopping == nil {
		return
	}
	close(stopping)
	<-stopped
}

func (w *Watchdog) check(now time.Time) {
	if w.Active != nil && !w.Active(now) {
		return
	}

	w.mu.Lock()
	silent := now.Sub(w.lastApp)
	if silent < w.Timeout {
		w.mu.Unlock()
		return
	}
	// Reset before restarting, not after. The new session needs a full timeout
	// to produce its first message, and leaving the old timestamp in place
	// would fire again immediately and loop.
	w.lastApp = now
	w.fired++
	fired := w.fired
	w.mu.Unlock()

	w.logf("no application traffic for %s — forcing reconnect (%d)", silent.Round(time.Millisecond), fired)

	if w.Restart == nil {
		return
	}
	if err := w.Restart(); err != nil {
		w.logf("reconnect failed: %v", err)
	}
}

func (w *Watchdog) logf(format string, v ...any) {
	if w.Log != nil {
		w.Log.Printf("watchdog: "+format, v...)
	}
}

// WatchClient wires a watchdog to a client's inbound traffic, counting only
// application messages.
func WatchClient(c *Client, w *Watchdog) {
	c.OnInbound(func(_ quickfix.SessionID, class InboundClass) {
		if class == InboundApp {
			w.Notify()
		}
	})
}
