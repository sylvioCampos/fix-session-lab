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
//
// It writes to GlobalSettings, not to the per-session settings, and that is not
// a stylistic choice.
//
//	// quickfix/settings.go
//	func (s *Settings) SessionSettings() map[SessionID]*SessionSettings {
//	    for sessionID, settings := range s.sessionSettings {
//	        cloneSettings := s.globalSettings.clone()   // <- a clone
//	        cloneSettings.overlay(settings)
//
// SessionSettings() hands back freshly built copies every call. Mutating what
// it returns changes nothing: NewInitiator calls it again and gets clean
// clones. The first version of this function did exactly that, silently did
// nothing, and was only caught by running the docker-compose stack — the tests
// generate their settings text with the right host already in it, so they never
// exercised this path at all.
//
// GlobalSettings, by contrast, returns the live object. Writing there reaches
// every session, because each clone starts from it.
//
// The catch is that a value in a [session] block still overlays the global one,
// so this cannot force a host onto a session that sets its own. Rather than
// fail silently a second time, the result is verified and an error returned.
func OverrideConnectHost(settings *quickfix.Settings, host string) error {
	if host == "" {
		return nil
	}

	settings.GlobalSettings().Set("SocketConnectHost", host)

	for sessionID, s := range settings.SessionSettings() {
		got, err := s.Setting("SocketConnectHost")
		if err != nil {
			return fmt.Errorf("session %s has no SocketConnectHost after override: %w", sessionID, err)
		}
		if got != host {
			return fmt.Errorf(
				"session %s still points at %q after overriding the host to %q; "+
					"a SocketConnectHost in its [session] block overrides the global one",
				sessionID, got, host)
		}
	}

	return nil
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
