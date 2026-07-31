// Package fixlog provides a quickfix.LogFactory that prints FIX traffic to a
// writer with the session password masked.
//
// Why this exists: quickfixgo's bundled screen and file logs write the raw
// outbound message. On a venue that authenticates via RawData — B3 EntryPoint
// does — that puts the session password in plaintext in every Logon line, and
// from there into whatever ships your logs. The masking has to happen before
// the bytes are written, not after, so it belongs in the Log implementation.
package fixlog

import (
	"bytes"
	"fmt"
	"io"
	"sync"

	"github.com/quickfixgo/quickfix"
)

const (
	soh = 0x01
	// displaySep replaces SOH on screen. Real FIX uses 0x01; printing it
	// verbatim makes the drills unreadable in a terminal.
	displaySep = '|'
	mask       = "****"
)

// SecretTags are the tags whose values are masked before writing.
//
// 96 is RawData, which carries the password on venues that authenticate that
// way. 554 is Password, used by venues that authenticate the vanilla FIX way.
// Masking both means the same log factory is safe whichever convention the
// venue you are integrating with happens to use.
var SecretTags = []int{96, 554}

// NewFactory returns a LogFactory writing every session's traffic to w.
func NewFactory(w io.Writer) quickfix.LogFactory {
	return &factory{w: w, mu: new(sync.Mutex)}
}

type factory struct {
	w  io.Writer
	mu *sync.Mutex
}

func (f *factory) Create() (quickfix.Log, error) {
	return &logger{w: f.w, mu: f.mu, prefix: "GLOBAL"}, nil
}

func (f *factory) CreateSessionLog(sessionID quickfix.SessionID) (quickfix.Log, error) {
	return &logger{w: f.w, mu: f.mu, prefix: sessionID.String()}, nil
}

type logger struct {
	w      io.Writer
	mu     *sync.Mutex
	prefix string
}

func (l *logger) OnIncoming(b []byte) { l.write("<--", string(Redact(b))) }
func (l *logger) OnOutgoing(b []byte) { l.write("-->", string(Redact(b))) }
func (l *logger) OnEvent(s string)    { l.write("   ", s) }

func (l *logger) OnEventf(format string, a ...interface{}) {
	l.write("   ", fmt.Sprintf(format, a...))
}

func (l *logger) write(dir, s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.w, "%-28s %s %s\n", l.prefix, dir, s)
}

// Redact masks the value of every tag in SecretTags and swaps SOH for a pipe so
// the result is readable in a terminal. The input is not modified.
//
// It walks fields rather than using a regular expression because a FIX value
// may contain any byte except SOH — including '=' and digits — so anchoring on
// the field delimiter is the only way to find a tag boundary reliably.
func Redact(raw []byte) []byte {
	out := make([]byte, 0, len(raw))

	for i := 0; i < len(raw); {
		end := bytes.IndexByte(raw[i:], soh)
		if end < 0 {
			end = len(raw) - i
		}
		field := raw[i : i+end]

		if eq := bytes.IndexByte(field, '='); eq > 0 && isSecret(field[:eq]) {
			out = append(out, field[:eq+1]...)
			out = append(out, mask...)
		} else {
			out = append(out, field...)
		}

		if i+end < len(raw) {
			out = append(out, displaySep)
		}
		i += end + 1
	}

	return out
}

func isSecret(tag []byte) bool {
	n, ok := atoiStrict(tag)
	if !ok {
		return false
	}
	for _, s := range SecretTags {
		if s == n {
			return true
		}
	}
	return false
}

// atoiStrict parses an unsigned decimal tag number. It rejects anything that is
// not all digits so that a value containing '=' cannot be mistaken for a tag.
func atoiStrict(b []byte) (int, bool) {
	if len(b) == 0 {
		return 0, false
	}
	n := 0
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}
