package exchange

// Order statuses (tag 39, OrdStatus) and execution types (tag 150, ExecType)
// used by the lab. FIX gives the two fields overlapping value spaces on
// purpose: ExecType says what just happened, OrdStatus says where the order
// stands now. Conflating them is the classic first-integration bug — a partial
// fill is ExecType=F with OrdStatus=1, not ExecType=1.
const (
	StatusNew             = "0"
	StatusPartiallyFilled = "1"
	StatusFilled          = "2"
	StatusCanceled        = "4"
	StatusRejected        = "8"

	ExecTypeNew      = "0"
	ExecTypeCanceled = "4"
	ExecTypeRejected = "8"
	ExecTypeTrade    = "F"
)

// TimeInForce values (tag 59) the lab distinguishes. Only the day/good-till
// split matters here, because it decides what cancel-on-disconnect touches.
const (
	TIFDay = "0"
	TIFGTC = "1"
)

// ExecRestatementReason (tag 378) values a venue sets on unsolicited reports so
// the client can tell why an order it did not touch just changed.
const (
	// RestatementCancelOnDisconnect marks a cancel the venue generated because
	// the session dropped, rather than one the client asked for. A client that
	// ignores this cannot distinguish its own cancel from the venue's.
	RestatementCancelOnDisconnect = 100
)
