package exchange

import (
	"fmt"
	"sync"

	"github.com/shopspring/decimal"
)

// OrderState is the venue's view of one order.
//
// The lab keeps this deliberately thin. A real venue models far more, but every
// extra field is a field a drill would have to explain, and the subject here is
// the session layer.
type OrderState struct {
	ClOrdID     string
	OrderID     string
	Account     string
	Symbol      string
	Side        string
	OrdType     string
	TimeInForce string
	Price       decimal.Decimal
	OrderQty    decimal.Decimal
	CumQty      decimal.Decimal
	AvgPx       decimal.Decimal
	Status      string
}

// LeavesQty is the quantity still working.
func (o *OrderState) LeavesQty() decimal.Decimal {
	if !o.IsOpen() {
		return decimal.Zero
	}
	return o.OrderQty.Sub(o.CumQty)
}

// IsOpen reports whether the order can still trade. Cancel-on-disconnect acts
// on exactly this set.
func (o *OrderState) IsOpen() bool {
	return o.Status == StatusNew || o.Status == StatusPartiallyFilled
}

// IsDay reports whether the order dies at the end of the session. Venues that
// implement cancel-on-disconnect generally spare good-till orders, so the
// distinction decides who gets cancelled in the COD drill.
func (o *OrderState) IsDay() bool {
	return o.TimeInForce == "" || o.TimeInForce == TIFDay
}

// Book holds the venue's orders.
//
// It is not a matching engine: nothing crosses and nothing fills on its own.
// Fills, partials, cancels and rejects are commanded through the admin API,
// which is what lets every drill reproduce byte for byte and lets CI assert on
// golden wire traces.
type Book struct {
	mu       sync.Mutex
	orders   map[string]*OrderState // keyed by ClOrdID
	sequence []*OrderState          // insertion order, for deterministic iteration
	orderSeq int
	execSeq  int
}

func NewBook() *Book {
	return &Book{orders: make(map[string]*OrderState)}
}

// Insert records a new order and assigns it a venue OrderID. A duplicate
// ClOrdID is an error, as it is on a real venue.
func (b *Book) Insert(o *OrderState) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if _, dup := b.orders[o.ClOrdID]; dup {
		return fmt.Errorf("duplicate ClOrdID %q", o.ClOrdID)
	}

	b.orderSeq++
	o.OrderID = fmt.Sprintf("ORD%06d", b.orderSeq)
	o.Status = StatusNew
	b.orders[o.ClOrdID] = o
	b.sequence = append(b.sequence, o)

	return nil
}

// Get returns a snapshot copy of an order by ClOrdID.
func (b *Book) Get(clOrdID string) (OrderState, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	o, ok := b.orders[clOrdID]
	if !ok {
		return OrderState{}, false
	}
	return *o, true
}

// Update applies fn to an order under the book's lock and returns the resulting
// state. Mutating through this method keeps every transition serialized against
// the fanout that reports it.
func (b *Book) Update(clOrdID string, fn func(*OrderState) error) (OrderState, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	o, ok := b.orders[clOrdID]
	if !ok {
		return OrderState{}, fmt.Errorf("unknown ClOrdID %q", clOrdID)
	}
	if err := fn(o); err != nil {
		return OrderState{}, err
	}
	return *o, nil
}

// OpenDayOrders returns the working non-good-till orders in insertion order.
// This is the set cancel-on-disconnect fires on.
func (b *Book) OpenDayOrders() []OrderState {
	b.mu.Lock()
	defer b.mu.Unlock()

	var out []OrderState
	for _, o := range b.sequence {
		if o.IsOpen() && o.IsDay() {
			out = append(out, *o)
		}
	}
	return out
}

// All returns every order in insertion order.
func (b *Book) All() []OrderState {
	b.mu.Lock()
	defer b.mu.Unlock()

	out := make([]OrderState, 0, len(b.sequence))
	for _, o := range b.sequence {
		out = append(out, *o)
	}
	return out
}

// NextExecID returns a monotonically increasing execution id. Drop-copy
// consumers deduplicate on it, so it must never repeat within a session.
func (b *Book) NextExecID() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.execSeq++
	return fmt.Sprintf("EXE%06d", b.execSeq)
}
