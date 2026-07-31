package session

import (
	"fmt"

	"github.com/quickfixgo/quickfix"
)

// OverrideConnectHost points every session in a settings file at host.
//
// The committed configs use 127.0.0.1 so that `go run ./cmd/oe-client` works
// with no arguments. Inside docker-compose the venue answers to a service name
// instead, and rewriting one setting is better than maintaining a second copy
// of every config file that differs by one line.
func OverrideConnectHost(settings *quickfix.Settings, host string) {
	if host == "" {
		return
	}
	for _, s := range settings.SessionSettings() {
		s.Set("SocketConnectHost", host)
	}
}

// SoleSession returns the single session defined in a settings file.
//
// Each client binary in this lab owns exactly one session. That is not a
// stylistic choice: quickfixgo exposes no way to disconnect one session, and
// Initiator.Stop() unregisters every session the initiator owns, so a process
// that multiplexes sessions cannot recycle just one of them. Enforcing the
// invariant here means a settings file that quietly grows a second session
// fails at startup rather than at the moment a watchdog needs to act.
func SoleSession(settings *quickfix.Settings) (quickfix.SessionID, error) {
	all := settings.SessionSettings()
	if len(all) != 1 {
		return quickfix.SessionID{}, fmt.Errorf(
			"expected exactly one session in the settings file, found %d", len(all))
	}
	for id := range all {
		return id, nil
	}
	return quickfix.SessionID{}, fmt.Errorf("no session found")
}
