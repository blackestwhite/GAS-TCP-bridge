package client

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestGetDownUsesPollTimeout(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sid":"test","ack":0,"chunks":[]}`))
	}))
	t.Cleanup(relay.Close)

	relayURL, err := url.Parse(relay.URL)
	if err != nil {
		t.Fatalf("parse relay url: %v", err)
	}
	cfg := withDefaults(Config{
		RelayURL:       relay.URL,
		PollTimeout:    20 * time.Millisecond,
		RequestTimeout: time.Second,
	})
	c := &Client{
		cfg:  cfg,
		http: newHTTPClient(cfg, relayURL),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := &connState{
		client: c,
		sid:    "test",
		ctx:    ctx,
	}

	start := time.Now()
	_, err = st.getDown()
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("getDown succeeded, want timeout")
	}
	if !strings.Contains(err.Error(), "poll timeout after") {
		t.Fatalf("error %q does not mention poll timeout", err)
	}
	if elapsed > 150*time.Millisecond {
		t.Fatalf("getDown took %s, poll timeout was not enforced", elapsed)
	}
}

func TestDecodeRelayDownReportsNonJSONDetails(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
		Body:       io.NopCloser(strings.NewReader("<html><title>Google error</title></html>")),
		Request: &http.Request{
			URL: &url.URL{Host: "script.googleusercontent.com"},
		},
	}

	_, err := decodeRelayDown(resp)
	if err == nil {
		t.Fatal("decode succeeded, want non-json error")
	}
	for _, want := range []string{"non-json", "script.googleusercontent.com", "text/html", "<html><title>Google error</title></html>"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not contain %q", err, want)
		}
	}
}

func TestReadSOCKS5ConnectRejectsIPv6(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	deadline := time.Now().Add(time.Second)
	_ = server.SetDeadline(deadline)
	_ = client.SetDeadline(deadline)

	errCh := make(chan error, 1)
	go func() {
		_, _, err := readSOCKS5Connect(server, time.Second, true)
		errCh <- err
	}()

	if _, err := client.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("write greeting: %v", err)
	}
	method := make([]byte, 2)
	if _, err := io.ReadFull(client, method); err != nil {
		t.Fatalf("read method selection: %v", err)
	}
	if method[0] != 0x05 || method[1] != 0x00 {
		t.Fatalf("method selection = %v, want [5 0]", method)
	}

	target := net.ParseIP("2001:67c:4e8:f004::a").To16()
	if target == nil {
		t.Fatal("parse IPv6 target")
	}
	req := append([]byte{0x05, 0x01, 0x00, 0x04}, target...)
	req = append(req, 0x01, 0xbb)
	if _, err := client.Write(req); err != nil {
		t.Fatalf("write connect request: %v", err)
	}

	reply := make([]byte, 10)
	if _, err := io.ReadFull(client, reply); err != nil {
		t.Fatalf("read SOCKS reply: %v", err)
	}
	if reply[1] != 0x08 {
		t.Fatalf("reply code = 0x%02x, want 0x08", reply[1])
	}

	err := <-errCh
	if err == nil {
		t.Fatal("readSOCKS5Connect succeeded, want IPv6 rejection")
	}
	if !strings.Contains(err.Error(), "IPv6 SOCKS target rejected") {
		t.Fatalf("error %q does not mention IPv6 rejection", err)
	}
}
