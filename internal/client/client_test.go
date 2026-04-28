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

	"gas-tcp-bridge/internal/protocol"
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

func TestFrontedRedirectPreservesMethodBodyAndToken(t *testing.T) {
	const sid = "redirect-session"
	const token = "test-token"

	var sawInitial bool
	var sawRedirectPost bool
	var sawRedirectGet bool
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/macros/s/test/exec":
			sawInitial = true
			if r.Host != "script.google.com" {
				t.Errorf("initial host=%q want script.google.com", r.Host)
			}
			http.Redirect(w, r, "http://script.googleusercontent.com/macros/echo", http.StatusFound)
		case "/macros/echo":
			if r.Host != "script.googleusercontent.com" {
				t.Errorf("redirect host=%q want script.googleusercontent.com", r.Host)
			}
			w.Header().Set("Content-Type", "application/json")
			switch r.Method {
			case http.MethodPost:
				sawRedirectPost = true
				if r.Header.Get("X-Bridge-Token") != token {
					t.Errorf("redirect token=%q want %q", r.Header.Get("X-Bridge-Token"), token)
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read redirect body: %v", err)
				}
				if !strings.Contains(string(body), `"sid":"`+sid+`"`) {
					t.Errorf("redirect body %q does not contain sid %q", body, sid)
				}
				_, _ = w.Write([]byte(`{"sid":"` + sid + `","ack":1}`))
			case http.MethodGet:
				sawRedirectGet = true
				if r.Header.Get("X-Bridge-Token") != token {
					t.Errorf("redirect token=%q want %q", r.Header.Get("X-Bridge-Token"), token)
				}
				_, _ = w.Write([]byte(`{"sid":"` + sid + `","ack":1,"chunks":[]}`))
			default:
				t.Errorf("redirect method=%s want POST or GET", r.Method)
				http.Error(w, "bad method", http.StatusMethodNotAllowed)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(front.Close)

	relayURL, err := url.Parse("http://blocked.invalid/macros/s/test/exec")
	if err != nil {
		t.Fatalf("parse relay url: %v", err)
	}
	cfg := withDefaults(Config{
		RelayURL:  relayURL.String(),
		Token:     token,
		FrontDial: front.Listener.Addr().String(),
		FrontHost: "script.google.com",
	})
	httpClient := newHTTPClient(cfg, relayURL)
	st := &connState{
		client: &Client{cfg: cfg, http: httpClient},
		sid:    sid,
		ctx:    context.Background(),
	}

	ack, err := st.postUp(protocol.Message{SID: sid, Seq: 1, Type: protocol.TypeOpen})
	if err != nil {
		t.Fatalf("postUp: %v", err)
	}
	if ack.Ack != 1 {
		t.Fatalf("ack=%d want 1", ack.Ack)
	}
	down, err := st.getDown()
	if err != nil {
		t.Fatalf("getDown: %v", err)
	}
	if down.Ack != 1 {
		t.Fatalf("down ack=%d want 1", down.Ack)
	}
	if !sawInitial || !sawRedirectPost || !sawRedirectGet {
		t.Fatalf("sawInitial=%v sawRedirectPost=%v sawRedirectGet=%v, want all", sawInitial, sawRedirectPost, sawRedirectGet)
	}
}
