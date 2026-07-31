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
	onInbound []func(quickfix.SessionID)
}

func NewClient(creds LogonCredentials, logger *log.Logger) *Client {
	return &Client{
		Creds:    creds,
		Log:      logger,
		loggedOn: make(map[quickfix.SessionID]bool),
	}
}

// OnInbound registers a callback fired on every inbound message, admin or
// application. The watchdog uses it as its liveness signal.
//
// Whether admin messages should count is the whole question. A heartbeat proves
// the socket and the engine are alive but says nothing about business data
// still flowing, so a watchdog that accepts heartbeats as liveness cannot
// detect a silent venue. Drill 09 builds on this hook.
func (c *Client) OnInbound(fn func(quickfix.SessionID)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onInbound = append(c.onInbound, fn)
}

// NoteInbound records that a message arrived on a session and fires the
// OnInbound callbacks. Client calls it for admin traffic; an embedding
// application calls it from its own FromApp for application traffic.
func (c *Client) NoteInbound(sessionID quickfix.SessionID) {
	c.mu.RLock()
	fns := c.onInbound
	c.mu.RUnlock()

	for _, fn := range fns {
		fn(sessionID)
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
	c.NoteInbound(sessionID)
	return nil
}
