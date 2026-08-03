# Drill 07 — Gap and ResendRequest

**Teaches:** how a session recovers messages it never received, and why
`PossDupFlag=Y` is the difference between a correct position and a doubled one.

This is the drill the lab exists for. Everything else is scaffolding around it.

The situation is ordinary: your process dies, or the network blips, and the
venue keeps trading. Reports are generated that you never see. When you come
back you are behind, and the protocol has to close that distance without
inventing or duplicating anything.

## Run it

```bash
make up

# Send an order and let it be acknowledged
curl -s localhost:8081/admin/orders | jq '.[-1].ClOrdID'

# Kill the client. The venue is untouched.
docker compose stop oe-client

# The venue keeps working while nobody is listening
curl -XPOST 'localhost:8081/admin/partial?clordid=CL000001&qty=30&px=25.50'
curl -XPOST 'localhost:8081/admin/partial?clordid=CL000001&qty=30&px=25.60'
curl -XPOST 'localhost:8081/admin/fill?clordid=CL000001&px=25.70'

# Bring it back and watch
docker compose start oe-client
docker compose logs -f oe-client
```

Two settings make this work, and both are already in `config/oe-client.cfg`:

- `ResetOnLogon=N` — `Y` would reset the sequence numbers on reconnect, which
  erases the gap rather than recovering it. Drill 06.
- `FileStorePath` on a volume — a store that dies with the process cannot tell
  you that you missed anything.

## Wire trace

From the client's side. This is the committed golden trace, minus timestamps.

```
--> 8=FIX.4.4|35=A|34=1|49=OECLIENT|56=FIXLABEX|98=0|108=30|
<-- 8=FIX.4.4|35=A|34=1|49=FIXLABEX|56=OECLIENT|98=0|108=30|
--> 8=FIX.4.4|35=D|34=2|…|11=CL000001|38=100|44=25.50|54=1|55=PETR4|59=0|
<-- 8=FIX.4.4|35=8|34=2|…|11=CL000001|14=0|17=EXE000001|39=0|150=0|151=100|

    ── the client stops here ──────────────────────────────────────────
--> 8=FIX.4.4|35=5|34=3|49=OECLIENT|56=FIXLABEX|          Logout
<-- 8=FIX.4.4|35=5|34=3|49=FIXLABEX|56=OECLIENT|

    ── while it is away the venue sends 34=4, 34=5, 34=6 to nobody ────

--> 8=FIX.4.4|35=A|34=4|49=OECLIENT|56=FIXLABEX|98=0|108=30|
<-- 8=FIX.4.4|35=A|34=7|49=FIXLABEX|56=OECLIENT|98=0|108=30|
                     ▲
              venue is at 7; the client expected 4 — three messages missing

--> 8=FIX.4.4|35=2|34=5|49=OECLIENT|56=FIXLABEX|7=4|16=0|
                 ▲                               ▲   ▲
          ResendRequest                  BeginSeqNo  EndSeqNo=0 ("everything")

<-- 8=FIX.4.4|35=8|34=4|43=Y|…|14=30 |17=EXE000002|31=25.50|32=30|39=1|150=F|151=70|
<-- 8=FIX.4.4|35=8|34=5|43=Y|…|14=60 |17=EXE000003|31=25.60|32=30|39=1|150=F|151=40|
<-- 8=FIX.4.4|35=8|34=6|43=Y|…|14=100|17=EXE000004|31=25.70|32=40|39=2|150=F|151=0|
                        ▲     ▲
                 PossDupFlag  CumQty is a running total, not an increment

<-- 8=FIX.4.4|35=4|34=7|43=Y|…|36=8|123=Y|
                 ▲                  ▲
          SequenceReset          GapFillFlag
```

## What to notice

**The client never asked "did I miss anything?"** It could not have known. The
gap is discovered because the venue's Logon response arrives with `34=7` when
the client expected `34=4`. Every FIX session detects loss this way and only
this way: the next message's sequence number is higher than expected.

**`7=4|16=0` — `EndSeqNo=0` means "everything from here."** The client does not
know how many messages it missed, so it asks open-endedly. `MaxMessagesInResend`
`Request` in the config caps how much the engine will ask for in one go, because
venues refuse ranges beyond a limit — B3's is 10,000.

**`43=Y` on every replayed message.** `PossDupFlag`. It means: you may have seen
this already. Without it a client cannot distinguish history from news, and
after every reconnect it would count the same fills again.

**And here is why that does not break this client:** look at `14=30`, `14=60`,
`14=100`. `CumQty` is a *running total*. The client assigns it. Replay the same
message five times and the answer is still 100. Now look at `32` — `LastQty` —
which is the *increment*. A client that accumulates `32` reaches 200 after this
recovery and then trades against a position that does not exist.

> This is the whole lesson. Cumulative fields are idempotent; incremental ones
> are not. FIX gives you both, and the replay semantics only work if you build
> state from the cumulative ones.

**`122=OrigSendingTime`** appears on replayed messages (stripped from the trace
above because it is a timestamp). It is when the venue *first* sent the message,
while `52=SendingTime` is when it re-sent it. A client stamping its own records
with `52` on a replay records the wrong time for every recovered execution.

**The final `35=4` with `123=Y` — SequenceReset / GapFill.** The venue does not
replay the Logon that occupied `34=7`. Administrative messages are not
retransmitted; replaying an old Logon or Heartbeat would be meaningless at best.
Instead the venue says "skip to 8" and the client's expected sequence number
jumps without any message arriving. Drill 08 goes into this.

## What is not simulated

The gap is real. The reports are genuine `ExecutionReport`s, generated while the
client was disconnected; quickfixgo assigned each a sequence number and wrote it
to the store before discovering there was no socket to write to. The replay
comes off that store.

The one shortcut: `POST /admin/gap?n=2000` exists for synthesizing a large gap
without generating 2000 real messages, which is closer to a certification
scenario. It moves the sequence number without storing anything, so the resend
comes back entirely as GapFill. Useful for watching an engine chunk a large
resend; not what this drill uses.

## Versus real B3

- The gap is generated by the exchange's test centre, typically a few thousand
  messages while you sit disconnected on purpose.
- B3 caps a `ResendRequest` at 10,000 messages and rejects anything larger, so
  the engine has to chunk. `MaxMessagesInResendRequest=10000` is what makes it.
- Drop-copy sessions recover through exactly the same mechanism, which is why
  the drop-copy consumer deduplicates on `ExecID` — drill 05.
- B3 resets sequence numbers on its own schedule; `ResetOnLogon=N` is right
  *within* a session's life, not across a scheduled reset.

## The test

[`internal/drills/drill07_gap_and_resend_test.go`](../../internal/drills/drill07_gap_and_resend_test.go)
disconnects the client, generates three reports it cannot receive, reconnects,
and asserts the recovered position is exactly the position it would have had
without the interruption — plus a byte-for-byte check against
[`testdata/07-gap-and-resend.golden`](../../internal/drills/testdata/07-gap-and-resend.golden),
so the trace above cannot drift from what the code emits.

`TestDrill07_ReplayIsIdempotent` is the one that would catch an accumulating
client.
