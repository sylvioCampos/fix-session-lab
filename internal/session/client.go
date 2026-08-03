package session

import (
	"log"
	"sync"

	"github.com/quickfixgo/quickfix"
)

// Client is the shared half of an initiator application: it authenticates on
// Logon, tracks whether the session is up, and notes when inbound traffic last
// arrived. What to do with an inbound application message is left to the
// embedding type, because that is the part that differs between an order-entry
// client and a drop-copy consumer.
type Client struct {
	Creds LogonCredentials
	Log   *log.Logger

	mu        sync.RWMutex
	loggedOn  map[quickfix.SessionID]bool
	onInbound []func(quickfix.SessionID, InboundClass)
}

// InboundClass distinguishes the two kinds of inbound traffic, because for
// liveness they mean completely different things.
//
// An admin message proves the socket is open and the engine on the other side
// is running. It proves nothing about whether the venue is still producing
// business data. A session can sit exchanging heartbeats indefinitely while
// delivering nothing, and every FIX engine will report it as healthy, because
// by the protocol's own definition it is.
type InboundClass int

const (
	// InboundAdmin is session-level traffic: Logon, Heartbeat, TestRequest,
	// ResendRequest, Reject, Logout.
	InboundAdmin InboundClass = iota
	// InboundApp is business traffic — the thing you actually connected for.
	InboundApp
)

func (c InboundClass) String() string {
	if c == InboundApp {
		return "app"
	}
	return "admin"
}

func NewClient(creds LogonCredentials, logger *log.Logger) *Client {
	return &Client{
		Creds:    creds,
		Log:      logger,
		loggedOn: make(map[quickfix.SessionID]bool),
	}
}

// OnInbound registers a callback fired on every inbound message, with the class
// of traffic it was. The watchdog subscribes here and counts only InboundApp —
// see InboundClass for why that distinction is the entire drill 09 lesson.
func (c *Client) OnInbound(fn func(quickfix.SessionID, InboundClass)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onInbound = append(c.onInbound, fn)
}

// NoteInbound records that a message arrived and fires the OnInbound callbacks.
// Client calls it for admin traffic; an embedding application calls it from its
// own FromApp with InboundApp.
func (c *Client) NoteInbound(sessionID quickfix.SessionID, class InboundClass) {
	c.mu.RLock()
	fns := c.onInbound
	c.mu.RUnlock()

	for _, fn := range fns {
		fn(sessionID, class)
	}
}

// LoggedOn reports whether a session is currently up.
func (c *Client) LoggedOn(sessionID quickfix.SessionID) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.loggedOn[sessionID]
}

// --- quickfix.Application (the parts every client shares) --------------------

func (c *Client) OnCreate(sessionID quickfix.SessionID) {
	c.Log.Printf("session created: %s", sessionID)
}

func (c *Client) OnLogon(sessionID quickfix.SessionID) {
	c.mu.Lock()
	c.loggedOn[sessionID] = true
	c.mu.Unlock()
	c.Log.Printf("logon: %s", sessionID)
}

func (c *Client) OnLogout(sessionID quickfix.SessionID) {
	c.mu.Lock()
	c.loggedOn[sessionID] = false
	c.mu.Unlock()
	c.Log.Printf("logout: %s", sessionID)
}

// ToAdmin injects credentials on the outbound Logon.
//
// It has no way to report an error — quickfixgo's Application interface gives
// ToAdmin no return value — so a missing password is logged and the Logon goes
// out without one, which the venue then rejects. That rejection, with its
// Logout text, is a clearer signal than a silent failure to connect.
func (c *Client) ToAdmin(msg *quickfix.Message, sessionID quickfix.SessionID) {
	msgType, err := msg.Header.GetString(quickfix.Tag(35))
	if err != nil || msgType != "A" {
		return
	}

	if err := InjectLogon(msg, sessionID, c.Creds); err != nil {
		c.Log.Printf("WARNING: logon for %s will be sent without credentials: %v", sessionID, err)
	}
}

func (c *Client) ToApp(msg *quickfix.Message, sessionID quickfix.SessionID) error { return nil }

func (c *Client) FromAdmin(msg *quickfix.Message, sessionID quickfix.SessionID) quickfix.MessageRejectError {
	c.NoteInbound(sessionID, InboundAdmin)
	return nil
}
