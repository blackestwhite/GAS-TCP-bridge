package client

import (
	"context"
	"io"
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
