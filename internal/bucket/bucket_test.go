package bucket

import "testing"

func TestDefaultIsValid(t *testing.T) {
	if err := Default("my-game", "My Game").Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidate(t *testing.T) {
	cases := map[string]func(*Bucket){
		"bad topology":       func(b *Bucket) { b.Topology = "ring" },
		"mesh over 8":        func(b *Bucket) { b.Topology = "mesh"; b.MaxPlayers = 9 },
		"star over 16":       func(b *Bucket) { b.MaxPlayers = 17 },
		"short code":         func(b *Bucket) { b.CodeLength = 3 },
		"bad ice scheme":     func(b *Bucket) { b.IceServers = []IceServer{{URLs: []string{"http://x"}}} },
		"relay without turn": func(b *Bucket) { b.RelayOnly = true },
		"tiny messages":      func(b *Bucket) { b.Limits.MessageBytes = 10 },
		"bad id":             func(b *Bucket) { b.ID = "A" },
	}
	for name, mut := range cases {
		b := Default("game", "Game")
		mut(&b)
		if b.Validate() == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	b := Default("game", "Game")
	b.RelayOnly = true
	b.IceServers = append(b.IceServers, IceServer{URLs: []string{"turn:t.example.com:3478"}})
	if err := b.Validate(); err != nil {
		t.Errorf("relay with turn: %v", err)
	}
}

func TestVersionAccepted(t *testing.T) {
	b := Default("game", "Game")
	if !b.VersionAccepted("9") {
		t.Error("empty list should accept any")
	}
	b.AcceptedVersions = []string{"1"}
	if b.VersionAccepted("2") || !b.VersionAccepted("1") {
		t.Error("list not enforced")
	}
}

func TestValidateRejectsOutOfBoundsAndSloppyConfig(t *testing.T) {
	cases := map[string]func(*Bucket){
		"name too long":          func(b *Bucket) { b.Name = string(make([]byte, 101)) },
		"notes too long":         func(b *Bucket) { b.Notes = string(make([]byte, 1001)) },
		"max_rooms beyond codes": func(b *Bucket) { b.CodeLength = 4; b.MaxRooms = 20000 }, // 32^4/100 = 10485
		"max_rooms zero":         func(b *Bucket) { b.MaxRooms = 0 },
		"max_rooms huge":         func(b *Bucket) { b.MaxRooms = 1_000_000 },
		"duplicate version":      func(b *Bucket) { b.AcceptedVersions = []string{"1.0", "1.0"} },
		"blank version":          func(b *Bucket) { b.AcceptedVersions = []string{" "} },
		"origin with path":       func(b *Bucket) { b.AllowedOrigins = []string{"https://example.com/game"} },
		"origin trailing slash":  func(b *Bucket) { b.AllowedOrigins = []string{"https://example.com/"} },
		"origin without scheme":  func(b *Bucket) { b.AllowedOrigins = []string{"example.com"} },
		"origin uppercase":       func(b *Bucket) { b.AllowedOrigins = []string{"https://Example.com"} },
		"too many ice servers":   func(b *Bucket) { b.IceServers = make([]IceServer, 11) },
		"huge credential": func(b *Bucket) {
			b.IceServers = []IceServer{{URLs: []string{"turn:t:1"}, Credential: string(make([]byte, 300))}}
		},
		"redacted mark as secret": func(b *Bucket) { b.IceServers = []IceServer{{URLs: []string{"turn:t:1"}, Credential: RedactedMark}} },
		"message_bytes over 1MiB": func(b *Bucket) { b.Limits.MessageBytes = 2 << 20 },
		"rate over 1000":          func(b *Bucket) { b.Limits.MessagesPerSecond = 5000 },
		"negative rooms_per_ip":   func(b *Bucket) { b.Limits.RoomsPerIP = -1 },
		"lockout over a day":      func(b *Bucket) { b.Limits.LockoutSeconds = 100000 },
		"per-connection too high": func(b *Bucket) { b.Limits.FailedJoinsPerConnection = 101 },
		"idle timeout over a day": func(b *Bucket) { b.RoomIdleTimeoutSeconds = 100000 },
		"bucket budget missing":   func(b *Bucket) { b.Limits.FailedJoinsPerMinute = 0 },
		"per-connection missing":  func(b *Bucket) { b.Limits.FailedJoinsPerConnection = 0 },
	}
	for name, mut := range cases {
		b := Default("game", "Game")
		mut(&b)
		if b.Validate() == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}

	ok := map[string]func(*Bucket){
		"rooms_per_ip zero means unlimited": func(b *Bucket) { b.Limits.RoomsPerIP = 0 },
		"localhost origin with port":        func(b *Bucket) { b.AllowedOrigins = []string{"http://localhost:8060"} },
		"https origin":                      func(b *Bucket) { b.AllowedOrigins = []string{"https://play.example.com"} },
		"4 character codes, few rooms":      func(b *Bucket) { b.CodeLength = 4; b.MaxRooms = 1000 },
	}
	for name, mut := range ok {
		b := Default("game", "Game")
		mut(&b)
		if err := b.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestNormalizeFillsLimitsAddedAfterABucketWasSaved(t *testing.T) {
	legacy := Default("old-game", "Old")
	legacy.Limits.FailedJoinsPerConnection = 0
	legacy.Limits.FailedJoinsPerMinute = 0
	legacy.Limits.FailedJoinsBeforeLockout = 5 // saved earlier: kept as it was
	n := legacy.Normalize()
	if n.Limits.FailedJoinsPerConnection != 5 || n.Limits.FailedJoinsPerMinute != 120 {
		t.Fatalf("new limits not filled: %+v", n.Limits)
	}
	if n.Limits.FailedJoinsBeforeLockout != 5 {
		t.Fatalf("an existing setting must not change: %+v", n.Limits)
	}
	if err := n.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestRedactedHidesCredentialsWithoutTouchingTheOriginal(t *testing.T) {
	b := Default("game", "Game")
	b.IceServers = []IceServer{
		{URLs: []string{"stun:s.example.com"}},
		{URLs: []string{"turn:t.example.com:3478"}, Username: "u", Credential: "s3cret"},
	}
	r := b.Redacted()
	if r.IceServers[1].Credential != RedactedMark || r.IceServers[0].Credential != "" {
		t.Fatalf("redacted: %+v", r.IceServers)
	}
	if b.IceServers[1].Credential != "s3cret" {
		t.Fatal("Redacted must not modify the original")
	}
}

func TestCloneSharesNothing(t *testing.T) {
	b := Default("game", "Game")
	b.AcceptedVersions = []string{"1"}
	c := b.Clone()
	c.AcceptedVersions[0] = "2"
	c.IceServers[0].URLs[0] = "stun:other"
	if b.AcceptedVersions[0] != "1" || b.IceServers[0].URLs[0] != "stun:stun.l.google.com:19302" {
		t.Fatal("a clone must not share slices with the original")
	}
}
