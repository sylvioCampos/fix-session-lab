# Drill 03 — Rejected logon

**Teaches:** how a venue refuses you, and why that is harder to diagnose than it
sounds.

A refused Logon does not announce itself. The venue answers with a Logout and
closes the connection; your client reconnects on its interval and is refused
again. What you observe is a reconnect loop indistinguishable from a firewall
rule, a wrong port, or a venue that is down — unless you read the text on the
Logout.

## Run it

```bash
make up

curl -XPOST 'localhost:8081/admin/reject-logon?reason=invalid%20credentials'
docker compose restart oe-client
docker compose logs -f oe-client
```

Then stop refusing, and the client recovers on its own with no intervention:

```bash
curl -XPOST 'localhost:8081/admin/reject-logon?on=false'
```

To reach the same refusal the ordinary way, give the client a password the venue
does not expect:

```bash
FIXLAB_OECLIENT_PASSWORD=wrong docker compose up -d oe-client
```

## Wire trace

```
--> 8=FIX.4.4|35=A|34=1|49=OECLIENT|56=FIXLABEX|58=fixlab-oe/1.0|95=9|96=****|98=0|108=30|141=Y|
<-- 8=FIX.4.4|35=5|34=1|49=FIXLABEX|56=OECLIENT|58=invalid credentials|
         ▲                                       ▲
      Logout                            the only thing that tells you why

    connection closed by the venue
    OECLIENT->FIXLABEX  Reconnecting in 3s

--> 8=FIX.4.4|35=A|34=1|…                        and again, and again
```

## What to notice

**The refusal is a Logout, not a Reject.** `35=5`, not `35=3`. Session-level
rejects are for messages the engine could not accept; a Logon it *understood*
and *declined* gets a Logout with a reason. Nothing about the shape of the
response distinguishes it from an ordinary end-of-day Logout — only the timing
and the text.

**`58=Text` is the entire diagnosis.** If you drop it, or your log rotates it
away, or you never look, a rejected logon and a broken network are the same
event. Log the Logout text on every disconnect. This is the cheapest possible
thing to get right and it is routinely missed.

**The client retries forever and that is correct.** quickfixgo reconnects after
a connection failure on `ReconnectInterval`, and a refused Logon looks like one.
You do not want a client that gives up — a venue's transient rejection during a
restart would then need manual intervention. But it does mean a permanent
condition, such as a wrong password, presents as an infinite loop rather than an
error.

**`141=Y` on every retry.** `ResetSeqNumFlag`. With `ResetOnLogon=Y` the client
proposes a reset on every attempt. That is fine here because no session was ever
established, and it is exactly the setting drill 06 says to turn off once one is.

**In Go, the application refuses by returning `quickfix.RejectLogon`** from
`FromAdmin`. quickfixgo turns that into the Logout above and drops the
connection. Returning an ordinary `MessageRejectError` instead produces a
different, wrong-looking exchange.

## Why the venue captures credentials at startup

`internal/exchange` reads the password it expects for each counterparty once,
when it loads its settings, rather than consulting the environment on each
Logon. A venue's view of what your password should be is not supposed to change
underneath a live session, and re-reading it per handshake would let an operator
editing the environment silently start accepting something different.

A counterparty with no configured credential authenticates with anything, so a
first run works before you have set up a thing.

## Versus real B3

- B3 issues one password per session, not per firm, and rotates them.
- A Logon can also be refused for reasons unrelated to credentials — a CompID
  not provisioned for that gateway, connecting from an unexpected address, or
  arriving outside the session window. The text is what tells them apart.
- Repeated failed Logons attract attention from the exchange. Do not leave a
  misconfigured client looping against production.

## The test

[`internal/drills/drill03_rejected_logon_test.go`](../../internal/drills/drill03_rejected_logon_test.go)
asserts the refusal arrives as a Logout carrying its reason, that the client
keeps retrying, and that it recovers unaided once the venue stops refusing. A
second test does it with a genuinely wrong password and checks that neither the
right nor the wrong one appears anywhere in the log.
