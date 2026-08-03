// Command oe-client runs an order-entry initiator session against the fake
// venue and sends orders on a timer.
//
// It is a separate binary from dc-client on purpose. quickfixgo has no
// per-session disconnect — the session type is unexported, and Initiator.Stop()
// unregisters every session it owns — so the only way to force one session to
// reconnect is to give it an Initiator of its own. One session per binary makes
// that possible; see drill 09.
package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/quickfixgo/enum"
	"github.com/quickfixgo/quickfix"
	filestore "github.com/quickfixgo/quickfix/store/file"
	"github.com/shopspring/decimal"

	"github.com/sylvioCampos/fix-session-lab/internal/client"
	"github.com/sylvioCampos/fix-session-lab/internal/fixlog"
	"github.com/sylvioCampos/fix-session-lab/internal/session"
)

type options struct {
	config    string
	host      string
	symbol    string
	qty       string
	price     string
	every     time.Duration
	codType   int
	codWindow int
	watchdog  time.Duration
}

func main() {
	var o options

	flag.StringVar(&o.config, "config", "config/oe-client.cfg", "quickfix settings file")
	flag.StringVar(&o.host, "host", "", "override SocketConnectHost (docker-compose passes the service name)")
	flag.StringVar(&o.symbol, "symbol", "PETR4", "symbol to send orders for")
	flag.StringVar(&o.qty, "qty", "100", "order quantity")
	flag.StringVar(&o.price, "price", "25.50", "limit price")
	flag.DurationVar(&o.every, "every", 0, "send an order on this interval; 0 sends one and waits")
	flag.IntVar(&o.codType, "cod-type", 0, "cancel-on-disconnect mode (0 off, 1 disconnect, 2 logout, 3 either)")
	flag.IntVar(&o.codWindow, "cod-window", 30000, "cancel-on-disconnect grace window in milliseconds")
	flag.DurationVar(&o.watchdog, "watchdog", 0,
		"force a reconnect after this long with no inbound application traffic; 0 disables")
	flag.Parse()

	logger := log.New(os.Stdout, "oe-client ", log.LstdFlags|log.Lmicroseconds)

	if err := run(o, logger); err != nil {
		logger.Fatalf("fatal: %v", err)
	}
}

func run(o options, logger *log.Logger) error {
	orderQty, err := decimal.NewFromString(o.qty)
	if err != nil {
		return err
	}
	orderPx, err := decimal.NewFromString(o.price)
	if err != nil {
		return err
	}

	cfg, err := os.Open(o.config)
	if err != nil {
		return err
	}
	defer cfg.Close()

	settings, err := quickfix.ParseSettings(cfg)
	if err != nil {
		return err
	}
	session.OverrideConnectHost(settings, o.host)

	sessionID, err := session.SoleSession(settings)
	if err != nil {
		return err
	}

	storeFactory := filestore.NewStoreFactory(settings)
	logFactory := fixlog.NewFactory(os.Stdout)

	// The application is replaced on every reconnect — a client that restarts
	// loses its in-memory order state and rebuilds it from what the venue
	// replays (drill 07). It is held atomically because the watchdog rebuilds
	// it from its own goroutine while the send loop is reading it.
	var app atomic.Pointer[client.OrderEntry]
	var watchdog *session.Watchdog

	build := func() (*quickfix.Initiator, error) {
		next := client.NewOrderEntry(session.LogonCredentials{
			AppID:            "fixlab-oe/1.0",
			CODType:          o.codType,
			CODTimeoutWindow: o.codWindow,
		}, logger)

		if watchdog != nil {
			session.WatchClient(next.Client, watchdog)
		}
		app.Store(next)

		return quickfix.NewInitiator(next, storeFactory, settings, logFactory)
	}

	supervisor := &session.Supervisor{Build: build, Log: logger}

	if o.watchdog > 0 {
		watchdog = &session.Watchdog{
			Timeout: o.watchdog,
			Restart: supervisor.Restart,
			Log:     logger,
			// No Active window here: this lab's venue has no trading hours.
			// Against a real venue this is where you would exclude legitimate
			// quiet periods — and where getting the boundary wrong turns the
			// watchdog into a machine for breaking your own connection. See
			// docs/drills/09-silent-session-watchdog.md.
		}
	}

	if err := supervisor.Start(); err != nil {
		return err
	}
	defer supervisor.Stop()

	if watchdog != nil {
		watchdog.Start()
		defer watchdog.Stop()
		logger.Printf("watchdog armed: reconnect after %s without application traffic", o.watchdog)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go sendOrders(app.Load, sessionID, client.NewOrder{
		Symbol:   o.symbol,
		Side:     enum.Side_BUY,
		OrderQty: orderQty,
		Price:    orderPx,
	}, o.every, stop, logger)

	<-stop
	logger.Printf("shutting down")
	return nil
}

func sendOrders(current func() *client.OrderEntry, sessionID quickfix.SessionID,
	order client.NewOrder, every time.Duration, stop <-chan os.Signal, logger *log.Logger) {

	if !waitForLogon(current, sessionID, stop) {
		return
	}

	for {
		if _, err := current().Send(sessionID, order); err != nil {
			logger.Printf("send failed: %v", err)
		}
		if every <= 0 {
			return
		}
		select {
		case <-stop:
			return
		case <-time.After(every):
		}
	}
}

// waitForLogon blocks until the session is up. Sending before Logon completes
// fails with an unknown-session error, which reads as a configuration problem
// when it is really a race.
func waitForLogon(current func() *client.OrderEntry, sessionID quickfix.SessionID,
	stop <-chan os.Signal) bool {

	for {
		if app := current(); app != nil && app.LoggedOn(sessionID) {
			return true
		}
		select {
		case <-stop:
			return false
		case <-time.After(100 * time.Millisecond):
		}
	}
}
