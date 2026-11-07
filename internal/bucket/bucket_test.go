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
