// Package bucket defines per-game configuration, validation and the Store interface.
package bucket

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

type IceServer struct {
	URLs       []string `json:"urls" bson:"urls"`
	Username   string   `json:"username,omitempty" bson:"username,omitempty"`
	Credential string   `json:"credential,omitempty" bson:"credential,omitempty"`
}

type Limits struct {
	JoinsPerIPPerMinute      int `json:"joins_per_ip_per_minute" bson:"joins_per_ip_per_minute"`
	FailedJoinsBeforeLockout int `json:"failed_joins_before_lockout" bson:"failed_joins_before_lockout"`
	LockoutSeconds           int `json:"lockout_seconds" bson:"lockout_seconds"`
	RoomsPerIP               int `json:"rooms_per_ip" bson:"rooms_per_ip"`
	MessageBytes             int `json:"message_bytes" bson:"message_bytes"`
	MessagesPerSecond        int `json:"messages_per_second" bson:"messages_per_second"`
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
	PublicListing          bool        `json:"public_listing" bson:"public_listing"`
	AllowedOrigins         []string    `json:"allowed_origins" bson:"allowed_origins"`
	IceServers             []IceServer `json:"ice_servers" bson:"ice_servers"`
	Limits                 Limits      `json:"limits" bson:"limits"`
	RoomIdleTimeoutSeconds int         `json:"room_idle_timeout_seconds" bson:"room_idle_timeout_seconds"`
	Notes                  string      `json:"notes" bson:"notes"`
	CreatedAt              time.Time   `json:"created_at" bson:"created_at"`
	UpdatedAt              time.Time   `json:"updated_at" bson:"updated_at"`
}

var idRe = regexp.MustCompile(`^[a-z0-9-]{3,40}$`)

// ValidID reports whether id is a legal bucket id.
func ValidID(id string) bool { return idRe.MatchString(id) }

// Default returns a bucket with plan defaults for the given id and name.
func Default(id, name string) Bucket {
	return Bucket{
		ID: id, Name: name, Enabled: true, Topology: "star",
		MaxPlayers: 8, MaxRooms: 200, CodeLength: 5,
		AcceptedVersions: []string{}, AllowedOrigins: []string{},
		IceServers: []IceServer{{URLs: []string{"stun:stun.l.google.com:19302"}}},
		Limits: Limits{
			JoinsPerIPPerMinute: 10, FailedJoinsBeforeLockout: 5, LockoutSeconds: 300,
			RoomsPerIP: 2, MessageBytes: 16384, MessagesPerSecond: 30,
		},
		RoomIdleTimeoutSeconds: 600,
	}
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
	if b.MaxRooms < 1 {
		add("max_rooms must be at least 1")
	}
	if b.CodeLength < 4 || b.CodeLength > 8 {
		add("code_length must be 4 to 8")
	}
	hasTurn := false
	for i, s := range b.IceServers {
		if len(s.URLs) == 0 {
			add("ice_servers[%d]: urls is empty", i)
		}
		for _, u := range s.URLs {
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
	l := b.Limits
	if l.JoinsPerIPPerMinute < 1 || l.FailedJoinsBeforeLockout < 1 || l.LockoutSeconds < 1 ||
		l.RoomsPerIP < 1 || l.MessageBytes < 256 || l.MessagesPerSecond < 1 {
		add("limits must all be positive (message_bytes at least 256)")
	}
	if b.RoomIdleTimeoutSeconds < 10 {
		add("room_idle_timeout_seconds must be at least 10")
	}
	return errors.Join(errs...)
}

// VersionAccepted: an empty list accepts any version.
func (b Bucket) VersionAccepted(v string) bool {
	if len(b.AcceptedVersions) == 0 {
		return true
	}
	for _, a := range b.AcceptedVersions {
		if a == v {
			return true
		}
	}
	return false
}
