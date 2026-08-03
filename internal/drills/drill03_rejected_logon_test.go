package drills

import (
	"strings"
	"testing"
	"time"

	"github.com/sylvioCampos/fix-session-lab/internal/session"
)

// Drill 03 — rejected logon.
//
// A refused Logon does not look like a refusal from the outside. The venue
// answers with a Logout and drops the connection; the client reconnects on its
// interval and is refused again. What you see is a reconnect loop identical to
// the one a firewall produces. The only thing distinguishing them is the text
// on the Logout, which is why reading it matters.
func TestDrill03_RejectedLogon(t *testing.T) {
	lab := startLab(t, withoutDropCopy())

	// Session is up. Now make the venue refuse.
	lab.Exchange.SetRejectLogons("invalid credentials")

	lab.StopOE(t)
	lab.StartOE(t)

	// It must not come back.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if lab.OE.LoggedOn(lab.OESessionID) {
			t.Fatal("the venue was told to refuse every logon, but the session logged on")
		}
		time.Sleep(50 * time.Millisecond)
	}

	wire := lab.Wire.String()

	// The refusal is a Logout carrying the reason, not a Reject and not a
	// silent close.
	if !strings.Contains(wire, "35=5") {
		t.Error("expected the venue to answer the Logon with a Logout (35=5)")
	}
	if !strings.Contains(wire, "58=invalid credentials") {
		t.Error("expected the Logout to carry the reason in Text (58); without it " +
			"a refused logon is indistinguishable from a network failure")
	}

	// And the client keeps trying, which is what makes this hard to diagnose.
	if strings.Count(wire, "35=A") < 3 {
		t.Error("expected the client to keep retrying the Logon")
	}

	// Recover: stop refusing, and the same client reconnects unaided.
	lab.Exchange.SetRejectLogons("")
	lab.WaitLoggedOn(t)
}

// TestDrill03_WrongPassword is the same refusal reached the ordinary way.
//
// The venue accepts any password when it has no credential configured for a
// counterparty, so this test configures one and then presents the wrong one.
func TestDrill03_WrongPassword(t *testing.T) {
	// The venue expects this for OECLIENT...
	t.Setenv(session.PasswordEnvVar(oeCompID), "correct-horse")

	lab := startLab(t, withoutDropCopy())
	lab.StopOE(t)

	// ...and now the client will present something else.
	t.Setenv(session.PasswordEnvVar(oeCompID), "wrong-password")
	lab.StartOE(t)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if lab.OE.LoggedOn(lab.OESessionID) {
			t.Fatal("logged on with the wrong password")
		}
		time.Sleep(50 * time.Millisecond)
	}

	if !strings.Contains(lab.Wire.String(), "58=invalid credentials") {
		t.Error("expected the venue to say why it refused")
	}

	// Neither password may appear anywhere in the log.
	for _, secret := range []string{"correct-horse", "wrong-password"} {
		if strings.Contains(lab.Wire.String(), secret) {
			t.Errorf("password %q leaked into the FIX log", secret)
		}
	}
}
