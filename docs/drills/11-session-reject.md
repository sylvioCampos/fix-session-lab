# Drill 11 — Session-level Reject

**Teaches:** the two rejection layers and how to tell them apart, and the one
session type that must never reject anything.

## Run it

Rejects come from the engine, below your application, so producing one means
sending something deliberately wrong. The tests do this directly; there is no
admin endpoint for it, because a venue does not have a "be malformed" button.

```bash
go test ./internal/drills -run TestDrill11 -v
FIXLAB_DUMP_WIRE=1 go test ./internal/drills -run TestDrill11_SessionReject -v
```

## Wire trace

An order carrying a `Side` the dictionary does not define:

```
--> 8=FIX.4.4|35=D|34=2|49=OECLIENT|56=FIXLABEX|11=BAD00001|38=100|40=2|44=25.50|
    54=Z|55=PETR4|
     ▲
   not a defined value

<-- 8=FIX.4.4|35=3|34=2|49=FIXLABEX|56=OECLIENT|45=2|371=54|372=D|373=5|
    58=Value is incorrect (out of range) for this tag|
         ▲                ▲       ▲     ▲     ▲
  session Reject   RefSeqNum  RefTagID  RefMsgType  SessionRejectReason
```

The same order with a valid `Side` and no `Symbol` gets a different answer
entirely:

```
<-- 8=FIX.4.4|35=j|34=2|49=FIXLABEX|56=OECLIENT|45=2|372=D|380=5|
    58=Conditionally Required Field Missing (55)|
         ▲                                       ▲
  Business Message Reject              BusinessRejectReason
```

## What to notice

**Two layers, two answers, and the split is not where most people guess.** A
session Reject (`35=3`) means the engine could not process the message — a value
outside a tag's declared range, a tag that does not belong on that message type,
a sequence problem. A Business Message Reject (`35=j`) means the message was fine
at the session layer and was refused above it.

An out-of-range value is a session reject. A *missing conditionally-required
field* is a business reject, even though both are caught by the same dictionary
validation pass in the same engine. The reject reason decides which layer
answers, not your intuition about severity.

**Your application never sees either one.** By the time you could have an
opinion, the engine has already replied. You cannot implement leniency for a
malformed message in `FromApp`, because `FromApp` is not called. If you need to
accept something your dictionary rejects, the dictionary is where you fix it.

**A Reject does not end the session.** Sequence numbers advance, the session
stays up, the next valid message works normally. A client that tears down its
connection on a Reject is turning a message-level problem into an outage.

**Read `RefSeqNum (45)` and `RefTagID (371)`.** With several messages in flight,
a Reject that you cannot attribute to a specific message and tag is nearly
useless. These are the fields that make it actionable.

## The session that must never reject

A drop-copy session sends only session-level admin traffic — Logon, Heartbeat,
ResendRequest, Logout. It must never emit a Reject.

That is not a stylistic rule, and the application alone cannot enforce it:

```
RejectInvalidMessage=N   # drop copy
RejectInvalidMessage=Y   # order entry
```

With `Y`, the engine answers anything failing dictionary validation with a
`35=3` before your code runs. Your `FromApp` can be as disciplined as you like
and it will be bypassed. `N` keeps validation on, so anomalies are still logged,
and suppresses only the automatic reply.

This is not hypothetical, and it was not planned. Building drill 10, the venue
sent a cancel-on-disconnect report carrying `ExecRestatementReason=100` — a value
stock FIX 4.4 does not define. The drop-copy client rejected it and the report
was lost. Two real bugs in one message: the dictionary needed the value, and the
drop-copy session should never have replied at all. Venues do emit values their
own published specs omit, so plan for it.

## Never reject a reject

The rule that outranks everything else here, and the one this repo learned the
expensive way.

If your application answers unknown message types with a reject, and the
counterparty does the same, then the first `35=j` either side sends never stops.
You reject the rejection, they reject that, and the two of you ping-pong at wire
speed. The first version of this lab did exactly that: a single duplicate
`ClOrdID` after a client restart produced **thirteen thousand messages in six
seconds**, burned the sequence numbers on both sides, and left two stores full
of nothing but rejections.

```go
switch msgType {
case msgTypeExecutionReport:
    return c.onExecutionReport(...)
case msgTypeBusinessReject:
    c.Log.Printf("business reject from the venue: %s", rejectText(msg))
    return nil          // log it, never answer it
default:
    return quickfix.NewBusinessMessageRejectError(...)
}
```

Both sides need the fix. Repairing one end shortens the loop by a message and
leaves it running.

It is worth being precise about why the unit tests missed this. They call
`FromApp` directly with a hand-built message and assert on what comes back —
which verifies what one side *answers* and, by construction, cannot observe what
happens when the other side answers back. The loop needs two real applications
on a real socket. `internal/drills/reject_storm_test.go` is that test now.

## Versus real venues

- A venue that receives a Reject it did not expect may escalate — repeated
  rejects from a session attract attention, and some venues throttle or
  disconnect.
- Business rejects often carry venue-specific reason codes in `58` that are more
  informative than the standard `380` value. Log the whole message.
- Some venues answer an unacceptable *order* with an `ExecutionReport`
  (`ExecType=8`, Rejected) rather than a Business Message Reject. That is a
  third layer again: the order was understood and declined on its merits. Do not
  collapse it into the other two.

## The test

[`internal/drills/drill11_session_reject_test.go`](../../internal/drills/drill11_session_reject_test.go)
produces a session Reject and a Business Message Reject from near-identical
messages, checks each carries the fields that make it actionable, confirms
neither reached the venue's application, and confirms the session survives.

A further test asserts the drop-copy consumer stays silent when handed something
it does not expect — the invariant that the `ExecRestatementReason=100` incident
broke.
