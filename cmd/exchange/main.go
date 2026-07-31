// Command exchange runs the fake venue: an order-entry acceptor session, a
// drop-copy acceptor session, and the admin control plane that drives drills.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/quickfixgo/quickfix"
	filestore "github.com/quickfixgo/quickfix/store/file"

	"github.com/sylvioCampos/fix-session-lab/internal/admin"
	"github.com/sylvioCampos/fix-session-lab/internal/exchange"
	"github.com/sylvioCampos/fix-session-lab/internal/fixlog"
)

func main() {
	cfgPath := flag.String("config", "config/exchange.cfg", "quickfix settings file")
	adminAddr := flag.String("admin", ":8081", "admin control-plane listen address")
	flag.Parse()

	logger := log.New(os.Stdout, "exchange  ", log.LstdFlags|log.Lmicroseconds)

	if err := run(*cfgPath, *adminAddr, logger); err != nil {
		logger.Fatalf("fatal: %v", err)
	}
}

func run(cfgPath, adminAddr string, logger *log.Logger) error {
	cfg, err := os.Open(cfgPath)
	if err != nil {
		return err
	}
	defer cfg.Close()

	settings, err := quickfix.ParseSettings(cfg)
	if err != nil {
		return err
	}

	book := exchange.NewBook()
	app := exchange.New(book, logger)
	if err := app.LoadKinds(settings); err != nil {
		return err
	}

	// A file store, not a memory store: the gap drill restarts a process and
	// expects sequence numbers to survive. FileStorePath is set per session in
	// the settings file and mounted as a volume in docker-compose.
	storeFactory := filestore.NewStoreFactory(settings)

	acceptor, err := quickfix.NewAcceptor(app, storeFactory, settings, fixlog.NewFactory(os.Stdout))
	if err != nil {
		return err
	}

	if err := acceptor.Start(); err != nil {
		return err
	}
	defer acceptor.Stop()

	srv := &http.Server{
		Addr:              adminAddr,
		Handler:           admin.New(app).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		logger.Printf("admin control plane listening on %s", adminAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Printf("admin server stopped: %v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	logger.Printf("shutting down")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(ctx)
}
