package drills

import (
	"strings"
	"testing"
	"time"
)

// Drill 02 — Heartbeat and TestRequest.
//
// The session layer's liveness mechanism, and the precise limit of what it
// proves. Both sides send a Heartbeat when they have had nothing else to say
// for HeartBtInt seconds. If nothing arrives for a little longer than that, the
// engine sends a TestRequest demanding an immediate answer, and drops the
// session if none comes.
//
// That machinery is sound and it is not enough, which is drill 09.
func TestDrill02_HeartbeatsFlow(t *testing.T) {
	lab := startLab(t, withHeartBtInt(1), withoutDropCopy())

	// Nothing is sent by either application. Any traffic from here is the
	// engine keeping the session alive on its own.
	waitFor(t, 6*time.Second, "heartbeats in both directions", func() bool {
		client := normalizeWire(lab.Wire.String(), "FIX.4.4:OECLIENT->FIXLABEX")
		return strings.Count(client, "35=0") >= 2
	})

	client := normalizeWire(lab.Wire.String(), "FIX.4.4:OECLIENT->FIXLABEX")

	if !strings.Contains(client, "--> 8=FIX.4.4|35=0") {
		t.Error("the client sent no Heartbeat")
	}
	if !strings.Contains(client, "<-- 8=FIX.4.4|35=0") {
		t.Error("the venue sent no Heartbeat")
	}

	// Heartbeats consume sequence numbers like everything else. A session that
	// sits idle overnight still advances, which is why a store that resets on
	// restart loses more than you would guess.
	if !lab.OE.LoggedOn(lab.OESessionID) {
		t.Error("session dropped while idle")
	}
}

// TestDrill02_TestRequestOnSilence cuts the wire in one direction.
//
// The client stops receiving anything. Its engine notices, sends a TestRequest,
// gets no answer, and disconnects — the venue meanwhile still receives the
// client's traffic and considers the session perfectly healthy, which is why
// the two sides can disagree about whether a session is alive.
func TestDrill02_TestRequestOnSilence(t *testing.T) {
	lab := startLab(t, withHeartBtInt(1), withProxy(), withoutDropCopy())

	lab.Proxy.Freeze()

	waitFor(t, 10*time.Second, "the client to send a TestRequest", func() bool {
		client := normalizeWire(lab.Wire.String(), "FIX.4.4:OECLIENT->FIXLABEX")
		return strings.Contains(client, "--> 8=FIX.4.4|35=1")
	})

	// No answer can arrive, so the client gives up on the session.
	waitFor(t, 10*time.Second, "the client to drop the session", func() bool {
		return !lab.OE.LoggedOn(lab.OESessionID)
	})

	// Meanwhile the venue is still receiving everything the client sends and
	// has no reason to think anything is wrong. The two sides genuinely
	// disagree about whether this session is alive, and both are right about
	// what they can observe.
	if !lab.Exchange.Silenced() {
		for _, s := range lab.Exchange.Sessions() {
			t.Logf("venue view: %s logged_on=%v seqnums=%+v", s.Kind, s.LoggedOn, s.SeqNums)
		}
	}

	// Deliberately not asserting recovery here. The client will keep
	// reconnecting through a wire that is still cut, and untangling that is
	// drill 07's subject, not this one.
	lab.Proxy.Thaw()
}

// TestDrill02_HeartbeatsProveNothingAboutData is the point of the drill.
//
// The venue is told to stop publishing application messages. Heartbeats keep
// flowing because the engine generates them below the application, so the
// session stays logged on and every liveness check the protocol offers keeps
// passing — while no business data arrives at all.
//
// Nothing in FIX will tell you about this. Drill 09 is what you do about it.
func TestDrill02_HeartbeatsProveNothingAboutData(t *testing.T) {
	lab := startLab(t, withHeartBtInt(1), withoutDropCopy())

	lab.Exchange.SetSilenced(true)

	if err := lab.Exchange.Cancel("nope", "should not reach anyone"); err == nil {
		t.Fatal("expected cancelling an unknown order to fail")
	}

	// Give the session long enough that a stuck one would have been noticed by
	// any transport-level mechanism.
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if !lab.OE.LoggedOn(lab.OESessionID) {
			t.Fatal("session dropped; the point of this drill is that it does not")
		}
		time.Sleep(100 * time.Millisecond)
	}

	client := normalizeWire(lab.Wire.String(), "FIX.4.4:OECLIENT->FIXLABEX")
	if strings.Count(client, "35=0") < 2 {
		t.Error("expected heartbeats to keep flowing through the silence")
	}
	if strings.Contains(client, "35=1") {
		t.Error("a TestRequest was sent; the session was never quiet at the " +
			"transport layer, which is exactly what makes this failure invisible")
	}
}
