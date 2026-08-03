# Drill 06 — Sequence number persistence

**Teaches:** what a message store is actually for, and how `ResetOnLogon=Y`
destroys it without anything appearing to go wrong.

Sequence numbers are the only mechanism a FIX session has for noticing it missed
something. They have to outlive the process, or the notice never comes.

## Run it

```bash
make up
curl -s localhost:8081/admin/sessions | jq '.[].seqnums'

docker compose restart oe-client
curl -s localhost:8081/admin/sessions | jq '.[].seqnums'
```

With `ResetOnLogon=N`, the numbers continue. With `Y`, they are back to 1.

Look at what survived:

```bash
docker compose exec oe-client ls /data/store
```

```
FIX.4.4-OECLIENT-FIXLABEX.body
FIX.4.4-OECLIENT-FIXLABEX.header
FIX.4.4-OECLIENT-FIXLABEX.senderseqnums
FIX.4.4-OECLIENT-FIXLABEX.targetseqnums
FIX.4.4-OECLIENT-FIXLABEX.session
```

Two files hold the counters. The other three hold the messages themselves —
that is what a resend is served from, and why `PersistMessages=Y` matters as
much as the counters do.

## What to notice

**A message store is not a log.** It exists so the session can answer two
questions: what number comes next, and what were the bytes of message *n*.
Drill 07 needs both.

**`ResetOnLogon` is a session-lifetime decision, not a setting you leave alone.**
`Y` is correct exactly once — the first connect, when neither side has stored
state and a clean handshake avoids a spurious gap. After that it is destructive:
every reconnect throws away the evidence that anything was missed, and recovery
becomes impossible because there is nothing left to recover *from*.

**The failure mode is silence.** With `Y`, a reconnect after an outage produces
a healthy-looking session, sequence numbers at 1 on both sides, no errors, no
warnings — and every execution that happened during the outage is simply gone.
Nothing in the logs says so. You find out during reconciliation, or you do not.

**Memory stores make this invisible in development.** A memory store is created
fresh per session, so restarting a process resets the numbers whether or not
`ResetOnLogon` says to. Everything works, nothing recovers, and the difference
only shows up in an environment where the store persists. The drills that
restart a process use a file store for exactly this reason.

## Where the numbers come from

The `/admin/sessions` endpoint reports what the venue *observed* — the
`MsgSeqNum (34)` on the messages its application was handed — rather than
reading the engine's store.

That is not a stylistic choice. quickfixgo's stores carry no synchronization;
`memoryStore` mutates plain `int` fields with no lock. Calling
`quickfix.GetExpectedSenderNum` from an HTTP handler while the session goroutine
is processing a message is a data race that `go test -race` will catch. Reading
tag 34 off messages the application already receives is free and safe.

## Versus real B3

- B3 resets drop-copy sequence numbers on its own schedule, several times a
  week. `ResetOnLogon=N` is correct *within* a session's life, not across a
  scheduled reset — you have to know the venue's calendar.
- The store files are secret material in production: they contain raw outbound
  Logons, which on a `RawData`-authenticating venue means plaintext passwords,
  plus real client order flow. Never attach them to a ticket.
- Some venues let you negotiate the expected sequence number on the Logon itself
  (`NextExpectedMsgSeqNum`, tag 789), which changes the recovery handshake.
  quickfixgo supports it via `EnableNextExpectedMsgSeqNum`; this lab does not
  use it, since the classic ResendRequest path is the one to understand first.

## The test

[`internal/drills/drill06_seqnum_persistence_test.go`](../../internal/drills/drill06_seqnum_persistence_test.go)
restarts the client over a file store and asserts the numbers continue, then
does the same with `ResetOnLogon=Y` and asserts they do not — because the
destructive case deserves a test as much as the correct one.
