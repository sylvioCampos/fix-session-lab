package client

import (
	"log"
	"sync"

	"github.com/quickfixgo/fix44/executionreport"
	"github.com/quickfixgo/quickfix"

	"github.com/sylvioCampos/fix-session-lab/internal/session"
)

// DropCopy is the drop-copy consumer.
//
// Two invariants define it. It never sends an application message — a drop-copy
// session is read-only, and a venue will reject anything else. And it
// deduplicates on ExecID, because a resend after a gap replays reports it has
// already seen, and a consumer that counts them twice reports a position that
// never existed.
type DropCopy struct {
	*session.Client

	mu      sync.Mutex
	seen    map[string]struct{}
	reports []Report
	dupes   int
}

// Report is one execution report as the drop-copy consumer recorded it.
type Report struct {
	ExecID    string
	ClOrdID   string
	OrderID   string
	ExecType  string
	OrdStatus string
	Replayed  bool
}

func NewDropCopy(creds session.LogonCredentials, logger *log.Logger) *DropCopy {
	return &DropCopy{
		Client: session.NewClient(creds, logger),
		seen:   make(map[string]struct{}),
	}
}

// FromApp records execution reports, discarding ones already seen.
func (c *DropCopy) FromApp(msg *quickfix.Message, sessionID quickfix.SessionID) quickfix.MessageRejectError {
	c.NoteInbound(sessionID)

	msgType, err := msg.Header.GetString(quickfix.Tag(35))
	if err != nil {
		return err
	}
	if msgType != "8" {
		// Deliberately not a reject. A drop-copy session must send only
		// session-level admin traffic; emitting a business reject would break
		// that invariant. Logging keeps the anomaly visible without answering.
		c.Log.Printf("ignoring unexpected message type %q on drop copy", msgType)
		return nil
	}

	return c.onExecutionReport(executionreport.FromMessage(msg))
}

func (c *DropCopy) onExecutionReport(er executionreport.ExecutionReport) quickfix.MessageRejectError {
	execID, err := er.GetExecID()
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
	orderID, err := er.GetOrderID()
	if err != nil {
		return err
	}

	clOrdID, _ := er.GetClOrdID()

	replayed := false
	if v, err := er.Header.GetBool(tagPossDupFlag); err == nil && v {
		replayed = true
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, dup := c.seen[execID]; dup {
		c.dupes++
		c.Log.Printf("DUP  execID=%s ignored (already recorded)", execID)
		return nil
	}

	c.seen[execID] = struct{}{}
	c.reports = append(c.reports, Report{
		ExecID:    execID,
		ClOrdID:   clOrdID,
		OrderID:   orderID,
		ExecType:  string(execType),
		OrdStatus: string(ordStatus),
		Replayed:  replayed,
	})

	c.Log.Printf("DC   execID=%s clOrdID=%-8s execType=%s ordStatus=%s%s",
		execID, clOrdID, execType, ordStatus, replayNote(replayed))

	return nil
}

// Reports returns every distinct report received, in arrival order.
func (c *DropCopy) Reports() []Report {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Report(nil), c.reports...)
}

// Duplicates returns how many reports were discarded as already seen.
func (c *DropCopy) Duplicates() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dupes
}
