package drills

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/sylvioCampos/fix-session-lab/internal/exchange"
	"github.com/sylvioCampos/fix-session-lab/internal/session"
)

// Drill 10 — cancel on disconnect.
//
// Orders outlive the session that created them. If your connection dies with
// working orders on the book, they keep trading — and you have no way to see
// them, cancel them, or react to their fills.
//
// Cancel on disconnect is the venue's answer: tell it on the Logon what to
// cancel and how long to wait first, and it cleans up for you.
func TestDrill10_CancelOnDisconnect(t *testing.T) {
	lab := startLab(t,
		withCOD(session.CODOnDisconnect, 200), // 200ms grace, short enough to test
		withFileStore(t.TempDir()),
		withResetOnLogon("N"),
	)

	// The venue must have seen the COD fields on the Logon. Arming it in the
	// config and forgetting to send it is a silent failure — the venue simply
	// does not protect you, and nothing says so.
	var armed bool
	for _, s := range lab.Exchange.Sessions() {
		if s.Kind == exchange.KindOrderEntry {
			armed = s.CODType == session.CODOnDisconnect && s.CODTimeout == 200
		}
	}
	if !armed {
		t.Fatal("the venue did not see CODType/CODTimeoutWindow on the Logon")
	}

	clOrdID := sendOrder(t, lab, "PETR4", decimal.NewFromInt(100), decimal.NewFromFloat(25.50))
	waitFor(t, 3*time.Second, "the order to be working", func() bool {
		o, ok := lab.Book.Get(clOrdID)
		return ok && o.IsOpen()
	})

	// The connection dies.
	lab.StopOE(t)

	// After the grace window the venue cancels.
	waitFor(t, 5*time.Second, "the venue to cancel the open order", func() bool {
		o, ok := lab.Book.Get(clOrdID)
		return ok && o.Status == exchange.StatusCanceled
	})

	// The order-entry client is gone, so the cancel report is only observable
	// on the drop copy — which is a large part of why you run one.
	waitFor(t, 5*time.Second, "the cancel report on the drop-copy session", func() bool {
		for _, r := range lab.DC.Reports() {
			if r.ClOrdID == clOrdID && r.ExecType == exchange.ExecTypeCanceled {
				return true
			}
		}
		return false
	})
}

// TestDrill10_ReconnectInsideWindowStandsDown is why the timeout window exists.
//
// Without it every transient network blip becomes a mass cancel, which is a
// worse problem than the one COD solves. A client that gets back inside the
// window keeps its orders.
func TestDrill10_ReconnectInsideWindowStandsDown(t *testing.T) {
	lab := startLab(t,
		withCOD(session.CODOnDisconnect, 3000), // generous window
		withFileStore(t.TempDir()),
		withResetOnLogon("N"),
		withoutDropCopy(),
	)

	clOrdID := sendOrder(t, lab, "VALE3", decimal.NewFromInt(200), decimal.NewFromFloat(61.00))
	waitFor(t, 3*time.Second, "the order to be working", func() bool {
		o, ok := lab.Book.Get(clOrdID)
		return ok && o.IsOpen()
	})

	lab.StopOE(t)
	lab.StartOE(t)
	lab.WaitLoggedOn(t)

	// Wait out the original window and then some. The order must survive.
	time.Sleep(4 * time.Second)

	o, ok := lab.Book.Get(clOrdID)
	if !ok {
		t.Fatal("order vanished")
	}
	if o.Status == exchange.StatusCanceled {
		t.Error("the order was cancelled despite the client reconnecting inside " +
			"the timeout window; every brief disconnect would flatten the book")
	}
}

// TestDrill10_GoodTillOrdersSurvive records a distinction that costs money.
//
// Cancel on disconnect targets day orders. A good-till order is meant to
// outlive the session — cancelling it because a socket dropped would destroy
// standing instructions the client never withdrew.
func TestDrill10_GoodTillOrdersSurvive(t *testing.T) {
	lab := startLab(t,
		withCOD(session.CODOnDisconnect, 100),
		withFileStore(t.TempDir()),
		withResetOnLogon("N"),
		withoutDropCopy(),
	)

	// Place one of each directly on the venue's book so the time-in-force is
	// explicit rather than whatever the client happens to send.
	day := &exchange.OrderState{
		ClOrdID: "DAY001", Symbol: "PETR4", Side: "1", OrdType: "2",
		TimeInForce: "0", OrderQty: decimal.NewFromInt(100), Price: decimal.NewFromFloat(25),
		CumQty: decimal.Zero, AvgPx: decimal.Zero,
	}
	gtc := &exchange.OrderState{
		ClOrdID: "GTC001", Symbol: "PETR4", Side: "1", OrdType: "2",
		TimeInForce: "1", OrderQty: decimal.NewFromInt(100), Price: decimal.NewFromFloat(24),
		CumQty: decimal.Zero, AvgPx: decimal.Zero,
	}
	if err := lab.Book.Insert(day); err != nil {
		t.Fatalf("insert day order: %v", err)
	}
	if err := lab.Book.Insert(gtc); err != nil {
		t.Fatalf("insert good-till order: %v", err)
	}

	lab.Exchange.CancelAllOpenDay("drill 10")

	after, _ := lab.Book.Get("DAY001")
	if after.Status != exchange.StatusCanceled {
		t.Errorf("day order status = %q, want cancelled", after.Status)
	}

	survivor, _ := lab.Book.Get("GTC001")
	if survivor.Status == exchange.StatusCanceled {
		t.Error("the good-till order was cancelled; it is meant to outlive the session")
	}
}
