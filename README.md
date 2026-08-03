# fix-session-lab

A working FIX 4.4 session boilerplate in Go, built around a fake exchange you
can command.

It exists because the hard part of connecting to a venue is not parsing
messages — libraries do that. The hard part is everything the session layer does
when things go wrong: sequence gaps, resends, replayed messages, a session that
is logged on and silent, orders left open when a connection drops. You normally
learn those during a certification window, against a real venue, on a schedule
that is not yours.

Here you learn them with `curl`.

Modeled on **B3 EntryPoint**, using [quickfixgo/quickfix][qfgo]. Everything is
generic FIX 4.4 except a handful of clearly marked venue-specific tags.

[qfgo]: https://github.com/quickfixgo/quickfix

## Quickstart

```bash
make up                     # exchange + both clients
docker compose logs -f      # watch the wire

curl -s localhost:8081/admin/sessions | jq
curl -s localhost:8081/admin/orders   | jq
curl -XPOST 'localhost:8081/admin/fill?clordid=CL000001&px=25.50'

make down
```

Or without Docker, in three terminals:

```bash
export FIXLAB_OECLIENT_PASSWORD=oe-secret FIXLAB_DCCLIENT_PASSWORD=dc-secret
go run ./cmd/exchange
go run ./cmd/oe-client
go run ./cmd/dc-client
```

## What's running

```
                    ┌──────────────────────────────┐
   oe-client ──FIX──┤  exchange                    │
   (initiator)      │    order-entry acceptor      │
                    │    drop-copy acceptor        ├── :8081 admin HTTP
   dc-client ──FIX──┤    order book (no matching)  │
   (initiator)      └──────────────────────────────┘
                              :9876 FIX
```

Three binaries. The venue is one process because a drop copy is generated from
the same order state as the order-entry report and must carry the same `ExecID`.
The clients are separate binaries because an order-entry client and a drop-copy
consumer are different jobs — and because quickfixgo cannot disconnect one
session out of several, so one session per process is what makes a client
recoverable.

## Drills

Each drill is a scenario you run, a wire trace to read, and one thing to
understand.

| # | Drill | Teaches |
|---|-------|---------|
| [01](docs/drills/01-first-logon.md) | First logon | `RawData` auth, `Text` on Logon, why the password must never hit a log |
| [02](docs/drills/02-heartbeat-testrequest.md) | Heartbeat & TestRequest | what "alive" proves, and what it does not |
| [03](docs/drills/03-rejected-logon.md) | Rejected logon | how a venue refuses you, and why that is hard to diagnose |
| [04](docs/drills/04-order-round-trip.md) | Order round trip | `ExecType` vs `OrdStatus`, cumulative vs incremental fields |
| [05](docs/drills/05-dropcopy-fanout-dedup.md) | Drop-copy fanout & dedup | why `ExecID` is the field everything hangs on |
| [06](docs/drills/06-seqnum-persistence.md) | Sequence persistence | `ResetOnLogon` Y vs N, and what a store is actually for |
| [07](docs/drills/07-gap-and-resend.md) | **Gap & ResendRequest** | recovering a gap, and surviving `PossDupFlag=Y` |
| [08](docs/drills/08-sequencereset-gapfill.md) | SequenceReset / GapFill | why admin messages are not replayed |
| [09](docs/drills/09-silent-session-watchdog.md) | **Silent session & watchdog** | the failure no FIX engine will detect for you |
| [10](docs/drills/10-cancel-on-disconnect.md) | Cancel on disconnect | `35002`/`35003`, and why the timeout window exists |
| [11](docs/drills/11-session-reject.md) | Session-level Reject | `35=3` vs `35=j`, and when you must not send either |

## The admin API

The control plane is how you make the venue misbehave on purpose. It is the
analog of the exchange operator pressing buttons during a certification window.

```
GET  /admin/sessions                              session state and seqnums
GET  /admin/orders                                the venue's order book
POST /admin/fill?clordid=&px=                     fill an order completely
POST /admin/partial?clordid=&qty=&px=             fill part of one
POST /admin/cancel?clordid=&reason=               cancel at the venue's initiative
POST /admin/reject?clordid=&reason=               reject a working order
POST /admin/cancel-all?reason=                    mass cancel open day orders
POST /admin/gap?n=&kind=                          open a sequence gap
POST /admin/silence?on=                           stay logged on, say nothing
POST /admin/reject-logon?reason=&on=              refuse the next logon
```

Every drill test drives these same methods, so what the docs describe is what CI
asserts.

## Tests

```bash
go test ./...                 # every drill
go test ./... -update         # rewrite the golden wire traces
FIXLAB_DUMP_WIRE=1 go test ./... -v -run TestDrill07   # print a full trace
```

Each drill has an integration test that stands up a real acceptor, real
initiator sessions, and real FIX over loopback. Drill 07 additionally asserts
its wire trace byte for byte against a committed golden file, with only the
volatile fields removed — `9`, `10`, `52`, `60`, `122`. Sequence numbers and
`PossDupFlag` are asserted, since a recovery test that ignored them would be
asserting nothing.

That is the guarantee: the trace printed in `docs/drills/07` is the trace CI
checks. If the code stops emitting it, the build goes red rather than the
documentation quietly going stale.

## Notes on quickfixgo

Things worth knowing before you build on it, all verified against v0.9.10:

- **There is no per-session disconnect.** The `session` type is unexported.
  `registry.go` gives you `Send`, `SendToTarget`, `ResetSession`,
  `UnregisterSession`, `SetNextSenderMsgSeqNum`, `SetNextTargetMsgSeqNum`,
  `GetExpectedSenderNum`, `GetExpectedTargetNum`, `GetMessageStore`, `GetLog` —
  and nothing that closes one session. `Initiator.Stop()` unregisters *every*
  session it owns, so `Start()` afterwards leaves them unroutable; a real restart
  means building a fresh `Initiator`. If you are coming from QuickFIX/J looking
  for `Session.disconnect(reason, true)`, it is not there, and the trap is
  different in Go: you have no handle at all.

- **The message stores are not synchronized.** `memoryStore` mutates plain ints
  with no lock. Calling `GetExpectedSenderNum` from an HTTP handler while the
  session goroutine is running is a data race that `-race` will catch. This lab
  reads `MsgSeqNum (34)` off the messages the application is already handed
  instead — see `SeqNums` in `internal/exchange`.

- **`ToAdmin` cannot fail.** It returns nothing, so a missing credential cannot
  abort an outbound Logon. Log it and let the venue's rejection tell you — and
  do not let one missing field suppress the rest of the Logon, or you will
  silently fail to arm cancel-on-disconnect too.

- **`Settings.SessionSettings()` returns clones, not the live settings.** It
  rebuilds a copy from `globalSettings` on every call, so mutating what it hands
  back changes nothing — `NewInitiator` calls it again and gets clean copies.
  Write to `GlobalSettings()`, which is live. This cost a silent no-op that only
  showed up under docker-compose; see `OverrideConnectHost`.

- **Never reject a reject.** If your `FromApp` answers unknown message types
  with a `BusinessMessageReject`, and the counterparty does the same, then the
  first `35=j` either side sends loops forever — both reject the rejection, at
  wire speed. Thirteen thousand messages in six seconds, the first time it
  happened here. Handle `35=j` and `35=3` explicitly and return nil.

- **Rejects split in a place you would not guess.** An out-of-range tag value on
  an application message produces a session Reject (`35=3`); a *missing
  conditionally-required field* on the same message produces a Business Message
  Reject (`35=j`). Same validation pass, different layer — decided by the reject
  reason. Drill 11.

- **Returning `quickfix.RejectLogon{Text: …}` from `FromAdmin`** is how an
  acceptor refuses a Logon. quickfixgo replies with a Logout carrying the reason,
  then drops the connection.

## Layout

```
cmd/exchange      fake venue: both acceptor sessions + admin API
cmd/oe-client     order-entry initiator
cmd/dc-client     drop-copy initiator
internal/exchange venue behavior: order state, ER fanout, commanded actions
internal/client   the two initiator applications
internal/session  shared: logon, credentials, watchdog, supervisor
internal/fixlog   redacting log factory
internal/drills   the tests behind docs/drills
spec/             the data dictionary, and what was changed in it
config/           quickfix settings, one per binary
```

Everything is under `internal/`. This is a reference, not a library — copy what
you need. No API stability is promised, and the code will change as quickfixgo
does.

## Credentials

Passwords come from the environment, keyed by SenderCompID:

```
FIXLAB_OECLIENT_PASSWORD
FIXLAB_DCCLIENT_PASSWORD
```

Never from a config file. The committed settings files contain no secrets, and
`.env` is gitignored. If the venue has no password configured for a
counterparty it accepts any password, so a first run works before you have set
anything up.

## Licence and attribution

MIT — see [LICENSE](LICENSE).

This product includes software developed by quickfixengine.org
(http://www.quickfixengine.org/).

Modeled on B3 EntryPoint, and not affiliated with or endorsed by B3. B3's
specifications are referenced, not reproduced — obtain the authoritative
documents from B3. See [NOTICE.md](NOTICE.md) and
[spec/README.md](spec/README.md).
