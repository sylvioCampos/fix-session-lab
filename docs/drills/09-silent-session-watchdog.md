# Drill 09 — The silent session, and the watchdog

**Teaches:** the failure no FIX engine will detect for you, and how to detect it
yourself without making things worse.

A session can be logged on, with an open socket, heartbeats flowing both ways,
and deliver no business data at all. Every liveness mechanism the protocol has
keeps passing, because by the protocol's own definition the session is healthy.

It is not healthy. You are receiving nothing you connected for, and nothing
below your application will ever say so.

## Run it

```bash
make up

# Arm the watchdog
docker compose stop oe-client
docker compose run --rm oe-client oe-client \
  -config config/oe-client.cfg -host exchange -every 5s -watchdog 20s

# In another terminal: the venue goes quiet at the application layer only
curl -XPOST 'localhost:8081/admin/silence?on=true'
```

Watch what happens, and what does not:

- `/admin/sessions` keeps reporting `logged_on: true`
- Heartbeats keep flowing in the logs
- No `35=1` TestRequest is sent — nothing is quiet at the transport layer
- After 20 seconds the watchdog fires and rebuilds the Initiator

```bash
curl -XPOST 'localhost:8081/admin/silence?on=false'
```

## What to notice

**Nothing is broken at the session layer.** Compare with drill 02, where
freezing the wire produced a TestRequest and then a disconnect within seconds.
Here the socket is fine, the engine on both sides is fine, and the session
survives indefinitely. The protocol has no opinion about business data, and it
is not supposed to.

**Only the application can detect this**, because only the application knows
what "data should have arrived by now" means. Ten minutes of quiet is alarming
on a busy order-entry session at midday and completely normal on a drop-copy
session for an instrument nobody is trading. No library can decide that for you.

**Count application traffic, not all traffic.** This is the assertion that
decides whether a watchdog is worth having:

```go
c.OnInbound(func(_ quickfix.SessionID, class session.InboundClass) {
    if class == session.InboundApp {
        w.Notify()
    }
})
```

A watchdog wired to *all* inbound traffic looks correct, compiles, tests green,
and detects nothing — because heartbeats never stop. That is their entire job.

**The active window is the other half, and it cuts the other way.** A watchdog
that treats every quiet moment as suspicious will tear down healthy sessions
during legitimate quiet: before the market opens, over a lunch break, on an
illiquid instrument. Get the boundary wrong by an hour and you have built a
machine for breaking your own connection every morning — one that fires during
pre-open, reconnects, and looks from the outside like an unstable venue.

```go
w := &session.Watchdog{
    Timeout: 5 * time.Minute,
    Active:  duringTradingHours,   // wrong boundary here is its own outage
    Restart: supervisor.Restart,
}
```

## Recovery in Go is not what you would expect

quickfixgo gives you no way to disconnect one session. The `session` type is
unexported, and `Initiator.Stop()` unregisters every session the initiator owns
— so calling `Start()` afterwards leaves them registered nowhere, and
`SendToTarget` fails with an unknown-session error that looks nothing like the
actual cause.

So a forced reconnect means: stop the Initiator, build a new one from the same
settings and the same store, start it. That is what `internal/session`'s
`Supervisor` does, and why every client in this lab owns exactly one session —
one session per Initiator is what makes "reconnect this session" meaningful at
all.

Sequence continuity survives because the *store* survives, not the Initiator.
With a memory store the new Initiator would start from scratch and silently lose
everything the old session had missed.

## If you are coming from QuickFIX/J

The Java version of this code has a trap Go does not. `Session.logout()` and
`Session.disconnect(reason, true)` look interchangeable and do opposite things:

| Method | On the wire | How the initiator reads it |
|---|---|---|
| `logout(reason)` | sends `35=5` — a graceful, protocol-level close | deliberate: **suppresses** auto-reconnect |
| `disconnect(reason, true)` | drops the TCP connection | a failure: **reconnects** per `ReconnectInterval` |

A watchdog whose job is to force a reconnect needs the second. Built on
`logout()` it produces the exact opposite of its purpose: the session stops
reconnecting *because* the shutdown looked intentional, so a stuck session
becomes permanently stuck. That is strictly worse than having no watchdog at
all, and it is nearly invisible — the code reads as if it is helping.

In Go you cannot make that mistake, because there is no per-session handle to
make it with. The Go trap is the one above: you must rebuild the Initiator, and
if you have several sessions on it you cannot recycle just one.

## Versus real venues

- A real watchdog's timeout comes from the venue's actual publishing cadence,
  and differs per session type and per market segment.
- Symptoms are frequently masked. A stuck real-time feed can look healthy for
  days if some other source is filling the same fields — you find it by
  comparing coverage field by field, not by watching the session.
- Restarting on a timer is not a substitute. It reconnects on schedule whether
  or not anything is wrong, and papers over the underlying fault.

## The test

[`internal/drills/drill09_silent_session_test.go`](../../internal/drills/drill09_silent_session_test.go)
silences the venue at the application layer, asserts the session stays logged
on, asserts the watchdog fires anyway, and asserts recovery built a new
Initiator.

Two more tests cover the ways a watchdog fails silently: one confirms admin
traffic does not count as liveness, the other that an inactive window suppresses
firing entirely.
