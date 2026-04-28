package client_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"gas-tcp-bridge/internal/client"
	"gas-tcp-bridge/internal/logging"
	"gas-tcp-bridge/internal/server"
)

const testToken = "test-token"

func TestRawModeEchoIntegration(t *testing.T) {
	echoAddr := startEchoServer(t)
	relayURL := startRelay(t, server.Config{
		FixedUpstream:  echoAddr,
		Token:          testToken,
		SessionTimeout: 5 * time.Second,
		Logger:         logging.NewWithWriter(logging.Error, io.Discard),
	})

	c := startTestClient(t, client.Config{
		RelayURL: relayURL,
		Mode:     client.ModeRaw,
		Token:    testToken,
	})

	conn, err := net.DialTimeout("tcp", c.Addr(), time.Second)
	if err != nil {
		t.Fatalf("dial client: %v", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("deadline: %v", err)
	}

	want := []byte("raw echo")
	if _, err := conn.Write(want); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := make([]byte, len(want))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("echo mismatch got %q want %q", got, want)
	}
}

func TestSOCKS5ModeEchoIntegration(t *testing.T) {
	echoAddr := startEchoServer(t)
	relayURL := startRelay(t, server.Config{
		Token:          testToken,
		SessionTimeout: 5 * time.Second,
		Logger:         logging.NewWithWriter(logging.Error, io.Discard),
	})

	c := startTestClient(t, client.Config{
		RelayURL: relayURL,
		Mode:     client.ModeSOCKS5,
		Token:    testToken,
	})

	conn, err := net.DialTimeout("tcp", c.Addr(), time.Second)
	if err != nil {
		t.Fatalf("dial client: %v", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("deadline: %v", err)
	}

	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("write greeting: %v", err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("read greeting reply: %v", err)
	}
	if reply[0] != 0x05 || reply[1] != 0x00 {
		t.Fatalf("bad greeting reply: %v", reply)
	}

	host, portText, err := net.SplitHostPort(echoAddr)
	if err != nil {
		t.Fatalf("split echo addr: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}
	ip := net.ParseIP(host).To4()
	if ip == nil {
		t.Fatalf("echo host is not IPv4: %s", host)
	}
	req := []byte{0x05, 0x01, 0x00, 0x01, ip[0], ip[1], ip[2], ip[3], byte(port >> 8), byte(port)}
	if _, err := conn.Write(req); err != nil {
		t.Fatalf("write connect: %v", err)
	}
	connectReply := make([]byte, 10)
	if _, err := io.ReadFull(conn, connectReply); err != nil {
		t.Fatalf("read connect reply: %v", err)
	}
	if connectReply[1] != 0x00 {
		t.Fatalf("connect failed reply=%v", connectReply)
	}

	want := []byte("socks echo")
	if _, err := conn.Write(want); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	got := make([]byte, len(want))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("echo mismatch got %q want %q", got, want)
	}
}

func TestFrontedHTTPTransportUsesDialHostAndBSID(t *testing.T) {
	echoAddr := startEchoServer(t)
	broker := server.NewBroker(server.Config{
		FixedUpstream:  echoAddr,
		Token:          testToken,
		SessionTimeout: 5 * time.Second,
		Logger:         logging.NewWithWriter(logging.Error, io.Discard),
	})
	t.Cleanup(broker.Close)

	brokerHTTP := httptest.NewServer(broker.Handler())
	t.Cleanup(brokerHTTP.Close)

	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "script.google.com" {
			t.Errorf("host header=%q want script.google.com", r.Host)
		}
		if r.URL.Path != "/macros/s/test/exec" {
			t.Errorf("path=%q want /macros/s/test/exec", r.URL.Path)
		}
		if r.URL.Query().Get("sid") != "" {
			t.Errorf("legacy sid query should not be sent to Apps Script")
		}
		proxyRelayRequest(t, w, r, brokerHTTP.URL)
	}))
	t.Cleanup(front.Close)

	c := startTestClient(t, client.Config{
		RelayURL:  "http://blocked.invalid/macros/s/test/exec",
		Mode:      client.ModeRaw,
		Token:     testToken,
		FrontDial: front.Listener.Addr().String(),
		FrontHost: "script.google.com",
	})

	conn, err := net.DialTimeout("tcp", c.Addr(), time.Second)
	if err != nil {
		t.Fatalf("dial client: %v", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("deadline: %v", err)
	}

	want := []byte("fronted echo")
	if _, err := conn.Write(want); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := make([]byte, len(want))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("echo mismatch got %q want %q", got, want)
	}
}

func startTestClient(t *testing.T, cfg client.Config) *client.Client {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cfg.Listen = "127.0.0.1:0"
	cfg.ChunkSize = 1024
	cfg.PollInterval = 10 * time.Millisecond
	cfg.RequestTimeout = 2 * time.Second
	cfg.Logger = logging.NewWithWriter(logging.Error, io.Discard)
	c, err := client.Start(ctx, cfg)
	if err != nil {
		cancel()
		t.Fatalf("start client: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		_ = c.Close()
	})
	return c
}

func startEchoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen echo: %v", err)
	}
	t.Cleanup(func() {
		_ = ln.Close()
	})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()
	return ln.Addr().String()
}

func startRelay(t *testing.T, cfg server.Config) string {
	t.Helper()
	broker := server.NewBroker(cfg)
	t.Cleanup(broker.Close)

	brokerHTTP := httptest.NewServer(broker.Handler())
	t.Cleanup(brokerHTTP.Close)

	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyRelayRequest(t, w, r, brokerHTTP.URL)
	}))
	t.Cleanup(relay.Close)
	return relay.URL
}

func proxyRelayRequest(t *testing.T, w http.ResponseWriter, r *http.Request, brokerURL string) {
	t.Helper()
	op := r.URL.Query().Get("op")
	sid := bridgeSID(r)
	if sid == "" {
		http.Error(w, "missing session id", http.StatusBadRequest)
		return
	}
	switch op {
	case "up":
		target := brokerURL + "/up?sid=" + url.QueryEscape(sid)
		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Bridge-Token", r.Header.Get("X-Bridge-Token"))
		proxyHTTP(w, req)
	case "down":
		ack := r.URL.Query().Get("ack")
		target := brokerURL + "/down?sid=" + url.QueryEscape(sid) + "&ack=" + url.QueryEscape(ack)
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		req.Header.Set("X-Bridge-Token", r.Header.Get("X-Bridge-Token"))
		proxyHTTP(w, req)
	default:
		http.Error(w, "bad op", http.StatusBadRequest)
	}
}

func bridgeSID(r *http.Request) string {
	if sid := r.URL.Query().Get("bsid"); sid != "" {
		return sid
	}
	return r.URL.Query().Get("sid")
}

func proxyHTTP(w http.ResponseWriter, req *http.Request) {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
