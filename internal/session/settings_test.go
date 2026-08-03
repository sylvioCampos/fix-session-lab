package session

import (
	"strings"
	"testing"

	"github.com/quickfixgo/quickfix"
)

const clientCfg = `
[default]
ConnectionType=initiator
BeginString=FIX.4.4
SocketConnectHost=127.0.0.1
SocketConnectPort=9876
StartTime=00:00:00
EndTime=00:00:00

[session]
SenderCompID=OECLIENT
TargetCompID=FIXLABEX
`

// TestOverrideConnectHost is a regression test for a bug that shipped.
//
// The first version wrote to the settings returned by Settings.SessionSettings,
// which hands back freshly built clones on every call. The write went to a copy
// nobody read, `-host exchange` silently did nothing, and every client in
// docker-compose sat in a reconnect loop against 127.0.0.1 inside its own
// container.
//
// Nothing caught it. The drill harness generates its settings text with the
// right host already in it, so it never called this function; CI only built the
// image without running it.
func TestOverrideConnectHost(t *testing.T) {
	settings := mustParse(t, clientCfg)

	if err := OverrideConnectHost(settings, "exchange"); err != nil {
		t.Fatalf("override: %v", err)
	}

	// Read through a *fresh* call, which is what NewInitiator does. Checking
	// the map returned before the override would pass even with the bug.
	for sessionID, s := range settings.SessionSettings() {
		got, err := s.Setting("SocketConnectHost")
		if err != nil {
			t.Fatalf("session %s: %v", sessionID, err)
		}
		if got != "exchange" {
			t.Errorf("session %s connects to %q, want %q", sessionID, got, "exchange")
		}
	}
}

// TestOverrideConnectHostEmptyIsNoop confirms the committed default survives
// when no override is asked for.
func TestOverrideConnectHostEmptyIsNoop(t *testing.T) {
	settings := mustParse(t, clientCfg)

	if err := OverrideConnectHost(settings, ""); err != nil {
		t.Fatalf("override: %v", err)
	}

	for _, s := range settings.SessionSettings() {
		if got, _ := s.Setting("SocketConnectHost"); got != "127.0.0.1" {
			t.Errorf("host = %q, want the configured 127.0.0.1", got)
		}
	}
}

// TestOverrideConnectHostReportsSessionLevelHost covers the case the global
// write cannot reach.
//
// A SocketConnectHost inside a [session] block overlays the global one, so the
// override cannot force it. Failing loudly is the entire point: silently not
// working is what caused the original bug.
func TestOverrideConnectHostReportsSessionLevelHost(t *testing.T) {
	settings := mustParse(t, `
[default]
ConnectionType=initiator
BeginString=FIX.4.4
SocketConnectHost=127.0.0.1
SocketConnectPort=9876
StartTime=00:00:00
EndTime=00:00:00

[session]
SenderCompID=OECLIENT
TargetCompID=FIXLABEX
SocketConnectHost=192.0.2.1
`)

	err := OverrideConnectHost(settings, "exchange")
	if err == nil {
		t.Fatal("expected an error when a [session] block pins its own host")
	}
	if !strings.Contains(err.Error(), "192.0.2.1") {
		t.Errorf("error should name the host that won: %v", err)
	}
}

func mustParse(t *testing.T, cfg string) *quickfix.Settings {
	t.Helper()

	settings, err := quickfix.ParseSettings(strings.NewReader(cfg))
	if err != nil {
		t.Fatalf("parse settings: %v", err)
	}
	return settings
}
