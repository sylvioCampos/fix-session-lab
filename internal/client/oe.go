// Package client holds the two initiator applications: an order-entry client
// that sends orders and tracks them, and a drop-copy consumer that only
// listens. They are separate types because they are separate jobs — one has a
// write path and order state, the other must never send an application message
// and must deduplicate what it receives.
package client

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/quickfixgo/enum"
	"github.com/quickfixgo/field"
	"github.com/quickfixgo/fix44/executionreport"
	"github.com/quickfixgo/fix44/newordersingle"
	"github.com/quickfixgo/quickfix"
	"github.com/shopspring/decimal"

	"github.com/sylvioCampos/fix-session-lab/internal/session"
)

// tagPossDupFlag marks a message as a replay of one already sent.
const tagPossDupFlag quickfix.Tag = 43

const (
	msgTypeExecutionReport = "8"
	msgTypeBusinessReject  = "j"
)

// rejectText pulls the human-readable reason off a reject, for logging.
func rejectText(msg *quickfix.Message) string {
	if s, err := msg.Body.GetString(quickfix.Tag(58)); err == nil {
		return s
	}
	return "(no Text)"
}

// OrderEntry is the order-entry client application.
type OrderEntry struct {
	*session.Client

	// prefix disambiguates ClOrdIDs between runs of this client.
	//
	// ClOrdID must be unique for the life of the venue's order book, not just
	// for the life of your process. The counter below lives in memory, so a
	// restart begins again at 1 while the venue still remembers the orders the
	// previous run sent — and answers the collision with a business reject.
	//
	// Tests leave this empty so their wire traces stay deterministic. The
	// shipped binary sets it per process; see cmd/oe-client.
	prefix string

	mu      sync.Mutex
	seq     int
	orders  map[string]OrderView
	replays int
}

// SetClOrdIDPrefix disambiguates this client's ClOrdIDs from those of previous
// runs. Call it before sending anything.
func (c *OrderEntry) SetClOrdIDPrefix(p string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prefix = p
}

// ResetClOrdIDSeq rewinds the ClOrdID counter, simulating a restarted process.
// It exists for the regression test that reproduces a duplicate-ClOrdID reject.
func (c *OrderEntry) ResetClOrdIDSeq() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq = 0
}

// OrderView is the client's own record of an order, built entirely from the
// execution reports it has received.
type OrderView struct {
	ClOrdID   string
	OrdStatus string
	CumQty    decimal.Decimal
	LeavesQty decimal.Decimal
	AvgPx     decimal.Decimal
	// LastExecID is the ExecID of the most recent report. The drop-copy feed
	// reports the same event under the same ExecID, which is how the two views
	// are reconciled against each other.
	LastExecID string
}

func NewOrderEntry(creds session.LogonCredentials, logger *log.Logger) *OrderEntry {
	return &OrderEntry{
		Client: session.NewClient(creds, logger),
		orders: make(map[string]OrderView),
	}
}

// FromApp handles inbound application messages. An order-entry session receives
// execution reports; anything else is unexpected and is rejected rather than
// ignored, so a venue sending something this client cannot handle finds out.
func (c *OrderEntry) FromApp(msg *quickfix.Message, sessionID quickfix.SessionID) quickfix.MessageRejectError {
	c.NoteInbound(sessionID, session.InboundApp)

	msgType, err := msg.Header.GetString(quickfix.Tag(35))
	if err != nil {
		return err
	}

	switch msgType {
	case msgTypeExecutionReport:
		return c.onExecutionReport(executionreport.FromMessage(msg))

	case msgTypeBusinessReject:
		// Never reject a reject.
		//
		// A BusinessMessageReject is not something to answer. Rejecting it
		// produces another reject, which the counterparty rejects in turn, and
		// the two sides ping-pong at wire speed until something falls over. The
		// first version of this client rejected everything that was not an
		// ExecutionReport, `35=j` included, and burned thirteen thousand
		// sequence numbers in six seconds the first time a venue refused an
		// order.
		//
		// The unit tests could not catch it: they call FromApp directly with a
		// hand-built message, so there was no counterparty to answer back. It
		// took two real applications on a real socket.
		c.Log.Printf("business reject from the venue: %s", rejectText(msg))
		return nil

	default:
		return quickfix.NewBusinessMessageRejectError(
			fmt.Sprintf("unsupported message type %q", msgType), 3, nil)
	}
}

func (c *OrderEntry) onExecutionReport(er executionreport.ExecutionReport) quickfix.MessageRejectError {
	clOrdID, err := er.GetClOrdID()
	if err != nil {
		return err
	}
	execType, err := er.GetExecType()
	if err != nil {
		return err
	}
	ordStatus, err := er.GetOrdStatus()
	if err != nil {
		return err
	}
	cumQty, err := er.GetCumQty()
	if err != nil {
		return err
	}
	leavesQty, err := er.GetLeavesQty()
	if err != nil {
		return err
	}
	avgPx, err := er.GetAvgPx()
	if err != nil {
		return err
	}
	execID, err := er.GetExecID()
	if err != nil {
		return err
	}

	// PossDupFlag=Y means the venue is replaying a report it already sent. A
	// client that treats a replayed fill as a new fill double-counts its
	// position, which is the most expensive mistake available in FIX recovery.
	// Cumulative fields make this survivable: CumQty is a running total, so
	// assigning it is idempotent where adding LastQty would not be.
	replayed := false
	if v, err := er.Header.GetBool(tagPossDupFlag); err == nil && v {
		replayed = true
	}

	c.mu.Lock()
	c.orders[clOrdID] = OrderView{
		ClOrdID:    clOrdID,
		OrdStatus:  string(ordStatus),
		CumQty:     cumQty,
		LeavesQty:  leavesQty,
		AvgPx:      avgPx,
		LastExecID: execID,
	}
	if replayed {
		c.replays++
	}
	c.mu.Unlock()

	c.Log.Printf("ER %-8s execType=%s ordStatus=%s cum=%s leaves=%s%s",
		clOrdID, execType, ordStatus, cumQty, leavesQty, replayNote(replayed))

	return nil
}

func replayNote(replayed bool) string {
	if replayed {
		return "  [43=Y replay — assign, do not accumulate]"
	}
	return ""
}

// NewOrder describes an order to send.
type NewOrder struct {
	Symbol   string
	Side     enum.Side
	OrderQty decimal.Decimal
	Price    decimal.Decimal
	Account  string
}

// Send sends a NewOrderSingle and returns the ClOrdID it generated.
func (c *OrderEntry) Send(sessionID quickfix.SessionID, o NewOrder) (string, error) {
	c.mu.Lock()
	c.seq++
	clOrdID := fmt.Sprintf("CL%s%06d", c.prefix, c.seq)
	c.mu.Unlock()

	msg := newordersingle.New(
		field.NewClOrdID(clOrdID),
		field.NewSide(o.Side),
		field.NewTransactTime(time.Now().UTC()),
		field.NewOrdType(enum.OrdType_LIMIT),
	)
	msg.Set(field.NewSymbol(o.Symbol))
	msg.Set(field.NewOrderQty(o.OrderQty, 0))
	msg.Set(field.NewPrice(o.Price, 2))
	msg.Set(field.NewTimeInForce(enum.TimeInForce_DAY))
	if o.Account != "" {
		msg.Set(field.NewAccount(o.Account))
	}

	if err := quickfix.SendToTarget(msg, sessionID); err != nil {
		return "", err
	}

	c.Log.Printf("sent %s: %s %s %s @ %s", clOrdID, o.Side, o.OrderQty, o.Symbol, o.Price)
	return clOrdID, nil
}

// ReplayCount is how many reports arrived carrying PossDupFlag=Y.
//
// Worth exposing rather than merely logging: after a recovery it is the only
// evidence that a replay actually happened. A recovery test that passes with a
// replay count of zero has proved nothing.
func (c *OrderEntry) ReplayCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.replays
}

// Order returns the client's view of an order.
func (c *OrderEntry) Order(clOrdID string) (OrderView, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.orders[clOrdID]
	return v, ok
}
