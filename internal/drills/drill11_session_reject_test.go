package drills

import (
	"strings"
	"testing"
	"time"

	"github.com/quickfixgo/enum"
	"github.com/quickfixgo/field"
	"github.com/quickfixgo/fix44/newordersingle"
	"github.com/quickfixgo/quickfix"
	"github.com/shopspring/decimal"
)

// Drill 11 — session-level Reject.
//
// A Reject (35=3) is the engine saying "I could not process that message" —
// a missing required field, a value outside a tag's declared range, a tag that
// does not belong on that message type. It happens below your application: by
// the time you could have an opinion, the engine has already answered.
//
// That automatic answer is usually what you want, and on one kind of session it
// is a bug.
func TestDrill11_SessionReject(t *testing.T) {
	lab := startLab(t, withoutDropCopy())

	// A well-formed NewOrderSingle carrying a Side the dictionary does not
	// define. The problem is with the field itself, so the engine answers at
	// the session layer.
	msg := newordersingle.New(
		field.NewClOrdID("BAD00001"),
		field.NewSide(enum.Side("Z")),
		field.NewTransactTime(time.Now().UTC()),
		field.NewOrdType(enum.OrdType_LIMIT),
	)
	msg.Set(field.NewSymbol("PETR4"))
	msg.Set(field.NewOrderQty(decimal.NewFromInt(100), 0))
	msg.Set(field.NewPrice(decimal.NewFromFloat(25.50), 2))

	if err := quickfix.SendToTarget(msg, lab.OESessionID); err != nil {
		t.Fatalf("send invalid order: %v", err)
	}

	clientView := func() string {
		return normalizeWire(lab.Wire.String(), "FIX.4.4:OECLIENT->FIXLABEX")
	}

	waitFor(t, 5*time.Second, "a session-level Reject", func() bool {
		return strings.Contains(clientView(), "<-- 8=FIX.4.4|35=3")
	})

	view := clientView()

	// A Reject says which message failed and which tag caused it. Without
	// RefSeqNum you cannot attribute it when several messages are in flight.
	if !strings.Contains(view, "45=") {
		t.Error("Reject carried no RefSeqNum (45); the rejection cannot be attributed")
	}
	if !strings.Contains(view, "371=54") {
		t.Error("expected RefTagID (371) to point at Side (54)")
	}

	// The venue's application never saw it — the engine answered first, which
	// is why you cannot implement leniency for this in FromApp.
	if len(lab.Book.All()) != 0 {
		t.Error("a message the engine rejected still reached the venue's order book")
	}

	// A Reject does not kill the session.
	clOrdID := sendOrder(t, lab, "PETR4", decimal.NewFromInt(100), decimal.NewFromFloat(25.50))
	waitFor(t, 5*time.Second, "the session to keep working after a Reject", func() bool {
		_, ok := lab.OE.Order(clOrdID)
		return ok
	})
}

// TestDrill11_MissingFieldIsABusinessReject shows the other half of the split,
// and it is not the one most people expect.
//
// A missing conditionally-required field on an application message does not
// produce a session Reject. quickfixgo answers with a Business Message Reject
// (35=j) carrying BusinessRejectReason (380), because the message was fine at
// the session layer and unacceptable at the application layer. Same engine,
// same validation pass, different answer — decided entirely by which reject
// reason the failure maps to.
func TestDrill11_MissingFieldIsABusinessReject(t *testing.T) {
	lab := startLab(t, withoutDropCopy())

	// Same order, valid Side, no Symbol.
	msg := newordersingle.New(
		field.NewClOrdID("BAD00002"),
		field.NewSide(enum.Side_BUY),
		field.NewTransactTime(time.Now().UTC()),
		field.NewOrdType(enum.OrdType_LIMIT),
	)
	msg.Set(field.NewOrderQty(decimal.NewFromInt(100), 0))
	msg.Set(field.NewPrice(decimal.NewFromFloat(25.50), 2))

	if err := quickfix.SendToTarget(msg, lab.OESessionID); err != nil {
		t.Fatalf("send incomplete order: %v", err)
	}

	clientView := func() string {
		return normalizeWire(lab.Wire.String(), "FIX.4.4:OECLIENT->FIXLABEX")
	}

	waitFor(t, 5*time.Second, "a Business Message Reject", func() bool {
		return strings.Contains(clientView(), "<-- 8=FIX.4.4|35=j")
	})

	view := clientView()

	if strings.Contains(view, "<-- 8=FIX.4.4|35=3") {
		t.Error("a missing conditionally-required field produced a session Reject; " +
			"it belongs at the business layer")
	}
	if !strings.Contains(view, "380=") {
		t.Error("Business Message Reject carried no BusinessRejectReason (380)")
	}
	if !strings.Contains(view, "372=D") {
		t.Error("expected RefMsgType (372) naming the rejected message type")
	}
}

// TestDrill11_DropCopyMustNotReject is the case where the automatic Reject is
// the bug.
//
// A drop-copy session sends only session-level admin traffic. With
// RejectInvalidMessage=Y an engine answers anything failing dictionary
// validation with a 35=3, breaking that invariant the moment the venue emits a
// value your dictionary does not know — which venues do, including values their
// own published specs omit.
//
// This is not hypothetical. Building this lab, the venue sent a
// cancel-on-disconnect report carrying ExecRestatementReason=100, a value stock
// FIX 4.4 does not define. The drop-copy client answered with a Reject and the
// report was lost. Both halves were real bugs: the dictionary needed the value,
// and the drop-copy session should never have replied at all.
func TestDrill11_DropCopyMustNotReject(t *testing.T) {
	lab := startLab(t)

	// Hand the drop-copy consumer a message type it does not expect. It must
	// record the anomaly and stay silent rather than answer.
	unexpected := quickfix.NewMessage()
	unexpected.Header.SetField(quickfix.Tag(35), quickfix.FIXString("B")) // News

	if rej := lab.DC.FromApp(unexpected, lab.DCSessionID); rej != nil {
		t.Errorf("the drop-copy consumer answered with %v; a read-only session "+
			"must not emit application or session rejects", rej)
	}

	// And the shipped configuration backs that up rather than relying on the
	// application alone: an engine-level auto-reject would bypass the code
	// above entirely.
	if !strings.Contains(rejectInvalid(dcCompID), "N") {
		t.Error("the drop-copy session is configured to auto-reject invalid messages")
	}
	if !strings.Contains(rejectInvalid(oeCompID), "Y") {
		t.Error("the order-entry session should auto-reject invalid messages")
	}
}

// TestDrill11_BusinessRejectIsDifferent separates the two rejection layers,
// which are routinely confused.
//
// A session Reject (35=3) means the message was malformed. A Business Message
// Reject (35=j) means it was well formed and the application refused it. They
// come from different layers, carry different fields, and a client that treats
// them alike will retry things it should not.
func TestDrill11_BusinessRejectIsDifferent(t *testing.T) {
	lab := startLab(t, withoutDropCopy())

	// Well formed, dictionary-valid, and something the venue's application
	// declines: an order-entry session that receives a News message.
	news := quickfix.NewMessage()
	news.Header.SetField(quickfix.Tag(35), quickfix.FIXString("B"))

	rej := lab.OE.FromApp(news, lab.OESessionID)
	if rej == nil {
		t.Fatal("the order-entry client accepted a message type it cannot handle")
	}
	if !rej.IsBusinessReject() {
		t.Errorf("expected a business reject for an unsupported message type, got %T", rej)
	}
}
