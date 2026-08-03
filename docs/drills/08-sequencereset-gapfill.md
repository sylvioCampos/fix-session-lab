# Drill 08 — SequenceReset and GapFill

**Teaches:** why not everything in a resend range gets replayed, and the one
detail about `PossDupFlag` that will strand your session if you get it wrong.

A resend covers a range of sequence numbers. Some of what occupied that range
was administrative — a Logon, a Heartbeat, a TestRequest — and replaying it
would be meaningless at best. A Heartbeat from four minutes ago proves nothing,
and an old Logon is actively confusing.

So the venue does not send them. It sends a `SequenceReset` saying "skip to N",
and the receiver's expected sequence number jumps without any message arriving.

## Run it

`/admin/gap` moves the venue's outbound sequence number without generating any
messages, so the skipped range contains nothing that *can* be replayed and the
whole resend collapses into a single gap fill.

```bash
make up
docker compose stop oe-client

curl -XPOST 'localhost:8081/admin/gap?n=5&kind=ORDER_ENTRY'

docker compose start oe-client
docker compose logs -f oe-client
```

> `/admin/gap` writes quickfixgo's sequence number from outside the session
> goroutine, and the stores carry no locking. Use it while the session is
> disconnected, as above. It is a drill tool, not a pattern to copy.

## Wire trace

```
--> 8=FIX.4.4|35=A|34=4|49=OECLIENT|56=FIXLABEX|98=0|108=30|
<-- 8=FIX.4.4|35=A|34=9|49=FIXLABEX|56=OECLIENT|98=0|108=30|
                     ▲
              five numbers ahead of what the client expected

--> 8=FIX.4.4|35=2|34=5|49=OECLIENT|56=FIXLABEX|7=4|16=0|
                                                 ▲   ▲
                                        BeginSeqNo  EndSeqNo=0 = "everything"

<-- 8=FIX.4.4|35=4|34=4|43=Y|49=FIXLABEX|56=OECLIENT|36=10|123=Y|
                 ▲     ▲                              ▲     ▲
       SequenceReset  PossDupFlag=Y            NewSeqNo  GapFillFlag=Y
```

One message replaces five. Nothing was retransmitted, because nothing in that
range existed.

## What to notice

**`123=Y` distinguishes a gap fill from a sequence reset.** Same message type,
two meanings. `GapFillFlag=Y` means "I am skipping messages during a resend" and
is a routine part of recovery. `GapFillFlag=N` — or absent — means "reset your
counter to `NewSeqNo` regardless", which is an administrative intervention that
can silently discard messages neither side ever accounts for. Treating them
alike is how a recovery quietly loses data.

**`36=NewSeqNo` is where to resume, not how many were skipped.** Off-by-one here
puts the session permanently out of step: every subsequent message looks like a
gap, the client asks for a resend, and gets another gap fill.

**The SequenceReset itself carries `43=Y`,** and this surprises people. It is
administrative housekeeping standing in for messages that were never delivered,
so it is flagged as a possible duplicate even though the receiver has certainly
never seen it.

> A client that treats `43=Y` as "already handled this, ignore it" will ignore
> the very message telling it where to resume, and then never resynchronise. The
> flag means "you may have seen this before" — it does not mean "safe to
> discard".

**Administrative messages are never replayed, only filled over.** In drill 07
the same thing happens at the end of a real recovery: three ExecutionReports are
retransmitted, then a single gap fill covers the Logon that occupied the last
slot in the range. Mixed ranges are the normal case; this drill just isolates
the pure one.

## Versus real venues

- A venue may chunk a large resend into several ranges, each with its own gap
  fills, when the request exceeds its limit — B3 rejects anything over 10,000
  messages.
- Some venues send `SequenceReset` with `GapFillFlag=N` to force resynchronisation
  after an operational problem. That is not recovery; it is an instruction to
  abandon the gap, and whatever was in it is gone.
- Real ranges mix replayed application messages and gap fills freely. Do not
  assume a resend is one or the other.

## The test

[`internal/drills/drill08_sequencereset_test.go`](../../internal/drills/drill08_sequencereset_test.go)
opens a gap containing nothing replayable, asserts the resend comes back as a
single `35=4` with `123=Y` and a `36=` to resume at, asserts nothing arrived
carrying real data, and confirms the session works normally afterwards.

A second test asserts the gap fill carries `43=Y` — the detail that strands a
client which treats that flag as permission to ignore a message.
