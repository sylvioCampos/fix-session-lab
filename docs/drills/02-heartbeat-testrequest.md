# Drill 02 — Heartbeat and TestRequest

**Teaches:** the session layer's liveness mechanism, and the precise limit of
what it proves.

## Run it

The default `HeartBtInt` is 30 seconds, so turn it down to watch:

```bash
make up
docker compose logs -f exchange oe-client
```

With nothing else happening you will see `35=0` in both directions on the
interval. Neither application sent them — the engine did.

## Wire trace

An idle session, `HeartBtInt=1`:

```
--> 8=FIX.4.4|35=A|34=1|49=OECLIENT|56=FIXLABEX|98=0|108=30|
<-- 8=FIX.4.4|35=A|34=1|49=FIXLABEX|56=OECLIENT|98=0|108=30|
                                                     ▲
                                          HeartBtInt, agreed on the Logon

--> 8=FIX.4.4|35=0|34=2|49=OECLIENT|56=FIXLABEX|
<-- 8=FIX.4.4|35=0|34=2|49=FIXLABEX|56=OECLIENT|
--> 8=FIX.4.4|35=0|34=3|49=OECLIENT|56=FIXLABEX|
```

Now the venue's traffic stops reaching the client:

```
    ── inbound delivery cut ───────────────────────────────────────────

--> 8=FIX.4.4|35=1|34=5|49=OECLIENT|56=FIXLABEX|112=TEST|
                 ▲                              ▲
          TestRequest                    TestReqID — the answer must echo it

    ── no answer can arrive ───────────────────────────────────────────

--> 8=FIX.4.4|35=5|34=6|49=OECLIENT|56=FIXLABEX|58=Timed out waiting for heartbeat|
    session down
```

## What to notice

**Heartbeats are the engine's, not yours.** They are generated below the
application layer. Your code never sees them go out and cannot suppress them —
which matters for drill 09, where the venue stops sending business data and
cannot stop sending heartbeats.

**`108=HeartBtInt` is negotiated on the Logon.** The initiator proposes, the
acceptor echoes. From then on both sides send a Heartbeat after that long
without anything else to say. Any traffic resets the timer, so a busy session
sends none at all.

**TestRequest is a demand, not a question.** After roughly 1.2 × `HeartBtInt`
with nothing inbound, the engine sends `35=1` with a `TestReqID (112)`. The peer
must answer with a Heartbeat echoing that ID. No answer means the session is
declared dead and dropped.

**The two sides can disagree, and both be right.** In the trace above only
inbound delivery to the client was cut. The venue kept receiving everything the
client sent, so from the venue's side the session looked perfect right up until
the client gave up. Liveness is a per-direction property, and each side can only
observe its own.

**And here is what none of it proves.** Set the venue to publish nothing at the
application layer:

```bash
curl -XPOST 'localhost:8081/admin/silence?on=true'
```

Heartbeats keep flowing. No TestRequest is sent — nothing is quiet at the
transport layer. The session stays logged on indefinitely, every liveness check
passing, delivering nothing. The protocol has no opinion about business data,
and it is not supposed to.

That gap is drill 09.

## Versus real venues

- Venues generally mandate a `HeartBtInt` rather than accepting your proposal.
- A too-short interval on a busy session is pure overhead; too long and you sit
  on a dead connection for minutes. 30 seconds is the usual compromise.
- `CheckLatency` compares `SendingTime` against your clock and drops the session
  when they diverge. It is on by default in quickfixgo and off in this lab's
  configs, because a container's clock is not worth arguing with. In production
  leave it on, and keep NTP working.

## The test

[`internal/drills/drill02_heartbeat_test.go`](../../internal/drills/drill02_heartbeat_test.go)
asserts heartbeats flow both ways on an idle session, uses a freezable TCP relay
to produce a TestRequest and the resulting disconnect, and — the important one —
asserts that application-layer silence produces *no* TestRequest and *no*
disconnect at all.
