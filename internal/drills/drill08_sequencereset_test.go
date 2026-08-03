package drills

import (
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/sylvioCampos/fix-session-lab/internal/exchange"
)

// Drill 08 — SequenceReset and GapFill.
//
// A resend covers a range of sequence numbers, but not everything in that range
// is worth resending. Administrative messages — Logon, Heartbeat, TestRequest —
// are meaningless replayed: a Heartbeat from four minutes ago proves nothing,
// and an old Logon would be actively confusing. So instead of retransmitting
// them the venue sends a SequenceReset with GapFillFlag=Y saying "skip ahead to
// N", and the receiver's expected sequence number jumps without any message
// having arrived.
//
// This drill produces a range containing nothing worth replaying at all, so the
// whole resend collapses into one GapFill.
func TestDrill08_PureGapFill(t *testing.T) {
	lab := startLab(t,
		withFileStore(t.TempDir()),
		withResetOnLogon("N"),
		withoutDropCopy(),
	)

	// The client goes away. Now move the venue's outbound sequence number
	// forward without generating any messages, so the skipped range contains
	// nothing that can be replayed.
	lab.StopOE(t)

	next, err := lab.Exchange.AdvanceSeqNum(exchange.KindOrderEntry, 5)
	if err != nil {
		t.Fatalf("advance seqnum: %v", err)
	}
	if next < 5 {
		t.Fatalf("expected the outbound seqnum to advance well past 5, got %d", next)
	}

	lab.StartOE(t)
	lab.WaitLoggedOn(t)

	clientView := func() string {
		return normalizeWire(lab.Wire.String(), "FIX.4.4:OECLIENT->FIXLABEX")
	}

	waitFor(t, 10*time.Second, "the SequenceReset-GapFill", func() bool {
		return strings.Contains(clientView(), "35=4")
	})

	view := clientView()

	// The client asked for the range.
	if !strings.Contains(view, "--> 8=FIX.4.4|35=2") {
		t.Error("the client sent no ResendRequest")
	}

	// And got back a gap fill rather than messages: GapFillFlag=Y with a
	// NewSeqNo to jump to.
	if !strings.Contains(view, "123=Y") {
		t.Error("expected GapFillFlag=Y (123) on the SequenceReset")
	}
	if !strings.Contains(view, "36=") {
		t.Error("expected NewSeqNo (36) telling the client where to resume")
	}

	// Nothing was actually replayed, because nothing in the range existed.
	if lab.OE.ReplayCount() != 0 {
		t.Errorf("%d message(s) came back with PossDupFlag=Y; the skipped range "+
			"held no real messages, so there was nothing to replay",
			lab.OE.ReplayCount())
	}

	// The session is usable again: the sequence numbers realigned.
	clOrdID := sendOrder(t, lab, "PETR4", decimal.NewFromInt(50), decimal.NewFromFloat(25.00))
	waitFor(t, 5*time.Second, "the session to work normally after the gap fill", func() bool {
		v, ok := lab.OE.Order(clOrdID)
		return ok && v.OrdStatus == exchange.StatusNew
	})
}

// TestDrill08_GapFillCarriesPossDupFlag records a detail that surprises people.
//
// The SequenceReset itself is marked PossDupFlag=Y. It is administrative
// housekeeping standing in for messages that were never delivered, so it is
// flagged as a duplicate even though the receiver has certainly not seen it
// before. A client that treats 43=Y as "ignore this" will ignore the very
// message telling it where to resume, and then never resynchronize.
func TestDrill08_GapFillCarriesPossDupFlag(t *testing.T) {
	lab := startLab(t,
		withFileStore(t.TempDir()),
		withResetOnLogon("N"),
		withoutDropCopy(),
	)

	lab.StopOE(t)
	if _, err := lab.Exchange.AdvanceSeqNum(exchange.KindOrderEntry, 3); err != nil {
		t.Fatalf("advance seqnum: %v", err)
	}
	lab.StartOE(t)
	lab.WaitLoggedOn(t)

	waitFor(t, 10*time.Second, "the SequenceReset", func() bool {
		return strings.Contains(normalizeWire(lab.Wire.String(), "FIX.4.4:OECLIENT->FIXLABEX"), "35=4")
	})

	view := normalizeWire(lab.Wire.String(), "FIX.4.4:OECLIENT->FIXLABEX")

	for _, line := range strings.Split(view, "\n") {
		if !strings.Contains(line, "|35=4|") {
			continue
		}
		if !strings.Contains(line, "|43=Y|") {
			t.Errorf("SequenceReset arrived without PossDupFlag=Y:\n  %s", line)
		}
		return
	}
	t.Error("no SequenceReset found in the client's view")
}
