// Command oe-client runs an order-entry initiator session against the fake
// venue and sends orders on a timer.
//
// It is a separate binary from dc-client on purpose. quickfixgo has no
// per-session disconnect — the session type is unexported, and Initiator.Stop()
// unregisters every session it owns — so the only way to force one session to
// reconnect is to give it a process (or at least an Initiator) of its own.
// One session per binary makes that possible; see drill 09.
package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
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

func main() {
	cfgPath := flag.String("config", "config/oe-client.cfg", "quickfix settings file")
	host := flag.String("host", "", "override SocketConnectHost (docker-compose passes the service name)")
	symbol := flag.String("symbol", "PETR4", "symbol to send orders for")
	qty := flag.String("qty", "100", "order quantity")
	price := flag.String("price", "25.50", "limit price")
	every := flag.Duration("every", 0, "send an order on this interval; 0 sends one and waits")
	codType := flag.Int("cod-type", 0, "cancel-on-disconnect mode (0 off, 1 disconnect, 2 logout, 3 either)")
	codWindow := flag.Int("cod-window", 30000, "cancel-on-disconnect grace window in milliseconds")
	flag.Parse()

	logger := log.New(os.Stdout, "oe-client ", log.LstdFlags|log.Lmicroseconds)

	if err := run(*cfgPath, *host, *symbol, *qty, *price, *every, *codType, *codWindow, logger); err != nil {
		logger.Fatalf("fatal: %v", err)
	}
}

func run(cfgPath, host, symbol, qty, price string, every time.Duration, codType, codWindow int, logger *log.Logger) error {
	orderQty, err := decimal.NewFromString(qty)
	if err != nil {
		return err
	}
	orderPx, err := decimal.NewFromString(price)
	if err != nil {
		return err
	}

	cfg, err := os.Open(cfgPath)
	if err != nil {
		return err
	}
	defer cfg.Close()

	settings, err := quickfix.ParseSettings(cfg)
	if err != nil {
		return err
	}
	session.OverrideConnectHost(settings, host)

	app := client.NewOrderEntry(session.LogonCredentials{
		AppID:            "fixlab-oe/1.0",
		CODType:          codType,
		CODTimeoutWindow: codWindow,
	}, logger)

	initiator, err := quickfix.NewInitiator(
		app, filestore.NewStoreFactory(settings), settings, fixlog.NewFactory(os.Stdout))
	if err != nil {
		return err
	}

	if err := initiator.Start(); err != nil {
		return err
	}
	defer initiator.Stop()

	sessionID, err := session.SoleSession(settings)
	if err != nil {
		return err
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go sendOrders(app, sessionID, client.NewOrder{
		Symbol:   symbol,
		Side:     enum.Side_BUY,
		OrderQty: orderQty,
		Price:    orderPx,
	}, every, stop, logger)

	<-stop
	logger.Printf("shutting down")
	return nil
}

func sendOrders(app *client.OrderEntry, sessionID quickfix.SessionID, order client.NewOrder,
	every time.Duration, stop <-chan os.Signal, logger *log.Logger) {

	waitForLogon(app, sessionID, stop)

	for {
		if _, err := app.Send(sessionID, order); err != nil {
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
func waitForLogon(app *client.OrderEntry, sessionID quickfix.SessionID, stop <-chan os.Signal) {
	for !app.LoggedOn(sessionID) {
		select {
		case <-stop:
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}
