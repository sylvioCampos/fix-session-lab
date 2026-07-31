# Drill 05 — Drop-copy fanout and deduplication

**Teaches:** what a drop-copy session is for, why it must be read-only, and why
`ExecID` is the field the whole thing hangs on.

## Run it

```bash
make up
docker compose logs -f dc-client

curl -XPOST 'localhost:8081/admin/fill?clordid=CL000001&px=25.50'
```

Every report the venue publishes appears twice in the logs: once on the
order-entry session, once on the drop-copy session.

## Wire trace

The same execution, sent on both sessions:

```
FIXLABEX->OECLIENT  -->  8=FIX.4.4|35=8|34=2|49=FIXLABEX|56=OECLIENT|
                         11=CL000001|14=0|17=EXE000001|37=ORD000001|39=0|150=0|151=100|
                                              ▲
FIXLABEX->DCCLIENT  -->  8=FIX.4.4|35=8|34=2|49=FIXLABEX|56=DCCLIENT|
                         11=CL000001|14=0|17=EXE000001|37=ORD000001|39=0|150=0|151=100|
                                              ▲
                                    same ExecID, different session
```

## What to notice

**`17=EXE000001` is identical on both sessions.** This is the property the whole
service depends on. The two feeds describe one event, and `ExecID` is what says
so. A venue that numbered its drop copy independently would hand you two
unrelated views of the same execution and no way to join them.

**`34=2` on both, but they are different sequence spaces.** Each session owns its
own numbering. That they happen to match here is coincidence — after any
independent traffic on one session, they diverge. Never reconcile across
sessions on `MsgSeqNum`; that is what `ExecID` is for.

**The drop-copy consumer deduplicates on `ExecID` and nothing else.** Not
`ClOrdID` (many reports per order), not `MsgSeqNum` (per-session, and reused
after a reset), not arrival order. `ExecID` is the only identifier that is
stable across a replay, which is what makes drill 07 survivable.

**The drop-copy client has no send path at all.** Not "it does not send" — it
*cannot*. A drop-copy session is read-only; the venue rejects application
messages on it. `internal/client/dc.go` has no `Send` method, which is a cheaper
guarantee than a code review.

**`RejectInvalidMessage=N` on the drop-copy config, `Y` on order entry.** This
looks like an inconsistency and is not. With `Y`, quickfixgo answers any message
failing dictionary validation with a session Reject (`35=3`) — an outbound
message on a session that must never send one. Venues do emit tags their own
specs do not document, so this is not hypothetical. `N` keeps validation on so
anomalies are still logged, and suppresses only the automatic reply.

## Why the venue is one process

The order-entry and drop-copy acceptor sessions live in the same process because
the fanout needs shared order state — the drop copy of an execution is generated
from the same `OrderState` that produced the order-entry report, at the same
instant, with the same `ExecID`.

Splitting them into separate services would require inventing a message bus
between them, and would teach an architecture no venue actually has.

## Versus real B3

- B3 runs several drop-copy sessions per segment, for matching-engine affinity;
  you need all of them connected for full coverage.
- A B3 drop copy carries the whole firm's flow, including orders sent from other
  systems — that is generally the reason to consume it.
- B3 resets drop-copy sequence numbers on its own schedule.
- Real drop-copy consumers persist deduplicated reports (`ON CONFLICT DO
  NOTHING` on `ExecID`) rather than holding them in memory as this lab does.

## The test

[`internal/drills/drill05_dropcopy_test.go`](../../internal/drills/drill05_dropcopy_test.go)
asserts the fanout reaches both sessions, that distinct events carry distinct
`ExecID`s, and that the same event carries the *same* `ExecID` on both sessions.

That last assertion earned its place: the first version of this lab generated
`ExecID` per session and the wire trace caught it.
