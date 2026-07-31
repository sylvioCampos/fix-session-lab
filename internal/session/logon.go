// Package session holds the pieces both sides of a FIX connection need:
// credential handling, Logon construction, and Logon verification.
package session

import (
	"fmt"
	"os"
	"strings"

	"github.com/quickfixgo/field"
	"github.com/quickfixgo/quickfix"
	"github.com/quickfixgo/tag"
)

// Custom tags. Venues that support cancel-on-disconnect typically expose it as
// a pair of Logon fields: what to cancel, and how long to wait first. These
// numbers match B3 EntryPoint. See spec/README.md.
const (
	TagCODType          quickfix.Tag = 35002
	TagCODTimeoutWindow quickfix.Tag = 35003
)

// COD modes carried in tag 35002.
const (
	CODDisabled             = 0
	CODOnDisconnect         = 1
	CODOnLogout             = 2
	CODOnDisconnectOrLogout = 3
)

// PasswordEnvVar returns the environment variable a session's password is read
// from. Keying by SenderCompID rather than using one shared variable means a
// process running several sessions — which is normal — cannot accidentally
// present one session's credentials on another's Logon.
func PasswordEnvVar(senderCompID string) string {
	return "FIXLAB_" + strings.ToUpper(senderCompID) + "_PASSWORD"
}

// Password reads the password for a session. It returns an error rather than an
// empty string so a missing credential fails at Logon with a clear message
// instead of producing a rejected handshake you then have to diagnose.
func Password(senderCompID string) (string, error) {
	env := PasswordEnvVar(senderCompID)
	pw := os.Getenv(env)
	if pw == "" {
		return "", fmt.Errorf("%s is not set", env)
	}
	return pw, nil
}

// LogonCredentials describes what an initiator puts on its outbound Logon.
type LogonCredentials struct {
	// AppID identifies the client application, sent in Text (58).
	AppID string
	// CODType, when non-zero, arms cancel-on-disconnect for this session.
	CODType int
	// CODTimeoutWindow is the grace period in milliseconds before the venue
	// acts on a disconnect. A reconnect inside the window aborts it.
	CODTimeoutWindow int
}

// InjectLogon fills in the authentication and cancel-on-disconnect fields on an
// outbound Logon. Call it from an Application's ToAdmin.
//
// The password is placed in RawData (96) with its length in RawDataLength (95),
// which is how B3 EntryPoint authenticates — not Username/Password (553/554).
// Populating 553/554 against such a venue is harmless but has no effect, and is
// a common first-integration mistake precisely because it is silent.
func InjectLogon(msg *quickfix.Message, sessionID quickfix.SessionID, creds LogonCredentials) error {
	pw, err := Password(sessionID.SenderCompID)
	if err != nil {
		return err
	}

	msg.Body.SetField(tag.RawDataLength, quickfix.FIXInt(len(pw)))
	msg.Body.SetField(tag.RawData, quickfix.FIXString(pw))
	msg.Body.SetField(tag.Text, quickfix.FIXString(creds.AppID))
	// 98=0 (None). FIX-level encryption is not used; transport security, when
	// a venue requires it, is TLS underneath the session.
	msg.Body.SetField(tag.EncryptMethod, quickfix.FIXInt(0))

	if creds.CODType != CODDisabled {
		msg.Body.SetField(TagCODType, quickfix.FIXInt(creds.CODType))
		msg.Body.SetField(TagCODTimeoutWindow, quickfix.FIXInt(creds.CODTimeoutWindow))
	}

	return nil
}

// LogonRequest is what an acceptor extracted from an inbound Logon.
type LogonRequest struct {
	SessionID        quickfix.SessionID
	Password         string
	AppID            string
	CODType          int
	CODTimeoutWindow int
}

// ParseLogon reads the interesting fields off an inbound Logon. Missing
// optional fields are not an error; the caller decides what it requires.
func ParseLogon(msg *quickfix.Message, sessionID quickfix.SessionID) LogonRequest {
	req := LogonRequest{SessionID: sessionID}

	var raw field.RawDataField
	if err := msg.Body.Get(&raw); err == nil {
		req.Password = raw.String()
	}

	var text field.TextField
	if err := msg.Body.Get(&text); err == nil {
		req.AppID = text.String()
	}

	if v, err := msg.Body.GetInt(TagCODType); err == nil {
		req.CODType = v
	}
	if v, err := msg.Body.GetInt(TagCODTimeoutWindow); err == nil {
		req.CODTimeoutWindow = v
	}

	return req
}

// RejectLogon builds the error an Application returns from FromAdmin to refuse a
// Logon. quickfixgo answers it with a Logout carrying the reason, then drops the
// connection — which is what a venue does when credentials are wrong.
func RejectLogon(reason string) quickfix.MessageRejectError {
	return quickfix.RejectLogon{Text: reason}
}
