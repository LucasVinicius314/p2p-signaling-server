// Package bucket defines per-game configuration, validation and the Store interface.
package bucket

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// RedactedMark replaces ICE server credentials in everything that leaves the admin surface (REST, MCP,
// the audit log). Players still receive the real credential in the welcome message.
const RedactedMark = "********"

type IceServer struct {
	URLs       []string `json:"urls" bson:"urls"`
	Username   string   `json:"username,omitempty" bson:"username,omitempty"`
	Credential string   `json:"credential,omitempty" bson:"credential,omitempty"`
}

// Limits are the per-bucket abuse limits. A "per address" limit counts an IPv4 address, or a whole
// IPv6 /64, as one address.
type Limits struct {
	JoinsPerIPPerMinute int `json:"joins_per_ip_per_minute" bson:"joins_per_ip_per_minute"`
	// FailedJoinsPerConnection: wrong codes one connection may try before it is dropped.
	FailedJoinsPerConnection int `json:"failed_joins_per_connection" bson:"failed_joins_per_connection"`
	// FailedJoinsBeforeLockout: wrong codes from one address, within LockoutSeconds, before that address is
	// locked out. Kept well above the per-connection figure so a few typos behind a shared NAT cannot lock
	// everyone out; one persistent guesser reaches it quickly.
	FailedJoinsBeforeLockout int `json:"failed_joins_before_lockout" bson:"failed_joins_before_lockout"`
	LockoutSeconds           int `json:"lockout_seconds" bson:"lockout_seconds"`
	// FailedJoinsPerMinute: wrong codes the whole bucket tolerates per minute from all addresses together;
	// beyond it joins are refused until the budget refills. Backstop against distributed guessing.
	FailedJoinsPerMinute int `json:"failed_joins_per_minute" bson:"failed_joins_per_minute"`
	// RoomsPerIP: rooms one address may host at once. 0 = unlimited.
	RoomsPerIP        int `json:"rooms_per_ip" bson:"rooms_per_ip"`
	MessageBytes      int `json:"message_bytes" bson:"message_bytes"`
	MessagesPerSecond int `json:"messages_per_second" bson:"messages_per_second"`
}

type Bucket struct {
	ID                     string      `json:"id" bson:"_id"`
	Name                   string      `json:"name" bson:"name"`
	Enabled                bool        `json:"enabled" bson:"enabled"`
	Topology               string      `json:"topology" bson:"topology"`
	MaxPlayers             int         `json:"max_players" bson:"max_players"`
	MaxRooms               int         `json:"max_rooms" bson:"max_rooms"`
	CodeLength             int         `json:"code_length" bson:"code_length"`
	AcceptedVersions       []string    `json:"accepted_versions" bson:"accepted_versions"`
	HostApproval           bool        `json:"host_approval" bson:"host_approval"`
	RelayOnly              bool        `json:"relay_only" bson:"relay_only"`
	AllowedOrigins         []string    `json:"allowed_origins" bson:"allowed_origins"`
	IceServers             []IceServer `json:"ice_servers" bson:"ice_servers"`
	Limits                 Limits      `json:"limits" bson:"limits"`
	RoomIdleTimeoutSeconds int         `json:"room_idle_timeout_seconds" bson:"room_idle_timeout_seconds"`
	Notes                  string      `json:"notes" bson:"notes"`
	// Revision counts saved changes. Updates are applied only to the revision they were based on, so two
	// edits at once cannot silently overwrite each other. Read-only for callers.
	Revision  int64     `json:"revision" bson:"revision"`
	CreatedAt time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt time.Time `json:"updated_at" bson:"updated_at"`
}

var idRe = regexp.MustCompile(`^[a-z0-9-]{3,40}$`)

// ValidID reports whether id is a legal bucket id.
func ValidID(id string) bool { return idRe.MatchString(id) }

// Bounds enforced by Validate.
const (
	maxNameLen        = 100
	maxNotesLen       = 1000
	maxVersions       = 100
	maxVersionLen     = 64
	maxOrigins        = 50
	maxIceServers     = 10
	maxIceURLs        = 10
	maxIceURLLen      = 512
	maxIceSecretLen   = 256
	maxRoomsCeiling   = 100_000
	maxMessageBytes   = 1 << 20
	maxIdleTimeoutSec = 24 * 60 * 60
	codeAlphabetSize  = 32 // see room.Alphabet
)

// Default returns a bucket with the standard defaults for the given id and name.
func Default(id, name string) Bucket {
	return Bucket{
		ID: id, Name: name, Enabled: true, Topology: "star",
		MaxPlayers: 8, MaxRooms: 200, CodeLength: 6,
		AcceptedVersions: []string{}, AllowedOrigins: []string{},
		IceServers: []IceServer{{URLs: []string{"stun:stun.l.google.com:19302"}}},
		Limits: Limits{
			JoinsPerIPPerMinute: 10, FailedJoinsPerConnection: 5, FailedJoinsBeforeLockout: 20,
			LockoutSeconds: 300, FailedJoinsPerMinute: 120,
			RoomsPerIP: 2, MessageBytes: 16384, MessagesPerSecond: 30,
		},
		RoomIdleTimeoutSeconds: 600,
	}
}

// Normalize fills limits that did not exist when a stored bucket was saved, so older documents keep working.
func (b Bucket) Normalize() Bucket {
	d := Default(b.ID, b.Name).Limits
	if b.Limits.FailedJoinsPerConnection == 0 {
		b.Limits.FailedJoinsPerConnection = d.FailedJoinsPerConnection
	}
	if b.Limits.FailedJoinsPerMinute == 0 {
		b.Limits.FailedJoinsPerMinute = d.FailedJoinsPerMinute
	}
	return b
}

// Redacted returns a copy that is safe to show an administrator or write to a log: credentials are masked.
func (b Bucket) Redacted() Bucket {
	servers := make([]IceServer, len(b.IceServers))
	for i, s := range b.IceServers {
		if s.Credential != "" {
			s.Credential = RedactedMark
		}
		servers[i] = s
	}
	b.IceServers = servers
	return b
}

// restoreCredentials puts real credentials back where an update echoes the redacted mark, matching servers
// by their URLs and username. An echoed mark with nothing to restore is an error.
func restoreCredentials(old, updated []IceServer) error {
	key := func(s IceServer) string { return strings.Join(s.URLs, ",") + "|" + s.Username }
	known := map[string]string{}
	for _, s := range old {
		known[key(s)] = s.Credential
	}
	for i, s := range updated {
		if s.Credential != RedactedMark {
			continue
		}
		cred, ok := known[key(s)]
		if !ok || cred == "" {
			return fmt.Errorf("ice_servers[%d]: credential is the redacted placeholder; send the real credential", i)
		}
		updated[i].Credential = cred
	}
	return nil
}

// Validate checks the whole bucket.
func (b Bucket) Validate() error {
	var errs []error
	add := func(f string, a ...any) { errs = append(errs, fmt.Errorf(f, a...)) }
	if !ValidID(b.ID) {
		add("id must match [a-z0-9-]{3,40}")
	}
	if strings.TrimSpace(b.Name) == "" {
		add("name is required")
	}
	if len(b.Name) > maxNameLen {
		add("name must be at most %d characters", maxNameLen)
	}
	if len(b.Notes) > maxNotesLen {
		add("notes must be at most %d characters", maxNotesLen)
	}
	switch b.Topology {
	case "star":
		if b.MaxPlayers < 2 || b.MaxPlayers > 16 {
			add("max_players must be 2 to 16")
		}
	case "mesh":
		if b.MaxPlayers < 2 || b.MaxPlayers > 8 {
			add("max_players must be 2 to 8 for mesh")
		}
	default:
		add("topology must be star or mesh")
	}
	if b.CodeLength < 4 || b.CodeLength > 8 {
		add("code_length must be 4 to 8")
	} else {
		// Keep live rooms a small fraction of the code space: finding a free code stays cheap and a
		// random guess stays unlikely to hit a room.
		space := 1
		for i := 0; i < b.CodeLength; i++ {
			space *= codeAlphabetSize
		}
		if limit := min(maxRoomsCeiling, space/100); b.MaxRooms < 1 || b.MaxRooms > limit {
			add("max_rooms must be 1 to %d for code_length %d (at most 1%% of the %d possible codes)", limit, b.CodeLength, space)
		}
	}
	b.validateVersions(add)
	b.validateOrigins(add)
	b.validateIceServers(add)
	b.validateLimits(add)
	if b.RoomIdleTimeoutSeconds < 10 || b.RoomIdleTimeoutSeconds > maxIdleTimeoutSec {
		add("room_idle_timeout_seconds must be 10 to %d", maxIdleTimeoutSec)
	}
	return errors.Join(errs...)
}

func (b Bucket) validateVersions(add func(string, ...any)) {
	if len(b.AcceptedVersions) > maxVersions {
		add("accepted_versions has more than %d entries", maxVersions)
	}
	seen := map[string]bool{}
	for i, v := range b.AcceptedVersions {
		switch {
		case strings.TrimSpace(v) == "" || len(v) > maxVersionLen:
			add("accepted_versions[%d] must be 1 to %d characters", i, maxVersionLen)
		case seen[v]:
			add("accepted_versions[%d]: %q is listed twice", i, v)
		}
		seen[v] = true
	}
}

// validateOrigins requires the browser Origin form: scheme://host[:port], lowercase, no path.
func (b Bucket) validateOrigins(add func(string, ...any)) {
	if len(b.AllowedOrigins) > maxOrigins {
		add("allowed_origins has more than %d entries", maxOrigins)
	}
	for i, o := range b.AllowedOrigins {
		u, err := url.Parse(o)
		ok := err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" &&
			u.Path == "" && u.RawQuery == "" && u.Fragment == "" && u.User == nil &&
			o == strings.ToLower(u.Scheme)+"://"+strings.ToLower(u.Host)
		if !ok {
			add("allowed_origins[%d]: %q must look like https://example.com or http://localhost:8060 (lowercase, no path, no trailing slash)", i, o)
		}
	}
}

func (b Bucket) validateIceServers(add func(string, ...any)) {
	if len(b.IceServers) > maxIceServers {
		add("ice_servers has more than %d entries", maxIceServers)
	}
	hasTurn := false
	for i, s := range b.IceServers {
		if len(s.URLs) == 0 {
			add("ice_servers[%d]: urls is empty", i)
		}
		if len(s.URLs) > maxIceURLs {
			add("ice_servers[%d]: more than %d urls", i, maxIceURLs)
		}
		if len(s.Username) > maxIceSecretLen || len(s.Credential) > maxIceSecretLen {
			add("ice_servers[%d]: username and credential must be at most %d characters", i, maxIceSecretLen)
		}
		if s.Credential == RedactedMark {
			add("ice_servers[%d]: credential is the redacted placeholder; send the real credential", i)
		}
		for _, u := range s.URLs {
			if len(u) > maxIceURLLen {
				add("ice_servers[%d]: a url is longer than %d characters", i, maxIceURLLen)
				continue
			}
			scheme, host, _ := strings.Cut(u, ":")
			switch scheme {
			case "stun", "stuns":
			case "turn", "turns":
				hasTurn = true
			default:
				add("ice_servers[%d]: %q must start with stun:, stuns:, turn: or turns:", i, u)
				continue
			}
			if host == "" {
				add("ice_servers[%d]: %q has no host", i, u)
			}
		}
	}
	if b.RelayOnly && !hasTurn {
		add("relay_only requires at least one turn: or turns: ice server")
	}
}

func (b Bucket) validateLimits(add func(string, ...any)) {
	l := b.Limits
	span := func(name string, v, lo, hi int) {
		if v < lo || v > hi {
			add("limits.%s must be %d to %d", name, lo, hi)
		}
	}
	span("joins_per_ip_per_minute", l.JoinsPerIPPerMinute, 1, 1000)
	span("failed_joins_per_connection", l.FailedJoinsPerConnection, 1, 100)
	span("failed_joins_before_lockout", l.FailedJoinsBeforeLockout, 1, 1000)
	span("lockout_seconds", l.LockoutSeconds, 1, 24*60*60)
	span("failed_joins_per_minute", l.FailedJoinsPerMinute, 1, 10000)
	span("rooms_per_ip", l.RoomsPerIP, 0, 1000)
	span("message_bytes", l.MessageBytes, 256, maxMessageBytes)
	span("messages_per_second", l.MessagesPerSecond, 1, 1000)
}
