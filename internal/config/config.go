// Package config parses and validates environment configuration.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strconv"
	"strings"
)

const minKeyLen = 32

type Config struct {
	MasterAPIKey      string
	MongoURI          string
	Port              int
	TrustedProxyCIDRs []netip.Prefix
	LogLevel          slog.Level
}

// Load reads configuration through getenv (os.Getenv in production) and fails fast on invalid values.
func Load(getenv func(string) string) (Config, error) {
	var errs []error
	c := Config{
		MasterAPIKey: getenv("MASTER_API_KEY"),
		MongoURI:     getenv("MONGODB_URI"),
		Port:         8080,
	}
	if c.MongoURI == "" {
		errs = append(errs, errors.New("MONGODB_URI is required"))
	}
	if len(c.MasterAPIKey) < minKeyLen {
		errs = append(errs, fmt.Errorf("MASTER_API_KEY must be set and at least %d characters", minKeyLen))
	}
	if v := getenv("PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil || p < 1 || p > 65535 {
			errs = append(errs, fmt.Errorf("PORT %q is not a valid port", v))
		} else {
			c.Port = p
		}
	}
	for _, s := range strings.Split(getenv("TRUSTED_PROXY_CIDRS"), ",") {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		p, err := netip.ParsePrefix(s)
		if err != nil {
			errs = append(errs, fmt.Errorf("TRUSTED_PROXY_CIDRS entry %q: %w", s, err))
			continue
		}
		c.TrustedProxyCIDRs = append(c.TrustedProxyCIDRs, p)
	}
	if err := c.LogLevel.UnmarshalText([]byte(orDefault(getenv("LOG_LEVEL"), "info"))); err != nil {
		errs = append(errs, fmt.Errorf("LOG_LEVEL: %w", err))
	}
	return c, errors.Join(errs...)
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}
