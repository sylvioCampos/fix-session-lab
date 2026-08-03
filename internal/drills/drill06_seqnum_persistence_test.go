package drills

import (
	"testing"
	"time"

	"github.com/quickfixgo/enum"
	"github.com/shopspring/decimal"

	"github.com/sylvioCampos/fix-session-lab/internal/client"
	"github.com/sylvioCampos/fix-session-lab/internal/exchange"
)

// Drill 06 — sequence number persistence.
//
// A FIX session's sequence numbers are the only thing that lets either side
// know it missed something. They therefore have to outlive the process. This
// drill shows the store doing its job, and shows ResetOnLogon=Y quietly
// destroying it.
func TestDrill06_SeqNumsSurviveRestart(t *testing.T) {
	lab := startLab(t,
		withFileStore(t.TempDir()),
		withResetOnLogon("N"),
		withoutDropCopy(),
	)

	// Generate traffic so the numbers are somewhere interesting.
	for range 3 {
		if _, err := lab.OE.Send(lab.OESessionID, client.NewOrder{
			Symbol:   "PETR4",
			Side:     enum.Side_BUY,
			OrderQty: decimal.NewFromInt(100),
			Price:    decimal.NewFromFloat(25.50),
		}); err != nil {
			t.Fatalf("send order: %v", err)
		}
	}

	waitFor(t, 3*time.Second, "three orders to be acknowledged", func() bool {
		return len(lab.Book.All()) == 3
	})

	before := oeSeqNums(t, lab)
	if before.LastReceived < 4 {
		t.Fatalf("expected the venue to have received at least 4 messages, got %d",
			before.LastReceived)
	}

	// Restart the client. Its process is gone; only the store survives.
	lab.StopOE(t)
	lab.StartOE(t)
	lab.WaitLoggedOn(t)

	after := oeSeqNums(t, lab)

	// The reconnect Logon continues the sequence rather than restarting it.
	// This is the whole point: had the numbers reset, neither side could tell
	// a fresh session from one that had missed everything in between.
	if after.LastReceived <= before.LastReceived {
		t.Errorf("sequence numbers went backwards across the restart: %d then %d\n"+
			"the store did not survive, so no gap could ever be detected",
			before.LastReceived, after.LastReceived)
	}
}

// TestDrill06_ResetOnLogonDestroysContinuity is the same restart with the one
// setting flipped. It is here because the failure it demonstrates is invisible:
// everything reconnects, everything looks healthy, and the session has silently
// forgotten that it missed anything.
func TestDrill06_ResetOnLogonDestroysContinuity(t *testing.T) {
	lab := startLab(t,
		withFileStore(t.TempDir()),
		withResetOnLogon("Y"),
		withoutDropCopy(),
	)

	if _, err := lab.OE.Send(lab.OESessionID, client.NewOrder{
		Symbol:   "PETR4",
		Side:     enum.Side_BUY,
		OrderQty: decimal.NewFromInt(100),
		Price:    decimal.NewFromFloat(25.50),
	}); err != nil {
		t.Fatalf("send order: %v", err)
	}

	waitFor(t, 3*time.Second, "the order to be acknowledged", func() bool {
		return len(lab.Book.All()) == 1
	})

	lab.StopOE(t)
	lab.StartOE(t)
	lab.WaitLoggedOn(t)

	after := oeSeqNums(t, lab)

	// Back to 1. The store was written and then thrown away on the Logon.
	if after.LastReceived != 1 {
		t.Errorf("with ResetOnLogon=Y the reconnect Logon should be MsgSeqNum 1, got %d",
			after.LastReceived)
	}
}

func oeSeqNums(t *testing.T, l *lab) exchange.SeqNums {
	t.Helper()

	for _, s := range l.Exchange.Sessions() {
		if s.Kind == exchange.KindOrderEntry {
			return s.SeqNums
		}
	}
	t.Fatal("no order-entry session found")
	return exchange.SeqNums{}
}
