// Package admin is the fake venue's control plane.
//
// Everything a drill needs the venue to do on command lives behind this HTTP
// API: fill an order, go silent, open a sequence gap, refuse the next logon.
// On a real venue these are the buttons an exchange operator presses during a
// certification window. Exposing them over HTTP means a reader drives a drill
// with curl and CI drives the same drill with a test client.
package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/shopspring/decimal"

	"github.com/sylvioCampos/fix-session-lab/internal/exchange"
)

// Server exposes the control plane over HTTP.
type Server struct {
	app *exchange.App
}

func New(app *exchange.App) *Server {
	return &Server{app: app}
}

// Handler returns the control-plane routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /admin/sessions", s.sessions)
	mux.HandleFunc("GET /admin/orders", s.orders)
	mux.HandleFunc("POST /admin/fill", s.fill)
	mux.HandleFunc("POST /admin/partial", s.partial)
	mux.HandleFunc("POST /admin/cancel", s.cancel)
	mux.HandleFunc("POST /admin/reject", s.reject)
	mux.HandleFunc("POST /admin/silence", s.silence)
	mux.HandleFunc("POST /admin/gap", s.gap)
	mux.HandleFunc("POST /admin/reject-logon", s.rejectLogon)
	mux.HandleFunc("POST /admin/cancel-all", s.cancelAll)

	return mux
}

func (s *Server) sessions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.app.Sessions())
}

func (s *Server) orders(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.app.Orders())
}

func (s *Server) fill(w http.ResponseWriter, r *http.Request) {
	clOrdID, err := requireStr(r, "clordid")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	px, err := requireDec(r, "px")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	if err := s.app.Fill(clOrdID, px); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"filled": clOrdID})
}

func (s *Server) partial(w http.ResponseWriter, r *http.Request) {
	clOrdID, err := requireStr(r, "clordid")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	qty, err := requireDec(r, "qty")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	px, err := requireDec(r, "px")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	if err := s.app.Partial(clOrdID, qty, px); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"partial": clOrdID})
}

func (s *Server) cancel(w http.ResponseWriter, r *http.Request) {
	clOrdID, err := requireStr(r, "clordid")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	reason := r.URL.Query().Get("reason")
	if reason == "" {
		reason = "cancelled by venue"
	}

	if err := s.app.Cancel(clOrdID, reason); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"cancelled": clOrdID})
}

func (s *Server) reject(w http.ResponseWriter, r *http.Request) {
	clOrdID, err := requireStr(r, "clordid")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	reason := r.URL.Query().Get("reason")
	if reason == "" {
		reason = "rejected by venue"
	}

	if err := s.app.Reject(clOrdID, reason); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"rejected": clOrdID})
}

func (s *Server) silence(w http.ResponseWriter, r *http.Request) {
	on := r.URL.Query().Get("on") != "false"
	s.app.SetSilenced(on)
	writeJSON(w, http.StatusOK, map[string]bool{"silenced": on})
}

func (s *Server) gap(w http.ResponseWriter, r *http.Request) {
	n, err := requireInt(r, "n")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	kind := exchange.Kind(r.URL.Query().Get("kind"))
	if kind == "" {
		kind = exchange.KindOrderEntry
	}

	next, err := s.app.AdvanceSeqNum(kind, n)
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"kind": kind, "next_sender_seqnum": next})
}

func (s *Server) rejectLogon(w http.ResponseWriter, r *http.Request) {
	reason := r.URL.Query().Get("reason")
	if r.URL.Query().Get("on") == "false" {
		reason = ""
	} else if reason == "" {
		reason = "invalid credentials"
	}

	s.app.SetRejectLogons(reason)
	writeJSON(w, http.StatusOK, map[string]string{"reject_logon_reason": reason})
}

func (s *Server) cancelAll(w http.ResponseWriter, r *http.Request) {
	reason := r.URL.Query().Get("reason")
	if reason == "" {
		reason = "mass cancel by venue"
	}
	n := s.app.CancelAllOpenDay(reason)
	writeJSON(w, http.StatusOK, map[string]int{"cancelled": n})
}

// --- helpers ----------------------------------------------------------------

func requireStr(r *http.Request, key string) (string, error) {
	v := r.URL.Query().Get(key)
	if v == "" {
		return "", fmt.Errorf("query parameter %q is required", key)
	}
	return v, nil
}

func requireInt(r *http.Request, key string) (int, error) {
	raw, err := requireStr(r, key)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("query parameter %q must be an integer: %w", key, err)
	}
	return n, nil
}

func requireDec(r *http.Request, key string) (decimal.Decimal, error) {
	raw, err := requireStr(r, key)
	if err != nil {
		return decimal.Zero, err
	}
	d, err := decimal.NewFromString(raw)
	if err != nil {
		return decimal.Zero, fmt.Errorf("query parameter %q must be a number: %w", key, err)
	}
	return d, nil
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}
