// Package signaling implements the WebSocket wire protocol (version 1).
package signaling

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

const ProtocolVersion = 1

// Error codes (section 6.3 of the plan).
const (
	ErrBadMessage      = "bad_message"
	ErrUnsupportedProt = "unsupported_protocol"
	ErrBucketNotFound  = "bucket_not_found"
	ErrBucketDisabled  = "bucket_disabled"
	ErrOriginRejected  = "origin_rejected"
	ErrRoomNotFound    = "room_not_found"
	ErrRoomFull        = "room_full"
	ErrRoomLocked      = "room_locked"
	ErrNotHost         = "not_host"
	ErrJoinDenied      = "join_denied"
	ErrLockedOut       = "locked_out"
	ErrRateLimited     = "rate_limited"
	ErrServerBusy      = "server_busy"
)

// Client -> server.

type hello struct {
	T           string `json:"t"`
	Bucket      string `json:"bucket"`
	GameVersion string `json:"game_version"`
	Protocol    int    `json:"protocol"`
	Name        string `json:"name"`
}

type createRoom struct {
	T          string `json:"t"`
	MaxPlayers int    `json:"max_players"`
}

type joinRoom struct {
	T    string `json:"t"`
	Code string `json:"code"`
}

type approveMsg struct {
	T      string `json:"t"`
	Peer   int32  `json:"peer"`
	Accept *bool  `json:"accept"`
}

type lockMsg struct {
	T      string `json:"t"`
	Locked *bool  `json:"locked"`
}

type signalIn struct {
	T    string          `json:"t"`
	To   int32           `json:"to"`
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data"`
}

type bare struct {
	T string `json:"t"`
}

// Server -> client.

type welcome struct {
	T          string      `json:"t"`
	IceServers any         `json:"ice_servers"`
	IcePolicy  string      `json:"ice_policy"`
	Topology   string      `json:"topology"`
	Limits     welcomeLims `json:"limits"`
	Protocol   int         `json:"protocol"`
}

type welcomeLims struct {
	MessageBytes      int `json:"message_bytes"`
	MessagesPerSecond int `json:"messages_per_second"`
	MaxPlayers        int `json:"max_players"`
}

type roomCreated struct {
	T      string `json:"t"`
	Code   string `json:"code"`
	PeerID int32  `json:"peer_id"`
}

type joined struct {
	T      string  `json:"t"`
	PeerID int32   `json:"peer_id"`
	HostID int32   `json:"host_id"`
	Peers  []int32 `json:"peers"`
}

type peerEvent struct {
	T      string `json:"t"` // peer_joined | peer_left
	PeerID int32  `json:"peer_id"`
}

type signalOut struct {
	T    string          `json:"t"`
	From int32           `json:"from"`
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data"`
}

type joinRequest struct {
	T    string `json:"t"`
	Peer int32  `json:"peer"`
	Name string `json:"name,omitempty"`
}

type joinCancelled struct {
	T    string `json:"t"`
	Peer int32  `json:"peer"`
}

// serverRestarting is sent to every client when the server shuts down; reconnect after RetryAfterMs.
type serverRestarting struct {
	T            string `json:"t"`
	RetryAfterMs int64  `json:"retry_after_ms"`
}

type roomClosed struct {
	T      string `json:"t"`
	Reason string `json:"reason"`
}

type errorMsg struct {
	T       string `json:"t"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type simple struct {
	T string `json:"t"`
}

func newError(code, msg string) errorMsg { return errorMsg{T: "error", Code: code, Message: msg} }

// decode reads the message type, then strictly decodes into the matching struct
// (unknown fields are rejected). typed returns a pointer to the message struct for a type.
func decode(raw []byte) (t string, msg any, err error) {
	var b bare
	if err := json.Unmarshal(raw, &b); err != nil {
		return "", nil, errors.New("not a JSON object")
	}
	switch b.T {
	case "hello":
		msg = &hello{}
	case "create_room":
		msg = &createRoom{}
	case "join_room":
		msg = &joinRoom{}
	case "signal":
		msg = &signalIn{}
	case "approve":
		msg = &approveMsg{}
	case "lock_room":
		msg = &lockMsg{}
	case "leave", "ping":
		msg = &bare{}
	default:
		return "", nil, fmt.Errorf("unknown message type")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(msg); err != nil {
		return "", nil, errors.New("malformed message")
	}
	if dec.More() {
		return "", nil, errors.New("trailing data")
	}
	return b.T, msg, nil
}
