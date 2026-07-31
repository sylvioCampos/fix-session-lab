package drills

import (
	"strings"
	"testing"

	"github.com/sylvioCampos/fix-session-lab/internal/fixlog"
	"github.com/sylvioCampos/fix-session-lab/internal/session"
)

// Drill 01 — first logon.
//
// Verifies the handshake both sessions must complete before anything else can
// happen, and that the password never reaches a log.
func TestDrill01_FirstLogon(t *testing.T) {
	t.Setenv(session.PasswordEnvVar(oeCompID), "hunter2-oe")
	t.Setenv(session.PasswordEnvVar(dcCompID), "hunter2-dc")

	lab := startLab(t)

	// Both sessions logged on — startLab would have failed otherwise. Confirm
	// the venue agrees, since a client can believe it is logged on while the
	// venue has already decided otherwise.
	sessions := lab.Exchange.Sessions()
	if len(sessions) != 2 {
		t.Fatalf("expected 2 configured sessions, got %d", len(sessions))
	}
	for _, s := range sessions {
		if !s.LoggedOn {
			t.Errorf("venue reports %s (%s) not logged on", s.SessionID, s.Kind)
		}
	}

	wire := lab.Wire.String()

	// The Logon carries the password in RawData (96), with its length in 95 —
	// not in Username/Password (553/554), which this venue ignores.
	if !strings.Contains(wire, "35=A") {
		t.Fatal("no Logon on the wire")
	}
	if !strings.Contains(wire, "95=") {
		t.Error("Logon carried no RawDataLength (95)")
	}

	// The password itself must never appear. This is the whole reason the lab
	// ships its own LogFactory.
	for _, secret := range []string{"hunter2-oe", "hunter2-dc"} {
		if strings.Contains(wire, secret) {
			t.Errorf("password %q leaked into the FIX log", secret)
		}
	}
	if !strings.Contains(wire, "96=****") {
		t.Error("expected RawData (96) to be masked in the log")
	}
}

// TestRedactLeavesOtherFieldsIntact guards the redaction helper directly: it
// must mask secrets without disturbing anything else, including values that
// contain characters a naive regular expression would trip over.
func TestRedactLeavesOtherFieldsIntact(t *testing.T) {
	raw := []byte("8=FIX.4.4\x0135=A\x0198=0\x01108=30\x0195=7\x0196=s3cr3t\x0158=app=1\x0110=000\x01")

	got := string(fixlog.Redact(raw))
	want := "8=FIX.4.4|35=A|98=0|108=30|95=7|96=****|58=app=1|10=000|"

	if got != want {
		t.Errorf("Redact()\n got: %s\nwant: %s", got, want)
	}
}
