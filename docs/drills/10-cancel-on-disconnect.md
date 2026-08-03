# Drill 10 — Cancel on disconnect

**Teaches:** what happens to your working orders when the connection dies, and
why the grace window is not optional.

Orders outlive the session that created them. If your connection drops with
orders resting on the book, they keep trading — and you cannot see them, cancel
them, or react to their fills. For anything automated that is the worst state
available: exposure you own and cannot control.

Cancel on disconnect is the venue's answer. You tell it on the Logon what to
cancel and how long to wait first.

## Run it

```bash
make up
docker compose stop oe-client

docker compose run --rm oe-client oe-client \
  -config config/oe-client.cfg -host exchange \
  -cod-type 1 -cod-window 5000 -every 3s
```

Confirm the venue actually saw it — arming COD in your own config and failing to
send it is a silent failure:

```bash
curl -s localhost:8081/admin/sessions | jq '.[] | {kind, cod_type, cod_timeout_window_ms}'
```

```json
{ "kind": "ORDER_ENTRY", "cod_type": 1, "cod_timeout_window_ms": 5000 }
```

Then kill the client with orders working and watch the drop-copy session:

```bash
docker compose logs -f dc-client
```

## Wire trace

Arming, on the Logon:

```
--> 8=FIX.4.4|35=A|34=1|49=OECLIENT|56=FIXLABEX|58=fixlab-oe/1.0|95=9|96=****|
    98=0|108=30|35002=1|35003=5000|
                   ▲        ▲
              CODType   CODTimeoutWindow (milliseconds)
```

The cancel, after the connection dies and the window expires — visible only on
the drop copy, since the order-entry session is gone:

```
<-- 8=FIX.4.4|35=8|34=3|49=FIXLABEX|56=DCCLIENT|11=CL000001|14=0|17=EXE000002|
    37=ORD000001|38=100|39=4|54=1|55=PETR4|58=cancel on disconnect|150=4|151=0|378=100|
                          ▲                                          ▲       ▲
                    OrdStatus=Cancelled                        ExecType=4  ExecRestatementReason
```

## What to notice

**`378=100` is how you know it was not you.** `ExecRestatementReason` marks a
report the venue generated on its own initiative. Without it, a cancel the venue
produced is indistinguishable from one you requested — and a client that assumes
every cancel is its own will conclude it sent a request it never sent.

This value is also a good example of why dictionaries need maintaining: `100` is
outside stock FIX 4.4's enum range for tag 378. An engine validating strictly
will reject the message rather than deliver it. See
[`spec/README.md`](../../spec/README.md).

**Drop copy is how you see any of this.** The order-entry session is gone by
definition — that is what triggered the cancel. Every report the venue generates
while you are away goes to a session that is not listening. The drop copy is
still connected and still recording, which is a large part of why you run one.
The order-entry session will also recover them on reconnect through the resend
mechanism, but only if `ResetOnLogon=N` — drill 07.

**The timeout window is the whole design.** Firing immediately would turn every
transient blip into a mass cancel, which is worse than the problem COD solves.
A client that reconnects inside the window keeps its orders. That also puts a
hard deadline on your reconnect path: it has to complete a full Logon inside the
window, so target comfortably under it rather than close to it.

**Good-till orders survive.** COD targets day orders. A good-till order is meant
to outlive the session; cancelling it because a socket dropped would destroy a
standing instruction you never withdrew.

**Arming it is per session, not per firm.** A drop-copy session owns no orders,
so COD on it does nothing — and finding COD fields in a drop-copy config is a
reliable sign someone copied a file without reading it.

## Versus real B3

- Modes are `1` cancel on hard disconnect, `2` cancel on logout, `3` either. The
  distinction matters: a graceful end-of-day logout is not the same event as a
  network failure, and you may well want different behavior for each.
- The window is capped — 0 to 60,000 ms.
- Codes in the 100 range are B3's; other venues number their restatement reasons
  differently. Check the spec rather than assuming.
- COD is not a substitute for your own reconciliation. It is a safety net for
  the case where you cannot act, not a replacement for acting.

## The test

[`internal/drills/drill10_cancel_on_disconnect_test.go`](../../internal/drills/drill10_cancel_on_disconnect_test.go)
asserts the venue saw the COD fields on the Logon, that an open order is
cancelled after the window, and that the cancel report reaches the drop-copy
session.

Two more cover the cases that cost money: a client reconnecting inside the
window keeps its orders, and good-till orders survive a cancel-all.
