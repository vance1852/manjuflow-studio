// Command server runs the ManjuFlow studio backend: HTTP API, SQLite storage,
// render workers and the periodic sweeper.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	// Embedding the timezone database keeps the business time zone available in
	// the scratch container image, which ships no system zoneinfo.
	_ "time/tzdata"

	"github.com/vance1852/manjuflow-studio/internal/app"
	"github.com/vance1852/manjuflow-studio/internal/clock"
	"github.com/vance1852/manjuflow-studio/internal/config"
	"github.com/vance1852/manjuflow-studio/internal/logging"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "manjuflow: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.FromEnvironment()
	if err != nil {
		return err
	}
	level, err := logging.ParseLevel(cfg.LogLevel)
	if err != nil {
		return err
	}
	logger := logging.New(os.Stdout, level).With("service", "manjuflow")
	logger.Info("configuration loaded", "config", cfg.Redacted())

	timeSource, err := clock.NewSystem()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	application, err := app.Build(ctx, cfg, logger, timeSource)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := application.Close(); closeErr != nil {
			logger.Error("cannot close database", "error", closeErr)
		}
	}()

	return application.Run(ctx, 2)
}
