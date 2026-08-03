package session

import (
	"fmt"
	"log"
	"sync"

	"github.com/quickfixgo/quickfix"
)

// Supervisor owns an Initiator's lifecycle so it can be replaced.
//
// It exists because "reconnect this session" is not an operation quickfixgo
// offers. Initiator.Stop() unregisters the sessions it owns, so the same
// Initiator cannot be restarted — Start() would run against sessions no longer
// in the registry, and SendToTarget would fail with an unknown-session error
// that looks nothing like the actual cause.
//
// So a reconnect means: stop the Initiator, build a new one from the same
// settings and the same store, start it. The store is what carries sequence
// continuity across the swap, which is why the file store matters here and a
// memory store would silently break recovery.
type Supervisor struct {
	// Build constructs a fresh Initiator. It is called once per (re)start, so
	// it must be safe to call repeatedly.
	Build func() (*quickfix.Initiator, error)

	Log *log.Logger

	mu      sync.Mutex
	current *quickfix.Initiator
	starts  int
}

// Start builds and starts an Initiator.
func (s *Supervisor) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startLocked()
}

func (s *Supervisor) startLocked() error {
	if s.current != nil {
		return fmt.Errorf("initiator already running")
	}

	init, err := s.Build()
	if err != nil {
		return fmt.Errorf("build initiator: %w", err)
	}
	if err := init.Start(); err != nil {
		return fmt.Errorf("start initiator: %w", err)
	}

	s.current = init
	s.starts++
	return nil
}

// Stop stops the current Initiator, if any.
func (s *Supervisor) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
}

func (s *Supervisor) stopLocked() {
	if s.current == nil {
		return
	}
	s.current.Stop()
	s.current = nil
}

// Restart replaces the Initiator. This is the closest thing to a per-session
// disconnect that quickfixgo permits.
func (s *Supervisor) Restart() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.stopLocked()
	if s.Log != nil {
		s.Log.Printf("supervisor: rebuilding initiator")
	}
	return s.startLocked()
}

// Starts is how many Initiators have been built, including the first.
func (s *Supervisor) Starts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.starts
}
