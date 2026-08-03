package exchange

import (
	"fmt"
	"time"

	"github.com/quickfixgo/enum"
	"github.com/quickfixgo/field"
	"github.com/quickfixgo/fix44/executionreport"
	"github.com/quickfixgo/fix44/newordersingle"
	"github.com/quickfixgo/quickfix"
	"github.com/quickfixgo/tag"
	"github.com/shopspring/decimal"

	"github.com/sylvioCampos/fix-session-lab/internal/session"
)

// report describes one execution report the venue wants to publish.
type report struct {
	order       OrderState
	execType    string
	lastQty     decimal.Decimal
	lastPx      decimal.Decimal
	text        string
	restatement int

	// execID is assigned once per event, not once per session. The drop copy
	// of an execution report must carry the same ExecID as the report sent on
	// the order-entry session — that identity is what lets a client reconcile
	// the two feeds against each other, and what makes ExecID deduplication
	// meaningful when a replay redelivers the same event on either session.
	execID string
}

// publish sends an execution report to the order-entry session and fans a copy
// out to the drop-copy session.
//
// The two are built separately rather than one message being sent twice: every
// session owns its own sequence numbers and header, so a quickfix.Message that
// has been sent on one session cannot be handed to another.
func (a *App) publish(r report) {
	a.mu.RLock()
	silenced := a.silenced
	a.mu.RUnlock()

	r.execID = a.book.NextExecID()

	if silenced {
		// Deliberate: the session stays logged on and the socket stays up.
		// This is what a stuck venue-side feed looks like from the client, and
		// no FIX engine will notice it for you.
		a.log.Printf("SILENCED: dropping %s report for %s", r.execType, r.order.ClOrdID)
		return
	}

	// Published to every configured session whether or not it is connected.
	//
	// That is not an oversight. A venue does not stop trading because a client
	// dropped, and the report still has to exist. quickfixgo assigns the
	// sequence number and writes the message to the store before it ever
	// touches a socket; if the session is down the bytes are discarded from the
	// send queue but the stored copy remains. The client's sequence number is
	// now behind, and on reconnect it asks for the range it missed — which is
	// exactly how a real gap forms and how it is recovered. Skipping the
	// publish would leave the numbering contiguous and there would be nothing
	// to recover. Drill 07.
	if oeID, ok := a.sessionOf(KindOrderEntry); ok {
		a.send(r, oeID)
	}
	if dcID, ok := a.sessionOf(KindDropCopy); ok {
		a.send(r, dcID)
	}
}

func (a *App) send(r report, sessionID quickfix.SessionID) {
	msg := a.buildExecutionReport(r)
	if err := quickfix.SendToTarget(msg, sessionID); err != nil {
		a.log.Printf("send to %s failed: %v", sessionID, err)
	}
}

func (a *App) buildExecutionReport(r report) executionreport.ExecutionReport {
	o := r.order

	msg := executionreport.New(
		field.NewOrderID(o.OrderID),
		field.NewExecID(r.execID),
		field.NewExecType(enum.ExecType(r.execType)),
		field.NewOrdStatus(enum.OrdStatus(o.Status)),
		field.NewSide(enum.Side(o.Side)),
		field.NewLeavesQty(o.LeavesQty(), 0),
		field.NewCumQty(o.CumQty, 0),
		field.NewAvgPx(o.AvgPx, 2),
	)

	msg.Set(field.NewClOrdID(o.ClOrdID))
	msg.Set(field.NewSymbol(o.Symbol))
	msg.Set(field.NewOrderQty(o.OrderQty, 0))
	msg.Set(field.NewTransactTime(time.Now().UTC()))

	if !o.Price.IsZero() {
		msg.Set(field.NewPrice(o.Price, 2))
	}
	if o.Account != "" {
		msg.Set(field.NewAccount(o.Account))
	}
	if r.execType == ExecTypeTrade {
		msg.Set(field.NewLastQty(r.lastQty, 0))
		msg.Set(field.NewLastPx(r.lastPx, 2))
	}
	if r.text != "" {
		msg.Set(field.NewText(r.text))
	}
	if r.restatement != 0 {
		// 378 tells the client the venue changed this order on its own. Without
		// it, a cancel the venue generated is indistinguishable from one the
		// client requested.
		msg.Body.SetField(tag.ExecRestatementReason, quickfix.FIXInt(r.restatement))
	}

	return msg
}

// onNewOrderSingle accepts an order and acknowledges it. Nothing else happens:
// fills, cancels and rejects are commanded through the admin API so that a
// drill produces the same bytes every time.
func (a *App) onNewOrderSingle(msg *quickfix.Message, sessionID quickfix.SessionID) quickfix.MessageRejectError {
	nos := newordersingle.FromMessage(msg)

	clOrdID, err := nos.GetClOrdID()
	if err != nil {
		return err
	}
	symbol, err := nos.GetSymbol()
	if err != nil {
		return err
	}
	side, err := nos.GetSide()
	if err != nil {
		return err
	}
	orderQty, err := nos.GetOrderQty()
	if err != nil {
		return err
	}
	ordType, err := nos.GetOrdType()
	if err != nil {
		return err
	}

	o := &OrderState{
		ClOrdID:  clOrdID,
		Symbol:   symbol,
		Side:     string(side),
		OrdType:  string(ordType),
		OrderQty: orderQty,
		CumQty:   decimal.Zero,
		AvgPx:    decimal.Zero,
	}
	if px, err := nos.GetPrice(); err == nil {
		o.Price = px
	}
	if tif, err := nos.GetTimeInForce(); err == nil {
		o.TimeInForce = string(tif)
	}
	if acct, err := nos.GetAccount(); err == nil {
		o.Account = acct
	}

	if err := a.book.Insert(o); err != nil {
		return quickfix.NewBusinessMessageRejectError(err.Error(), 5, nil)
	}

	a.log.Printf("accepted %s %s %s @ %s (%s)",
		o.ClOrdID, o.Side, o.OrderQty, o.Price, o.Symbol)

	a.publish(report{order: *o, execType: ExecTypeNew})
	return nil
}

// onSessionLost fires cancel-on-disconnect after the grace window.
//
// The window is the whole point: a client that reconnects inside it keeps its
// orders. Firing immediately would make every transient network blip a mass
// cancel, which is worse than the problem COD solves.
func (a *App) onSessionLost(sessionID quickfix.SessionID, req session.LogonRequest) {
	window := time.Duration(req.CODTimeoutWindow) * time.Millisecond
	a.log.Printf("cancel-on-disconnect: %s lost, waiting %s", sessionID, window)

	time.AfterFunc(window, func() {
		a.mu.RLock()
		back := a.loggedOn[sessionID]
		a.mu.RUnlock()

		if back {
			a.log.Printf("cancel-on-disconnect: %s reconnected in time, standing down", sessionID)
			return
		}
		a.CancelAllOpenDay(fmt.Sprintf("cancel on disconnect (%s)", sessionID))
	})
}

// CancelAllOpenDay cancels every working day order and reports each one with
// ExecRestatementReason set. Good-till orders survive, which is what venues
// implementing COD generally do.
func (a *App) CancelAllOpenDay(reason string) int {
	orders := a.book.OpenDayOrders()

	for _, o := range orders {
		updated, err := a.book.Update(o.ClOrdID, func(s *OrderState) error {
			s.Status = StatusCanceled
			return nil
		})
		if err != nil {
			a.log.Printf("cancel %s failed: %v", o.ClOrdID, err)
			continue
		}
		a.publish(report{
			order:       updated,
			execType:    ExecTypeCanceled,
			text:        reason,
			restatement: RestatementCancelOnDisconnect,
		})
	}

	a.log.Printf("cancelled %d open day order(s): %s", len(orders), reason)
	return len(orders)
}
