// Command dc-client runs a drop-copy initiator session against the fake venue.
//
// It only listens. A drop-copy session is read-only: the venue rejects any
// application message sent on it, so this binary has no write path at all.
package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/quickfixgo/quickfix"
	filestore "github.com/quickfixgo/quickfix/store/file"

	"github.com/sylvioCampos/fix-session-lab/internal/client"
	"github.com/sylvioCampos/fix-session-lab/internal/fixlog"
	"github.com/sylvioCampos/fix-session-lab/internal/session"
)

func main() {
	cfgPath := flag.String("config", "config/dc-client.cfg", "quickfix settings file")
	host := flag.String("host", "", "override SocketConnectHost (docker-compose passes the service name)")
	flag.Parse()

	logger := log.New(os.Stdout, "dc-client ", log.LstdFlags|log.Lmicroseconds)

	if err := run(*cfgPath, *host, logger); err != nil {
		logger.Fatalf("fatal: %v", err)
	}
}

func run(cfgPath, host string, logger *log.Logger) error {
	cfg, err := os.Open(cfgPath)
	if err != nil {
		return err
	}
	defer cfg.Close()

	settings, err := quickfix.ParseSettings(cfg)
	if err != nil {
		return err
	}
	if err := session.OverrideConnectHost(settings, host); err != nil {
		return err
	}

	// Cancel-on-disconnect is not armed on a drop-copy session: it owns no
	// orders. Arming it here would do nothing on a correct venue and would be
	// a confusing thing for a reader to find in a config.
	app := client.NewDropCopy(session.LogonCredentials{AppID: "fixlab-dc/1.0"}, logger)

	initiator, err := quickfix.NewInitiator(
		app, filestore.NewStoreFactory(settings), settings, fixlog.NewFactory(os.Stdout))
	if err != nil {
		return err
	}

	if err := initiator.Start(); err != nil {
		return err
	}
	defer initiator.Stop()

	if _, err := session.SoleSession(settings); err != nil {
		return err
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	logger.Printf("shutting down; %d report(s) recorded, %d duplicate(s) discarded",
		len(app.Reports()), app.Duplicates())
	return nil
}
