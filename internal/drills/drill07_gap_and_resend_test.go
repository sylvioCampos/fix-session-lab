package drills

import (
	"strings"
	"testing"
	"time"

	"github.com/quickfixgo/enum"
	"github.com/shopspring/decimal"

	"github.com/sylvioCampos/fix-session-lab/internal/client"
	"github.com/sylvioCampos/fix-session-lab/internal/exchange"
)

// Drill 07 — gap and ResendRequest.
//
// The one this lab is really for.
//
// A client disconnects. The venue keeps trading and keeps sending reports the
// client never receives. The client reconnects, notices the inbound sequence
// number is higher than it expected, asks for the missing range with a
// ResendRequest, and the venue replays — marking every replayed message
// PossDupFlag=Y so the client knows it is seeing history, not news.
//
// Nothing here is simulated. The reports really are generated while the client
// is away, really are persisted by the engine with sequence numbers assigned,
// and really are replayed off the store.
func TestDrill07_GapAndResend(t *testing.T) {
	lab := startLab(t,
		withFileStore(t.TempDir()),
		withResetOnLogon("N"), // Y here would erase the gap instead of recovering it
		withoutDropCopy(),
	)

	clOrdID := sendOrder(t, lab, "PETR4", decimal.NewFromInt(100), decimal.NewFromFloat(25.50))

	waitFor(t, 3*time.Second, "the order acknowledgement", func() bool {
		v, ok := lab.OE.Order(clOrdID)
		return ok && v.OrdStatus == exchange.StatusNew
	})

	// The client goes away. Snapshot after the disconnect, not before: the
	// Logout exchange consumes a sequence number of its own and would be
	// counted as part of the gap.
	lab.StopOE(t)
	beforeGap := oeSeqNums(t, lab)

	// The venue keeps working. Each of these is a real ExecutionReport: the
	// engine assigns it a sequence number and writes it to the store even
	// though there is no connection to put it on the wire.
	if err := lab.Exchange.Partial(clOrdID, decimal.NewFromInt(30), decimal.NewFromFloat(25.50)); err != nil {
		t.Fatalf("partial while disconnected: %v", err)
	}
	if err := lab.Exchange.Partial(clOrdID, decimal.NewFromInt(30), decimal.NewFromFloat(25.60)); err != nil {
		t.Fatalf("partial while disconnected: %v", err)
	}
	if err := lab.Exchange.Fill(clOrdID, decimal.NewFromFloat(25.70)); err != nil {
		t.Fatalf("fill while disconnected: %v", err)
	}

	duringGap := oeSeqNums(t, lab)
	if duringGap.LastSent-beforeGap.LastSent != 3 {
		t.Fatalf("expected the venue's outbound seqnum to advance by 3 while "+
			"disconnected, went %d -> %d", beforeGap.LastSent, duringGap.LastSent)
	}

	// The client comes back with a fresh application — it has forgotten
	// everything, exactly like a restarted process.
	lab.StartOE(t)
	lab.WaitLoggedOn(t)

	// Recovery: the client must end up knowing the order is filled, which it
	// can only learn from the replay.
	waitFor(t, 10*time.Second, "the replayed reports to arrive", func() bool {
		v, ok := lab.OE.Order(clOrdID)
		return ok && v.OrdStatus == exchange.StatusFilled
	})

	recovered, _ := lab.OE.Order(clOrdID)

	if !recovered.CumQty.Equal(decimal.NewFromInt(100)) {
		t.Errorf("CumQty after recovery = %s, want 100", recovered.CumQty)
	}
	if !recovered.LeavesQty.IsZero() {
		t.Errorf("LeavesQty after recovery = %s, want 0", recovered.LeavesQty)
	}

	// (30×25.50 + 30×25.60 + 40×25.70) / 100 = 2561/100 = 25.61
	if got := recovered.AvgPx.Round(2); !got.Equal(decimal.NewFromFloat(25.61)) {
		t.Errorf("AvgPx after recovery = %s, want 25.61", got)
	}

	// Every replayed report must be flagged. A client that cannot tell a
	// replay from a new execution will double-count the moment it recovers.
	if lab.OE.ReplayCount() == 0 {
		t.Error("no message arrived with PossDupFlag=Y; nothing was actually replayed, " +
			"so this test proved nothing about recovery")
	}

	// Recovery is not finished when the last ExecutionReport lands. The venue
	// still owes a SequenceReset-GapFill for the administrative message that
	// occupied the final slot in the range, and it arrives a moment later.
	// Asserting the trace before that would be asserting a half-finished
	// recovery — and would make this test flaky, which for the flagship drill
	// is worse than useless.
	//
	// The condition is checked against the client's own view, not the whole
	// capture. Every session writes to one buffer, so the venue logs the
	// GapFill on its side before the client has received it — waiting on the
	// raw capture would let the assertion run a beat too early and reintroduce
	// exactly the flake it is meant to remove.
	clientView := func() string {
		return normalizeWire(lab.Wire.String(), "FIX.4.4:OECLIENT->FIXLABEX")
	}
	waitFor(t, 5*time.Second, "the client to receive the SequenceReset-GapFill", func() bool {
		return strings.Contains(clientView(), "35=4")
	})

	// The wire itself is the lesson here, so it is asserted byte for byte
	// (minus timestamps and derived lengths). If the trace printed in
	// docs/drills/07 ever stops matching what the code emits, this fails.
	assertGolden(t, "07-gap-and-resend.golden", clientView())
}

// TestDrill07_ReplayIsIdempotent is the assertion that matters most and the one
// a naive client fails.
//
// The client's position after recovery must equal its position had it never
// disconnected. That holds only because CumQty is a running total that gets
// assigned. A client that added LastQty on each report would reach 200 here,
// and would place its next order against a position that does not exist.
func TestDrill07_ReplayIsIdempotent(t *testing.T) {
	lab := startLab(t,
		withFileStore(t.TempDir()),
		withResetOnLogon("N"),
		withoutDropCopy(),
	)

	clOrdID := sendOrder(t, lab, "VALE3", decimal.NewFromInt(80), decimal.NewFromFloat(61.00))
	waitFor(t, 3*time.Second, "the acknowledgement", func() bool {
		v, ok := lab.OE.Order(clOrdID)
		return ok && v.OrdStatus == exchange.StatusNew
	})

	lab.StopOE(t)
	if err := lab.Exchange.Fill(clOrdID, decimal.NewFromFloat(61.00)); err != nil {
		t.Fatalf("fill while disconnected: %v", err)
	}
	lab.StartOE(t)
	lab.WaitLoggedOn(t)

	waitFor(t, 10*time.Second, "the replayed fill", func() bool {
		v, ok := lab.OE.Order(clOrdID)
		return ok && v.OrdStatus == exchange.StatusFilled
	})

	v, _ := lab.OE.Order(clOrdID)
	if !v.CumQty.Equal(decimal.NewFromInt(80)) {
		t.Errorf("CumQty = %s, want 80 — a replayed fill was counted more than once",
			v.CumQty)
	}
}

func sendOrder(t *testing.T, l *lab, symbol string, qty, px decimal.Decimal) string {
	t.Helper()

	clOrdID, err := l.OE.Send(l.OESessionID, client.NewOrder{
		Symbol:   symbol,
		Side:     enum.Side_BUY,
		OrderQty: qty,
		Price:    px,
	})
	if err != nil {
		t.Fatalf("send order: %v", err)
	}
	return clOrdID
}
