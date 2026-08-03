package drills

import (
	"strings"
	"testing"
	"time"

	"github.com/quickfixgo/enum"
	"github.com/shopspring/decimal"

	"github.com/sylvioCampos/fix-session-lab/internal/client"
)

// TestRejectStorm is a regression test for a bug that shipped and that no unit
// test could have found.
//
// Both applications rejected any message type they did not recognise. Neither
// recognised a BusinessMessageReject. So the first time the venue refused an
// order, the client rejected the rejection, the venue rejected that, and the
// two ping-ponged at wire speed — thirteen thousand messages in six seconds,
// sequence numbers gone, both stores full of garbage.
//
// The existing drill 11 tests call FromApp directly with a hand-built message.
// That checks what one side answers, and by construction cannot observe what
// happens when the other side answers back. Only two real applications on a
// real socket produce the loop.
//
// The rule this encodes: never reject a reject.
func TestRejectStorm(t *testing.T) {
	lab := startLab(t, withoutDropCopy())

	// Provoke a genuine business reject: two orders with the same ClOrdID.
	order := client.NewOrder{
		Symbol:   "PETR4",
		Side:     enum.Side_BUY,
		OrderQty: decimal.NewFromInt(100),
		Price:    decimal.NewFromFloat(25.50),
	}

	first, err := lab.OE.Send(lab.OESessionID, order)
	if err != nil {
		t.Fatalf("send first order: %v", err)
	}
	waitFor(t, 5*time.Second, "the first order to be acknowledged", func() bool {
		_, ok := lab.OE.Order(first)
		return ok
	})

	// Rewind the client's counter so it reissues the same ClOrdID, which is
	// exactly what a restarted process does.
	lab.OE.ResetClOrdIDSeq()

	if _, err := lab.OE.Send(lab.OESessionID, order); err != nil {
		t.Fatalf("send duplicate order: %v", err)
	}

	clientView := func() string {
		return normalizeWire(lab.Wire.String(), "FIX.4.4:OECLIENT->FIXLABEX")
	}

	waitFor(t, 5*time.Second, "the venue to reject the duplicate", func() bool {
		return strings.Contains(clientView(), "<-- 8=FIX.4.4|35=j")
	})

	// Let any storm develop. Two seconds of ping-pong produced thousands of
	// messages when this was broken.
	time.Sleep(2 * time.Second)

	view := clientView()
	outbound := strings.Count(view, "--> 8=FIX.4.4|35=j")
	inbound := strings.Count(view, "<-- 8=FIX.4.4|35=j")

	if outbound != 0 {
		t.Errorf("the client sent %d business reject(s); it must never answer a reject "+
			"with a reject", outbound)
	}
	if inbound != 1 {
		t.Errorf("expected exactly one inbound business reject, got %d — "+
			"more than one means the loop is still running", inbound)
	}

	// The session must still be usable. A storm leaves it unrecoverable.
	if !lab.OE.LoggedOn(lab.OESessionID) {
		t.Fatal("session went down")
	}

	lab.OE.ResetClOrdIDSeq()
	lab.OE.SetClOrdIDPrefix("X")

	recovered, err := lab.OE.Send(lab.OESessionID, order)
	if err != nil {
		t.Fatalf("send after reject: %v", err)
	}
	waitFor(t, 5*time.Second, "the session to keep working after a reject", func() bool {
		_, ok := lab.OE.Order(recovered)
		return ok
	})
}

// TestClOrdIDPrefixSurvivesRestart covers the other half.
//
// Not rejecting a reject stops the storm; it does not stop the client
// generating colliding ClOrdIDs after every restart. The venue is right to
// refuse those, so the client has to stop producing them.
func TestClOrdIDPrefixSurvivesRestart(t *testing.T) {
	lab := startLab(t, withoutDropCopy())

	order := client.NewOrder{
		Symbol:   "PETR4",
		Side:     enum.Side_BUY,
		OrderQty: decimal.NewFromInt(100),
		Price:    decimal.NewFromFloat(25.50),
	}

	lab.OE.SetClOrdIDPrefix("run1")
	first, err := lab.OE.Send(lab.OESessionID, order)
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	// A restarted process: counter back to 1, different prefix.
	lab.OE.ResetClOrdIDSeq()
	lab.OE.SetClOrdIDPrefix("run2")
	second, err := lab.OE.Send(lab.OESessionID, order)
	if err != nil {
		t.Fatalf("send after restart: %v", err)
	}

	if first == second {
		t.Fatalf("both runs produced ClOrdID %q; the venue will reject the second", first)
	}

	waitFor(t, 5*time.Second, "both orders to be accepted", func() bool {
		return len(lab.Book.All()) == 2
	})
}
