# Drill 01 — First logon

**Teaches:** how a FIX session is established, how a venue that authenticates
via `RawData` differs from vanilla FIX, and why the password must never reach a
log.

Nothing else in this lab works until this handshake completes. Every later drill
starts from a logged-on session.

## Run it

```bash
make up
docker compose logs -f exchange oe-client dc-client
```

Two sessions connect: the order-entry client and the drop-copy client, both
against the same venue process on port 9876.

```bash
curl -s localhost:8081/admin/sessions | jq
```

```json
[
  { "session_id": "FIX.4.4:FIXLABEX->DCCLIENT", "kind": "DROP_COPY",
    "logged_on": true, "seqnums": { "last_sent": 1, "last_received": 1 } },
  { "session_id": "FIX.4.4:FIXLABEX->OECLIENT", "kind": "ORDER_ENTRY",
    "logged_on": true, "seqnums": { "last_sent": 1, "last_received": 1 } }
]
```

## Wire trace

```
OECLIENT->FIXLABEX  -->  8=FIX.4.4|9=115|35=A|34=1|49=OECLIENT|52=…|56=FIXLABEX|
                         58=fixlab-oe/1.0|95=10|96=****|98=0|108=30|141=Y|10=146|
FIXLABEX->OECLIENT  <--  (the same bytes, as the venue received them)
FIXLABEX->OECLIENT       Received logon request
FIXLABEX->OECLIENT       Logon contains ResetSeqNumFlag=Y, resetting sequence numbers to 1
FIXLABEX->OECLIENT       Responding to logon request
FIXLABEX->OECLIENT  -->  8=FIX.4.4|9=77|35=A|34=1|49=FIXLABEX|52=…|56=OECLIENT|
                         98=0|108=30|141=Y|10=214|
OECLIENT->FIXLABEX  <--  (the venue's Logon response)
OECLIENT->FIXLABEX       Received logon response
```

## What to notice

**`96=****` — the password is masked, and that is not free.** quickfixgo's
bundled screen and file logs write the raw message. On a venue that
authenticates through `RawData (96)`, that puts the session password in
plaintext in every Logon line, and from there into whatever ships your logs.
`internal/fixlog` exists to mask it before the bytes are written. Copy that file
if you take nothing else from this repo.

**`95=10` and `96=…`, not `553`/`554`.** B3 EntryPoint authenticates through
`RawDataLength` and `RawData`. `Username (553)` and `Password (554)` are not part
of its Logon. Populating them is not an error — the venue ignores unknown
optional tags — which is exactly what makes it a bad first bug: nothing fails
loudly, the Logon is simply refused and you go looking in the wrong place.

**`58=fixlab-oe/1.0` on a Logon.** Stock FIX 4.4 does not list `Text` on Logon;
B3 does, and expects an application identifier there. An engine validating
against the unmodified dictionary answers with *"Tag not defined for this message
type"* and drops the connection **before `OnLogon` fires** — so your application
log shows nothing at all, just a connect/disconnect loop. See
[`spec/README.md`](../../spec/README.md), which also covers the mirror-image
mistake of marking `Text` required.

**`141=Y` — ResetSeqNumFlag.** Both sides reset to 1. That is right for a first
connect and wrong for everything afterwards; drill 06 covers why.

**`108=30` — HeartBtInt.** The client proposes 30 seconds and the venue echoes
it. From here on, silence longer than that gets a TestRequest. Drill 02.

**The venue's Logon response carries no `95`/`96`.** Authentication is one-way:
the client proves itself to the venue, not the other way round.

## Try breaking it

```bash
# Make the venue refuse the next logon.
curl -XPOST 'localhost:8081/admin/reject-logon?reason=invalid%20credentials'
docker compose restart oe-client
```

The venue answers the Logon with a `35=5` Logout carrying the reason, then drops
the connection. The client reconnects on `ReconnectInterval` and is refused
again — a loop that looks identical to a network problem from the outside and is
distinguishable only by reading the Logout text.

```bash
curl -XPOST 'localhost:8081/admin/reject-logon?on=false'
```

## Versus real B3

- CompIDs are provisioned per session and per environment; the ones here are
  invented.
- Production requires network-level access (B3 uses a private network) before
  a socket will even open.
- B3 issues one password per session, not one per firm.
- B3 supports cancel-on-disconnect fields on this same Logon — drill 10.

## The test

[`internal/drills/drill01_first_logon_test.go`](../../internal/drills/drill01_first_logon_test.go)
asserts the handshake completes on both sessions and that neither password
appears anywhere in the captured wire log.
