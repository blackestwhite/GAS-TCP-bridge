package server

import "testing"

func TestWithDefaultsDialNetwork(t *testing.T) {
	cfg := withDefaults(Config{})
	if cfg.DialNetwork != "tcp4" {
		t.Fatalf("DialNetwork=%q want tcp4", cfg.DialNetwork)
	}
}
