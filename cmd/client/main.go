package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gas-tcp-bridge/internal/client"
	"gas-tcp-bridge/internal/logging"
)

func main() {
	var cfg client.Config
	var logLevel string
	flag.StringVar(&cfg.Listen, "listen", "127.0.0.1:1080", "local TCP listen address")
	flag.StringVar(&cfg.RelayURL, "relay-url", "", "Google Apps Script Web App URL")
	flag.StringVar(&cfg.SIDParam, "sid-param", "bsid", "session query parameter for Apps Script requests")
	flag.StringVar(&cfg.Mode, "mode", client.ModeSOCKS5, "local mode: socks5 or raw")
	flag.IntVar(&cfg.ChunkSize, "chunk-size", 16*1024, "TCP chunk size in bytes")
	flag.DurationVar(&cfg.PollInterval, "poll-interval", 100*time.Millisecond, "downstream poll interval")
	flag.DurationVar(&cfg.RequestTimeout, "request-timeout", 20*time.Second, "HTTP request timeout")
	flag.StringVar(&cfg.Token, "token", "", "shared bridge token")
	flag.StringVar(&cfg.FrontDial, "front-dial", "", "optional TCP dial address for fronted HTTPS, e.g. www.google.com:443")
	flag.StringVar(&cfg.FrontSNI, "front-sni", "", "optional TLS ServerName/SNI for fronted HTTPS")
	flag.StringVar(&cfg.FrontHost, "front-host", "", "optional HTTP Host override for the relay URL host")
	flag.BoolVar(&cfg.FrontForceHTTP1, "front-force-http1", true, "force HTTP/1.1 when fronted HTTPS is enabled")
	flag.StringVar(&logLevel, "log-level", "info", "log level: debug, info, warn, error")
	flag.Parse()

	level, err := logging.ParseLevel(logLevel)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	cfg.Logger = logging.New(level)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	c, err := client.Start(ctx, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	<-ctx.Done()
	_ = c.Close()
}
