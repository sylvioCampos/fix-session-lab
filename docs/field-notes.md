# Field notes: seven things quickfixgo will not tell you

Every item below cost real debugging time while building this lab, and every one
of them is reproduced by a test in this repository. They are ordered by symptom,
because that is what you will have when you arrive: something is broken, and the
logs are not saying why.

Verified against `quickfixgo/quickfix` **v0.9.10**. Where a claim is about the
library's internals rather than its documented API, the source is quoted.

---

## 1. "My session connects, immediately disconnects, and my application log says nothing"

You will see a connect/disconnect loop with no explanation. Not a rejection, not
an error — `OnLogon` is simply never called, so your application never learns
anything happened.

The cause is dictionary validation failing on the Logon itself. Stock FIX 4.4
does **not** list `Text (58)` on `Logon`, and several venues — B3 EntryPoint
among them — expect an application identifier there. The engine rejects the
message before your application is involved:

```
FIXLABEX->OECLIENT       Tag not defined for this message type
FIXLABEX->OECLIENT       Disconnected
```

Fix it in the dictionary, not the code:

```xml
<message name='Logon' msgcat='admin' msgtype='A'>
  ...
  <field name='Text' required='N' />
</message>
```

**And do not reach for `required='Y'`** because your venue mandates it. A
dictionary's `required` flag is not directional — it applies to messages you
receive as well as ones you send, and the venue's own Logon *response* typically
omits `Text`. You will then reject their reply and be back where you started.
Enforce what you must send in application code; keep the dictionary permissive.

The same trap applies to venue-specific *values* on standard tags, which is
easier to miss than a custom tag. `ExecRestatementReason (378)` is a stock FIX
4.4 field, but B3 uses values in the 100 range that vanilla 4.4 does not define.
A strict engine answers the venue's own execution report with *"Value is
incorrect (out of range) for this tag"*.

→ [`spec/README.md`](../spec/README.md), [drill 01](drills/01-first-logon.md)

---

## 2. "Where is `Session.disconnect()`?"

If you are arriving from QuickFIX/J, you are looking for
`Session.disconnect(reason, true)` and you will not find it. quickfixgo's
`session` type is **unexported**. The complete public surface for touching a live
session is `registry.go`:

```
Send, SendToTarget, ResetSession, UnregisterSession,
SetNextSenderMsgSeqNum, SetNextTargetMsgSeqNum,
GetExpectedSenderNum, GetExpectedTargetNum,
GetMessageStore, GetLog
```

Nothing there closes one session.

`Initiator.Stop()` looks like the answer and is a trap of its own — it
unregisters **every** session the initiator owns:

```go
// quickfix/initiator.go
func (i *Initiator) Stop() {
    ...
    for sessionID := range i.sessionSettings {
        err := UnregisterSession(sessionID)
```

So calling `Start()` afterwards leaves those sessions registered nowhere, and
`SendToTarget` fails with an unknown-session error that looks nothing like the
actual cause. A genuine reconnect means constructing a **new** `Initiator` over
the same store — the store is what carries sequence continuity across the swap.

The practical consequence: give each session its own `Initiator`, or you cannot
recycle one without taking down the others.

Worth knowing what you are *not* exposed to. In QuickFIX/J the equivalent code
has a nastier failure: `logout()` and `disconnect(reason, true)` look
interchangeable and are opposites. `logout()` is a graceful protocol-level close,
so the initiator treats it as deliberate and **suppresses auto-reconnect
entirely** — a watchdog built on it makes a stuck session permanently stuck,
which is strictly worse than no watchdog. In Go you cannot make that mistake,
because there is no handle to make it with.

→ [`internal/session/supervisor.go`](../internal/session/supervisor.go),
[drill 09](drills/09-silent-session-watchdog.md)

---

## 3. "The session is up, heartbeats are flowing, and no data is arriving"

Nothing is broken at the session layer, and nothing ever will be. Heartbeats are
generated *below* the application — your code cannot suppress them and neither
can the venue's. So a venue that has stopped publishing business data still
looks perfectly healthy: socket open, session logged on, `TestRequest` never
fires, every liveness check the protocol offers passing.

FIX has no opinion about business data, and is not supposed to.

Detecting this is your job, because only your application knows what "data
should have arrived by now" means. Ten minutes of quiet is alarming on a busy
order-entry session at midday and completely normal on a drop-copy session for
an instrument nobody is trading.

Two things decide whether such a watchdog is worth having:

**Count application traffic only.** Wired to all inbound traffic it will never
fire, because heartbeats never stop — that is their entire job. That version
compiles, tests green, and detects nothing.

```go
c.OnInbound(func(_ quickfix.SessionID, class session.InboundClass) {
    if class == session.InboundApp {
        w.Notify()
    }
})
```

**Bound it to when silence is actually suspicious.** A watchdog that treats every
quiet moment as a fault will tear down healthy sessions before the open, over a
lunch auction, on an illiquid instrument. Get the boundary wrong by an hour and
you have built a machine that breaks your own connection every morning — and the
logs will show it doing its job.

→ [`internal/session/watchdog.go`](../internal/session/watchdog.go),
[drill 09](drills/09-silent-session-watchdog.md)

---

## 4. "My client and the venue are sending each other rejects forever"

The single most expensive mistake in this list, and the easiest to write.

If your `FromApp` answers unknown message types with a `BusinessMessageReject`,
and the counterparty does the same, then the first `35=j` either side emits never
stops. You reject the rejection, they reject that, and the two of you ping-pong
at wire speed. Observed here: **thirteen thousand messages in six seconds**,
sequence numbers gone on both sides, both stores full of nothing but rejections.

```go
switch msgType {
case msgTypeExecutionReport:
    return c.onExecutionReport(...)
case msgTypeBusinessReject:
    c.Log.Printf("business reject: %s", rejectText(msg))
    return nil          // log it, never answer it
default:
    return quickfix.NewBusinessMessageRejectError(...)
}
```

**Never reject a reject.** Both ends need the fix — repairing one shortens the
loop by a message and leaves it running.

There is a related rule for read-only sessions. A drop-copy session must send
only session-level admin traffic, and your application alone cannot guarantee
that: with `RejectInvalidMessage=Y` the engine answers anything failing
dictionary validation with a `35=3` before your code runs. Set it to `N` on such
sessions — validation stays on, anomalies are still logged, only the automatic
reply is suppressed.

→ [`internal/drills/reject_storm_test.go`](../internal/drills/reject_storm_test.go),
[drill 11](drills/11-session-reject.md)

---

## 5. "Why did that give me a `35=3` and this a `35=j`?"

The split is not where most people guess. It is decided by the *reject reason*,
not by severity or by which layer you feel the problem belongs to.

Two near-identical `NewOrderSingle` messages:

| What is wrong | Answer |
|---|---|
| `Side` carries a value the dictionary does not define | `35=3` session Reject |
| `Symbol` is missing (conditionally required) | `35=j` Business Message Reject |

Same engine, same dictionary validation pass, different layer. And your
application sees neither — by the time you could have an opinion, the engine has
already replied. You cannot implement leniency in `FromApp`, because `FromApp` is
not called.

A third case exists and is routinely collapsed into these two: a venue may answer
an unacceptable *order* with an `ExecutionReport` carrying `ExecType=8`. That is
not a protocol problem at all — the order was understood and declined on its
merits.

→ [drill 11](drills/11-session-reject.md)

---

## 6. "`-race` is flagging quickfixgo's message store"

It is right to. The stores carry no synchronisation whatsoever:

```go
// quickfix/memory_store.go
type memoryStore struct {
    senderMsgSeqNum, targetMsgSeqNum int
    ...
}

func (store *memoryStore) NextTargetMsgSeqNum() int {
    return store.targetMsgSeqNum + 1
}
```

Plain ints, no mutex. So calling `quickfix.GetExpectedSenderNum` from an HTTP
handler, a metrics scrape or a health check while the session goroutine is
processing a message is a genuine data race, and `-race` will catch it.

If you only need to *observe* sequence numbers, do not read the store at all.
`MsgSeqNum (34)` is set on the header before `ToAdmin`/`ToApp` are called, so
the application is already handed everything it needs, under whatever lock you
choose:

```go
func (a *App) noteSent(msg *quickfix.Message, id quickfix.SessionID) {
    n, err := msg.Header.GetInt(tagMsgSeqNum)
    ...
}
```

Writing a sequence number from outside the session goroutine is a different
matter and is unavoidable for some recovery scenarios. Do it while the session
is quiet, and do not build anything unattended on it.

→ [`internal/exchange/exchange.go`](../internal/exchange/exchange.go)

---

## 7. "I changed a setting and nothing happened"

`Settings.SessionSettings()` does not return the settings. It returns freshly
built copies, every call:

```go
// quickfix/settings.go
func (s *Settings) SessionSettings() map[SessionID]*SessionSettings {
    allSessionSettings := make(map[SessionID]*SessionSettings)
    for sessionID, settings := range s.sessionSettings {
        cloneSettings := s.globalSettings.clone()   // <- a clone
        cloneSettings.overlay(settings)
        allSessionSettings[sessionID] = cloneSettings
    }
    return allSessionSettings
}
```

So this compiles, reads correctly, and does nothing at all:

```go
for _, s := range settings.SessionSettings() {
    s.Set("SocketConnectHost", host)   // writes to a copy nobody reads
}
```

`NewInitiator` calls `SessionSettings()` again and gets clean clones. In this lab
the symptom was every client in docker-compose sitting in a reconnect loop
against `127.0.0.1` inside its own container, with the override apparently
applied.

`GlobalSettings()` returns the live object, so write there instead. The catch is
that a value in a `[session]` block still overlays the global one — so verify the
result rather than trusting it:

```go
settings.GlobalSettings().Set("SocketConnectHost", host)

for sessionID, s := range settings.SessionSettings() {
    if got, _ := s.Setting("SocketConnectHost"); got != host {
        return fmt.Errorf("session %s still points at %q", sessionID, got)
    }
}
```

→ [`internal/session/settings.go`](../internal/session/settings.go)

---

## A note on how these were found

Six of the seven were found by running things, not by reading code. Several
survived a full test suite, code review and green CI before showing up:

- #7 was invisible until the docker-compose stack ran for the first time. Every
  test generated its settings with the right host already in it.
- #4 could not be caught by the tests that existed, because they called `FromApp`
  directly with a hand-built message. That verifies what one side *answers* and
  by construction cannot observe what happens when the other side answers back.
  The loop needs two real applications on a real socket.
- #1 was found by a handshake failing, and the fix was found by reading the
  engine's event log rather than the application's.

The lab exists so you can meet all of them deliberately, with `curl`, instead of
during a certification window.

→ [Start here](../README.md#drills)
