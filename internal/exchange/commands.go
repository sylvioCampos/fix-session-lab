package exchange

import (
	"fmt"
	"sort"

	"github.com/quickfixgo/quickfix"
	"github.com/shopspring/decimal"
)

// The methods here are the venue's commanded behavior — everything the admin
// API can make the exchange do. Keeping them on App rather than inside the HTTP
// handlers means the drills' integration tests drive exactly the same code path
// a reader drives with curl.

// Fill fully fills an order at px.
func (a *App) Fill(clOrdID string, px decimal.Decimal) error {
	o, ok := a.book.Get(clOrdID)
	if !ok {
		return fmt.Errorf("unknown ClOrdID %q", clOrdID)
	}
	return a.Partial(clOrdID, o.LeavesQty(), px)
}

// Partial fills qty of an order at px, moving it to PartiallyFilled or Filled.
func (a *App) Partial(clOrdID string, qty, px decimal.Decimal) error {
	if qty.LessThanOrEqual(decimal.Zero) {
		return fmt.Errorf("qty must be positive, got %s", qty)
	}

	updated, err := a.book.Update(clOrdID, func(o *OrderState) error {
		if !o.IsOpen() {
			return fmt.Errorf("order %q is %s and cannot trade", clOrdID, o.Status)
		}
		if qty.GreaterThan(o.LeavesQty()) {
			return fmt.Errorf("qty %s exceeds leaves %s", qty, o.LeavesQty())
		}

		// AvgPx is recomputed over the whole executed quantity, not just this
		// fill — a client reconciling on AvgPx alone will diverge otherwise.
		notional := o.AvgPx.Mul(o.CumQty).Add(px.Mul(qty))
		o.CumQty = o.CumQty.Add(qty)
		o.AvgPx = notional.Div(o.CumQty)

		if o.CumQty.Equal(o.OrderQty) {
			o.Status = StatusFilled
		} else {
			o.Status = StatusPartiallyFilled
		}
		return nil
	})
	if err != nil {
		return err
	}

	a.publish(report{
		order:    updated,
		execType: ExecTypeTrade,
		lastQty:  qty,
		lastPx:   px,
	})
	return nil
}

// Cancel cancels an order at the venue's initiative.
func (a *App) Cancel(clOrdID, reason string) error {
	updated, err := a.book.Update(clOrdID, func(o *OrderState) error {
		if !o.IsOpen() {
			return fmt.Errorf("order %q is %s and cannot be cancelled", clOrdID, o.Status)
		}
		o.Status = StatusCanceled
		return nil
	})
	if err != nil {
		return err
	}

	a.publish(report{order: updated, execType: ExecTypeCanceled, text: reason})
	return nil
}

// Reject rejects a working order.
func (a *App) Reject(clOrdID, reason string) error {
	updated, err := a.book.Update(clOrdID, func(o *OrderState) error {
		if !o.IsOpen() {
			return fmt.Errorf("order %q is %s and cannot be rejected", clOrdID, o.Status)
		}
		o.Status = StatusRejected
		return nil
	})
	if err != nil {
		return err
	}

	a.publish(report{order: updated, execType: ExecTypeRejected, text: reason})
	return nil
}

// SetRejectLogons makes subsequent Logons fail with reason. An empty reason
// restores normal behavior.
func (a *App) SetRejectLogons(reason string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.rejectLogons = reason
}

// SetSilenced stops the venue from emitting application traffic while leaving
// every session logged on and every socket up.
//
// This is the failure the session layer cannot see. Heartbeats keep flowing —
// quickfixgo generates those below the application — so the engine reports a
// perfectly healthy session while no business data arrives. Detecting it is the
// client's job, which is what the watchdog in internal/session exists for.
func (a *App) SetSilenced(v bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.silenced = v
}

// Silenced reports whether the venue is currently withholding traffic.
func (a *App) Silenced() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.silenced
}

// AdvanceSeqNum moves a session's outbound sequence number forward by n,
// creating a gap the counterparty must recover with a ResendRequest.
//
// This is the venue-side half of the gap drill. On a real venue the gap appears
// because the venue kept trading while you were disconnected; here it is one
// call, because quickfixgo exposes SetNextSenderMsgSeqNum publicly.
//
// Call it only while the session is quiet. quickfixgo's stores are not
// synchronized, so writing a sequence number from this goroutine while the
// session goroutine is processing a message is a data race. That is acceptable
// for a drill you drive by hand between messages; it is not a pattern to copy
// into anything that runs unattended.
func (a *App) AdvanceSeqNum(kind Kind, n int) (int, error) {
	if n <= 0 {
		return 0, fmt.Errorf("n must be positive, got %d", n)
	}

	sessionID, _ := a.sessionOf(kind)
	if sessionID == (quickfix.SessionID{}) {
		return 0, fmt.Errorf("no %s session configured", kind)
	}

	current, err := quickfix.GetExpectedSenderNum(sessionID)
	if err != nil {
		return 0, err
	}

	next := current + n
	if err := quickfix.SetNextSenderMsgSeqNum(sessionID, next); err != nil {
		return 0, err
	}

	a.log.Printf("advanced %s outbound seqnum %d -> %d", sessionID, current, next)
	return next, nil
}

// SessionStatus is what GET /admin/sessions reports.
type SessionStatus struct {
	SessionID  string  `json:"session_id"`
	Kind       Kind    `json:"kind"`
	LoggedOn   bool    `json:"logged_on"`
	SeqNums    SeqNums `json:"seqnums"`
	CODType    int     `json:"cod_type"`
	CODTimeout int     `json:"cod_timeout_window_ms"`
}

// Sessions reports the state of every configured session.
//
// The sequence numbers are the ones the venue observed on the wire, not ones
// read back from quickfixgo's store — see SeqNums for why that distinction
// matters.
func (a *App) Sessions() []SessionStatus {
	a.mu.RLock()
	defer a.mu.RUnlock()

	out := make([]SessionStatus, 0, len(a.kinds))
	for id, kind := range a.kinds {
		out = append(out, SessionStatus{
			SessionID:  id.String(),
			Kind:       kind,
			LoggedOn:   a.loggedOn[id],
			SeqNums:    a.seqnums[id],
			CODType:    a.cod[id].CODType,
			CODTimeout: a.cod[id].CODTimeoutWindow,
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].SessionID < out[j].SessionID })
	return out
}

// Orders returns every order the venue knows about, in insertion order.
func (a *App) Orders() []OrderState {
	return a.book.All()
}
