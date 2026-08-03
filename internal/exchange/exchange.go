// Package exchange implements the fake venue: one process hosting both an
// order-entry acceptor session and a drop-copy acceptor session.
//
// Both live in one process on purpose. Drop copy exists because the venue fans
// every execution report out to a second session, and that fanout needs shared
// order state. Splitting them into separate processes would force an invented
// message bus between them and teach an architecture no venue actually has.
package exchange

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/quickfixgo/quickfix"

	"github.com/sylvioCampos/fix-session-lab/internal/session"
)

// Kind distinguishes the two acceptor sessions. It is read from a custom
// SessionKind key in the settings file rather than inferred from CompIDs,
// because CompID conventions differ per venue and per environment.
type Kind string

const (
	KindOrderEntry Kind = "ORDER_ENTRY"
	KindDropCopy   Kind = "DROP_COPY"

	// SettingKind is the settings key naming a session's role.
	SettingKind = "SessionKind"
)

// App implements quickfix.Application for both acceptor sessions.
type App struct {
	book *Book

	mu       sync.RWMutex
	kinds    map[quickfix.SessionID]Kind
	loggedOn map[quickfix.SessionID]bool
	cod      map[quickfix.SessionID]session.LogonRequest
	seqnums  map[quickfix.SessionID]SeqNums

	// expect holds the password the venue requires from each counterparty,
	// captured at startup. A CompID absent from this map authenticates with
	// anything, so a first run works before the reader has configured a thing.
	expect map[string]string

	// rejectLogons, when set, makes the next Logon fail with this reason.
	// Drill 03 uses it; the admin API sets it.
	rejectLogons string

	// silenced suppresses outbound application traffic while leaving the TCP
	// connection and the session state untouched. This reproduces the failure
	// that no FIX engine protects you from: a session that is logged on,
	// socket-alive, and saying nothing.
	silenced bool

	log Logger
}

// Logger is the minimal event sink the exchange needs. It is separate from the
// FIX log so venue-side narration does not get mistaken for wire traffic.
type Logger interface {
	Printf(format string, v ...any)
}

func New(book *Book, log Logger) *App {
	return &App{
		book:     book,
		kinds:    make(map[quickfix.SessionID]Kind),
		loggedOn: make(map[quickfix.SessionID]bool),
		cod:      make(map[quickfix.SessionID]session.LogonRequest),
		seqnums:  make(map[quickfix.SessionID]SeqNums),
		expect:   make(map[string]string),
		log:      log,
	}
}

// SeqNums is what the venue has actually seen on the wire for a session.
//
// These are observed, not read back from the engine. quickfixgo's store
// implementations carry no synchronization — memoryStore and fileStore both
// mutate plain ints — so calling quickfix.GetExpectedSenderNum from an HTTP
// handler while the session goroutine is processing a message is a genuine data
// race that -race will catch. Reading tag 34 off the messages the application
// is already handed costs nothing and is safe, because the application owns the
// lock it stores them under.
type SeqNums struct {
	LastSent     int `json:"last_sent"`
	LastReceived int `json:"last_received"`
}

// tagMsgSeqNum is set on the header before ToAdmin/ToApp are called, so the
// application always sees the number the message will go out with.
const tagMsgSeqNum quickfix.Tag = 34

func (a *App) noteSent(msg *quickfix.Message, sessionID quickfix.SessionID) {
	n, err := msg.Header.GetInt(tagMsgSeqNum)
	if err != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.seqnums[sessionID]
	s.LastSent = n
	a.seqnums[sessionID] = s
}

func (a *App) noteReceived(msg *quickfix.Message, sessionID quickfix.SessionID) {
	n, err := msg.Header.GetInt(tagMsgSeqNum)
	if err != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.seqnums[sessionID]
	s.LastReceived = n
	a.seqnums[sessionID] = s
}

// RegisterKind records what role a configured session plays.
func (a *App) RegisterKind(sessionID quickfix.SessionID, kind Kind) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.kinds[sessionID] = kind
}

// LoadKinds reads each configured session's role and the credential it expects.
//
// Credentials are captured once, at startup, rather than read from the
// environment on every Logon. A venue's view of what a counterparty's password
// should be is not supposed to change underneath a running session, and
// re-reading it per handshake would mean an operator editing the environment
// could silently start accepting a different password.
func (a *App) LoadKinds(settings *quickfix.Settings) error {
	for sessionID, s := range settings.SessionSettings() {
		raw, err := s.Setting(SettingKind)
		if err != nil {
			return fmt.Errorf("session %s: %s is required: %w", sessionID, SettingKind, err)
		}

		kind := Kind(strings.ToUpper(strings.TrimSpace(raw)))
		if kind != KindOrderEntry && kind != KindDropCopy {
			return fmt.Errorf("session %s: %s=%q is not %s or %s",
				sessionID, SettingKind, raw, KindOrderEntry, KindDropCopy)
		}
		a.RegisterKind(sessionID, kind)

		// The counterparty's CompID is this session's target.
		if pw, err := session.Password(sessionID.TargetCompID); err == nil {
			a.mu.Lock()
			a.expect[sessionID.TargetCompID] = pw
			a.mu.Unlock()
		}
	}
	return nil
}

func (a *App) kindOf(sessionID quickfix.SessionID) Kind {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.kinds[sessionID]
}

// sessionOf returns the SessionID configured for a role, and whether one is
// configured at all. It deliberately says nothing about whether the session is
// connected — see publish for why the venue keeps sending to a session that is
// down.
func (a *App) sessionOf(kind Kind) (quickfix.SessionID, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	for id, k := range a.kinds {
		if k == kind {
			return id, true
		}
	}
	return quickfix.SessionID{}, false
}

// --- quickfix.Application ---------------------------------------------------

func (a *App) OnCreate(sessionID quickfix.SessionID) {
	a.log.Printf("session created: %s (%s)", sessionID, a.kindOf(sessionID))
}

func (a *App) OnLogon(sessionID quickfix.SessionID) {
	a.mu.Lock()
	a.loggedOn[sessionID] = true
	a.mu.Unlock()
	a.log.Printf("logon: %s (%s)", sessionID, a.kindOf(sessionID))
}

func (a *App) OnLogout(sessionID quickfix.SessionID) {
	a.mu.Lock()
	a.loggedOn[sessionID] = false
	req, armed := a.cod[sessionID]
	a.mu.Unlock()

	a.log.Printf("logout: %s (%s)", sessionID, a.kindOf(sessionID))

	if armed && req.CODType != session.CODDisabled {
		a.onSessionLost(sessionID, req)
	}
}

// ToAdmin is called before an outbound admin message leaves. The venue has
// nothing to add — it does not authenticate to the client — so it only records
// the sequence number.
func (a *App) ToAdmin(msg *quickfix.Message, sessionID quickfix.SessionID) {
	a.noteSent(msg, sessionID)
}

func (a *App) ToApp(msg *quickfix.Message, sessionID quickfix.SessionID) error {
	a.noteSent(msg, sessionID)
	return nil
}

// FromAdmin sees inbound admin messages. Logon is where the venue authenticates
// and where cancel-on-disconnect is armed.
func (a *App) FromAdmin(msg *quickfix.Message, sessionID quickfix.SessionID) quickfix.MessageRejectError {
	a.noteReceived(msg, sessionID)

	msgType, err := msg.Header.GetString(quickfix.Tag(35))
	if err != nil {
		return err
	}
	if msgType != "A" {
		return nil
	}

	a.mu.RLock()
	reject := a.rejectLogons
	a.mu.RUnlock()

	if reject != "" {
		a.log.Printf("rejecting logon from %s: %s", sessionID, reject)
		return session.RejectLogon(reject)
	}

	req := session.ParseLogon(msg, sessionID)

	if err := a.checkPassword(sessionID, req.Password); err != nil {
		a.log.Printf("rejecting logon from %s: %v", sessionID, err)
		return session.RejectLogon(err.Error())
	}

	a.mu.Lock()
	a.cod[sessionID] = req
	a.mu.Unlock()

	if req.CODType != session.CODDisabled {
		a.log.Printf("cancel-on-disconnect armed for %s: type=%d window=%dms",
			sessionID, req.CODType, req.CODTimeoutWindow)
	}

	return nil
}

// FromApp sees inbound application messages.
func (a *App) FromApp(msg *quickfix.Message, sessionID quickfix.SessionID) quickfix.MessageRejectError {
	a.noteReceived(msg, sessionID)

	if a.kindOf(sessionID) == KindDropCopy {
		// A drop-copy session is read-only. A client that sends application
		// traffic on it has misunderstood the service, and saying so is more
		// useful than silently dropping the message.
		return quickfix.NewBusinessMessageRejectError(
			"drop copy is read-only; application messages are not accepted", 3, nil)
	}

	msgType, err := msg.Header.GetString(quickfix.Tag(35))
	if err != nil {
		return err
	}

	switch msgType {
	case "D":
		return a.onNewOrderSingle(msg, sessionID)
	default:
		return quickfix.NewBusinessMessageRejectError(
			fmt.Sprintf("unsupported message type %q", msgType), 3, nil)
	}
}

func (a *App) checkPassword(sessionID quickfix.SessionID, got string) error {
	a.mu.RLock()
	expected, configured := a.expect[sessionID.TargetCompID]
	a.mu.RUnlock()

	if !configured {
		// No credential configured for this counterparty: accept anything. A
		// real venue would refuse; the lab defaults to permissive so a first
		// run works before the reader has set any environment variables.
		return nil
	}
	if got != expected {
		return errors.New("invalid credentials")
	}
	return nil
}
