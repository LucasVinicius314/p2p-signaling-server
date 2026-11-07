package config

import (
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestRejectsWeakKey(t *testing.T) {
	_, err := Load(env(map[string]string{"MASTER_API_KEY": "short", "MONGODB_URI": "mongodb://x"}))
	if err == nil || !strings.Contains(err.Error(), "MASTER_API_KEY") {
		t.Fatalf("want key error, got %v", err)
	}
}

func TestDefaultsAndValid(t *testing.T) {
	c, err := Load(env(map[string]string{
		"MASTER_API_KEY": strings.Repeat("k", 32), "MONGODB_URI": "mongodb://x",
		"TRUSTED_PROXY_CIDRS": "10.0.0.0/8, 172.16.0.0/12",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Port != 8080 || len(c.TrustedProxyCIDRs) != 2 {
		t.Fatalf("unexpected config %+v", c)
	}
}

func TestBadValues(t *testing.T) {
	_, err := Load(env(map[string]string{
		"MASTER_API_KEY": strings.Repeat("k", 32), "MONGODB_URI": "m",
		"PORT": "99999", "TRUSTED_PROXY_CIDRS": "nope", "LOG_LEVEL": "loud",
	}))
	if err == nil {
		t.Fatal("expected errors")
	}
}
