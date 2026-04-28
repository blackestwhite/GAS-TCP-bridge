package client

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

func normalizeFrontConfig(cfg *Config, relayURL *url.URL) error {
	if cfg.FrontDial == "" {
		return nil
	}

	defaultPort := "443"
	if relayURL.Scheme == "http" {
		defaultPort = "80"
	}
	host, addr, err := normalizeHostPort(cfg.FrontDial, defaultPort)
	if err != nil {
		return err
	}
	cfg.FrontDial = addr
	if cfg.FrontSNI == "" {
		cfg.FrontSNI = host
	}
	return nil
}

func newHTTPClient(cfg Config, relayURL *url.URL) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = cfg.RequestTimeout

	if cfg.FrontDial != "" {
		dialer := &net.Dialer{Timeout: cfg.RequestTimeout}
		frontDial := cfg.FrontDial
		tr.Proxy = nil
		tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, frontDial)
		}

		if relayURL.Scheme == "https" {
			tr.TLSClientConfig = &tls.Config{
				MinVersion: tls.VersionTLS12,
				ServerName: cfg.FrontSNI,
			}
		}
		if cfg.FrontForceHTTP1 {
			tr.ForceAttemptHTTP2 = false
			tr.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
		}
	}

	var rt http.RoundTripper = tr
	if cfg.FrontHost != "" {
		rt = hostRewriteTransport{
			next:     rt,
			baseHost: relayURL.Host,
			host:     cfg.FrontHost,
		}
	}

	return &http.Client{
		Timeout:       cfg.RequestTimeout,
		Transport:     rt,
		CheckRedirect: preserveRelayRedirect,
	}
}

func preserveRelayRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("stopped after %d redirects", len(via))
	}
	prev := via[len(via)-1]

	req.Method = prev.Method
	req.Header = prev.Header.Clone()
	req.Host = ""
	req.ContentLength = prev.ContentLength
	req.GetBody = prev.GetBody

	if prev.GetBody != nil {
		body, err := prev.GetBody()
		if err != nil {
			return err
		}
		req.Body = body
		return nil
	}
	if prev.Body == nil || prev.Body == http.NoBody {
		req.Body = nil
		return nil
	}
	return errors.New("cannot replay redirected request body")
}

type hostRewriteTransport struct {
	next     http.RoundTripper
	baseHost string
	host     string
}

func (t hostRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	if sameURLHost(clone.URL.Host, t.baseHost) {
		clone.Host = t.host
	}
	return t.next.RoundTrip(clone)
}

func normalizeHostPort(value string, defaultPort string) (string, string, error) {
	host, port, err := net.SplitHostPort(value)
	if err == nil {
		if host == "" || port == "" {
			return "", "", fmt.Errorf("invalid front dial address %q", value)
		}
		return strings.Trim(host, "[]"), net.JoinHostPort(host, port), nil
	}

	if strings.Contains(value, "]:") {
		return "", "", fmt.Errorf("invalid front dial address %q: %w", value, err)
	}
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		host = strings.Trim(value, "[]")
		return host, net.JoinHostPort(host, defaultPort), nil
	}
	if strings.Count(value, ":") > 1 {
		host = value
		return host, net.JoinHostPort(host, defaultPort), nil
	}
	if strings.Contains(value, ":") {
		host, port, splitErr := net.SplitHostPort(value)
		if splitErr != nil || host == "" || port == "" {
			return "", "", fmt.Errorf("invalid front dial address %q", value)
		}
	}
	host = value
	return host, net.JoinHostPort(host, defaultPort), nil
}

func sameURLHost(a, b string) bool {
	return strings.EqualFold(a, b)
}
