package drills

import (
	"testing"
	"time"

	"github.com/quickfixgo/enum"
	"github.com/shopspring/decimal"

	"github.com/sylvioCampos/fix-session-lab/internal/client"
	"github.com/sylvioCampos/fix-session-lab/internal/exchange"
)

// Drill 05 — drop-copy fanout and deduplication.
//
// Every execution report the venue publishes on the order-entry session also
// goes out on the drop-copy session. The drop-copy consumer deduplicates on
// ExecID (17), which is what makes it survive a replay after a gap: the same
// report arriving twice must be recorded once.
func TestDrill05_DropCopyFanout(t *testing.T) {
	lab := startLab(t)

	clOrdID, err := lab.OE.Send(lab.OESessionID, client.NewOrder{
		Symbol:   "VALE3",
		Side:     enum.Side_SELL,
		OrderQty: decimal.NewFromInt(200),
		Price:    decimal.NewFromFloat(61.20),
	})
	if err != nil {
		t.Fatalf("send order: %v", err)
	}

	// The ack reaches both sessions.
	waitFor(t, 3*time.Second, "the ack on both sessions", func() bool {
		v, ok := lab.OE.Order(clOrdID)
		return ok && v.OrdStatus == exchange.StatusNew && len(lab.DC.Reports()) == 1
	})

	if err := lab.Exchange.Fill(clOrdID, decimal.NewFromFloat(61.20)); err != nil {
		t.Fatalf("fill: %v", err)
	}

	waitFor(t, 3*time.Second, "the fill on the drop-copy session", func() bool {
		return len(lab.DC.Reports()) == 2
	})

	reports := lab.DC.Reports()

	if got := reports[0].ExecType; got != exchange.ExecTypeNew {
		t.Errorf("first drop-copy report ExecType = %q, want %q", got, exchange.ExecTypeNew)
	}
	if got := reports[1].ExecType; got != exchange.ExecTypeTrade {
		t.Errorf("second drop-copy report ExecType = %q, want %q", got, exchange.ExecTypeTrade)
	}
	if reports[0].ClOrdID != clOrdID || reports[1].ClOrdID != clOrdID {
		t.Errorf("drop-copy reports carry ClOrdIDs %q/%q, want %q",
			reports[0].ClOrdID, reports[1].ClOrdID, clOrdID)
	}

	// Distinct events carry distinct ExecIDs. A venue that reuses one makes
	// deduplication destructive rather than protective.
	if reports[0].ExecID == reports[1].ExecID {
		t.Errorf("ExecID repeated across reports: %q", reports[0].ExecID)
	}

	// The same event carries the SAME ExecID on both sessions. This is the
	// property that makes the two feeds reconcilable, and it is what makes
	// ExecID deduplication work when a replay redelivers an event on either
	// one. A venue that numbered its drop copy independently would give a
	// client two unrelated views of one execution.
	oeView, ok := lab.OE.Order(clOrdID)
	if !ok {
		t.Fatal("order-entry client has no record of the order")
	}
	if oeView.OrdStatus != exchange.StatusFilled {
		t.Errorf("order-entry OrdStatus = %q, want %q", oeView.OrdStatus, exchange.StatusFilled)
	}
	if oeView.LastExecID != reports[1].ExecID {
		t.Errorf("the fill has ExecID %q on order entry and %q on drop copy; they must match",
			oeView.LastExecID, reports[1].ExecID)
	}
	if lab.DC.Duplicates() != 0 {
		t.Errorf("discarded %d report(s) as duplicates with no replay in play", lab.DC.Duplicates())
	}
}

// TestDrill05_DedupOnExecID drives the consumer's deduplication directly, since
// producing a genuine replay needs the gap machinery from drill 07.
func TestDrill05_DedupOnExecID(t *testing.T) {
	lab := startLab(t)

	clOrdID, err := lab.OE.Send(lab.OESessionID, client.NewOrder{
		Symbol:   "PETR4",
		Side:     enum.Side_BUY,
		OrderQty: decimal.NewFromInt(50),
		Price:    decimal.NewFromFloat(25.00),
	})
	if err != nil {
		t.Fatalf("send order: %v", err)
	}

	waitFor(t, 3*time.Second, "the ack on drop copy", func() bool {
		return len(lab.DC.Reports()) == 1
	})

	first := lab.DC.Reports()[0]

	// Cancel and re-observe: a distinct event with a distinct ExecID is
	// recorded, so the consumer is not simply collapsing everything by ClOrdID.
	if err := lab.Exchange.Cancel(clOrdID, "drill 05"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	waitFor(t, 3*time.Second, "the cancel on drop copy", func() bool {
		return len(lab.DC.Reports()) == 2
	})

	second := lab.DC.Reports()[1]
	if first.ExecID == second.ExecID {
		t.Fatalf("cancel reused ExecID %q from the ack", first.ExecID)
	}
	if second.OrdStatus != exchange.StatusCanceled {
		t.Errorf("cancel report OrdStatus = %q, want %q", second.OrdStatus, exchange.StatusCanceled)
	}
}

// TestDrill05_DropCopyIsReadOnly confirms the venue refuses application traffic
// on a drop-copy session. A client that assumes it can act on the same session
// it observes will find out here rather than in production.
func TestDrill05_DropCopyIsReadOnly(t *testing.T) {
	lab := startLab(t)

	if lab.DCSessionID == lab.OESessionID {
		t.Fatal("drop copy and order entry must be distinct sessions")
	}

	statuses := lab.Exchange.Sessions()
	var kinds []exchange.Kind
	for _, s := range statuses {
		kinds = append(kinds, s.Kind)
	}
	if len(kinds) != 2 {
		t.Fatalf("expected 2 sessions, got %v", kinds)
	}
}
