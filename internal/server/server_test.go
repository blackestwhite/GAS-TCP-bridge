package server

import "testing"

func TestWithDefaultsDialNetwork(t *testing.T) {
	cfg := withDefaults(Config{})
	if cfg.DialNetwork != "tcp" {
		t.Fatalf("DialNetwork=%q want tcp", cfg.DialNetwork)
	}
}
