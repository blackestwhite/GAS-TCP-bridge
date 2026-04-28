package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gas-tcp-bridge/internal/logging"
	"gas-tcp-bridge/internal/server"
)

func main() {
	var cfg server.Config
	var logLevel string
	flag.StringVar(&cfg.Listen, "listen", ":8080", "HTTP listen address")
	flag.DurationVar(&cfg.SessionTimeout, "session-timeout", 60*time.Second, "idle session timeout")
	flag.IntVar(&cfg.MaxDownBatch, "max-down-batch", 256*1024, "maximum bytes per /down response")
	flag.IntVar(&cfg.ChunkSize, "chunk-size", 16*1024, "TCP chunk size in bytes")
	flag.StringVar(&cfg.FixedUpstream, "fixed-upstream", "", "optional fixed upstream host:port for raw mode")
	flag.StringVar(&cfg.DialNetwork, "dial-network", "tcp", "target dial network: tcp, tcp4, or tcp6")
	flag.StringVar(&cfg.Token, "token", "", "shared bridge token")
	flag.StringVar(&logLevel, "log-level", "info", "log level: debug, info, warn, error")
	flag.Parse()

	if cfg.DialNetwork != "tcp" && cfg.DialNetwork != "tcp4" && cfg.DialNetwork != "tcp6" {
		fmt.Fprintln(os.Stderr, "dial-network must be tcp, tcp4, or tcp6")
		os.Exit(2)
	}

	level, err := logging.ParseLevel(logLevel)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	logger := logging.New(level)
	cfg.Logger = logger

	broker := server.NewBroker(cfg)
	defer broker.Close()

	httpServer := &http.Server{
		Addr:              cfg.Listen,
		Handler:           broker.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Infof("broker listening on %s", cfg.Listen)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Errorf("server failed: %v", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Warnf("shutdown failed: %v", err)
	}
}
