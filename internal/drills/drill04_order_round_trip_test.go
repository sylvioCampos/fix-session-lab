package drills

import (
	"testing"
	"time"

	"github.com/quickfixgo/enum"
	"github.com/shopspring/decimal"

	"github.com/sylvioCampos/fix-session-lab/internal/client"
	"github.com/sylvioCampos/fix-session-lab/internal/exchange"
)

// Drill 04 — order round trip.
//
// One order, acknowledged, partially filled, then filled. The point is the
// relationship between ExecType (150) and OrdStatus (39): the first says what
// just happened, the second says where the order now stands. A partial fill is
// ExecType=F with OrdStatus=1, and reading either one alone gets you a wrong
// position.
func TestDrill04_OrderRoundTrip(t *testing.T) {
	lab := startLab(t)

	clOrdID, err := lab.OE.Send(lab.OESessionID, client.NewOrder{
		Symbol:   "PETR4",
		Side:     enum.Side_BUY,
		OrderQty: decimal.NewFromInt(100),
		Price:    decimal.NewFromFloat(25.50),
		Account:  "ACC1",
	})
	if err != nil {
		t.Fatalf("send order: %v", err)
	}

	// Acknowledgement: ExecType=0 (New), OrdStatus=0 (New), nothing traded.
	waitFor(t, 3*time.Second, "the order acknowledgement", func() bool {
		v, ok := lab.OE.Order(clOrdID)
		return ok && v.OrdStatus == exchange.StatusNew
	})

	ack, _ := lab.OE.Order(clOrdID)
	if !ack.CumQty.IsZero() {
		t.Errorf("CumQty on ack = %s, want 0", ack.CumQty)
	}
	if !ack.LeavesQty.Equal(decimal.NewFromInt(100)) {
		t.Errorf("LeavesQty on ack = %s, want 100", ack.LeavesQty)
	}

	// Partial fill of 40 at 25.50.
	if err := lab.Exchange.Partial(clOrdID, decimal.NewFromInt(40), decimal.NewFromFloat(25.50)); err != nil {
		t.Fatalf("partial fill: %v", err)
	}

	waitFor(t, 3*time.Second, "the partial fill", func() bool {
		v, ok := lab.OE.Order(clOrdID)
		return ok && v.OrdStatus == exchange.StatusPartiallyFilled
	})

	partial, _ := lab.OE.Order(clOrdID)
	if !partial.CumQty.Equal(decimal.NewFromInt(40)) {
		t.Errorf("CumQty after partial = %s, want 40", partial.CumQty)
	}
	if !partial.LeavesQty.Equal(decimal.NewFromInt(60)) {
		t.Errorf("LeavesQty after partial = %s, want 60", partial.LeavesQty)
	}

	// Fill the remainder at a different price, so AvgPx has to be a real
	// weighted average rather than the last price.
	if err := lab.Exchange.Fill(clOrdID, decimal.NewFromFloat(25.60)); err != nil {
		t.Fatalf("fill: %v", err)
	}

	waitFor(t, 3*time.Second, "the final fill", func() bool {
		v, ok := lab.OE.Order(clOrdID)
		return ok && v.OrdStatus == exchange.StatusFilled
	})

	final, _ := lab.OE.Order(clOrdID)
	if !final.CumQty.Equal(decimal.NewFromInt(100)) {
		t.Errorf("CumQty when filled = %s, want 100", final.CumQty)
	}
	if !final.LeavesQty.IsZero() {
		t.Errorf("LeavesQty when filled = %s, want 0", final.LeavesQty)
	}

	// (40 * 25.50 + 60 * 25.60) / 100 = 25.56
	wantAvg := decimal.NewFromFloat(25.56)
	if !final.AvgPx.Round(2).Equal(wantAvg) {
		t.Errorf("AvgPx = %s, want %s", final.AvgPx.Round(2), wantAvg)
	}
}

// TestDrill04_RejectsUnknownOrder confirms the venue refuses to act on an order
// it has never seen, rather than inventing one.
func TestDrill04_RejectsUnknownOrder(t *testing.T) {
	lab := startLab(t)

	err := lab.Exchange.Fill("CL999999", decimal.NewFromFloat(10))
	if err == nil {
		t.Fatal("filling an unknown ClOrdID should fail")
	}
}
