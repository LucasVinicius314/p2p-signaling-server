package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// tagFor builds a friends tag the way the client library does: the lowercase hex of a SHA-256.
func tagFor(name string) string {
	sum := sha256.Sum256([]byte("test/" + name))
	return hex.EncodeToString(sum[:])
}

// listFor asks for the rooms of the given tags and returns the entries.
func (c *client) listFor(tags ...string) []any {
	c.t.Helper()
	c.send(msg{"t": "list_rooms", "tags": tags})
	m := c.expect("room_list")
	rooms, _ := m["rooms"].([]any)
	return rooms
}

func hostWithTag(t *testing.T, e *env, tag string) (*client, string) {
	t.Helper()
	host := e.connected(t)
	m := msg{"t": "create_room"}
	if tag != "" {
		m["host_tag"] = tag
	}
	host.send(m)
	return host, host.expect("room_created")["code"].(string)
}

func TestFriendsListing(t *testing.T) {
	e := setup(t, `{"name":"Game","limits":{"rooms_per_ip":0}}`) // all test clients share one address
	alice, code := hostWithTag(t, e, tagFor("alice"))
	_, plainCode := hostWithTag(t, e, "") // no tag: reachable by code only
	friend := e.connected(t)

	rooms := friend.listFor(tagFor("alice"))
	if len(rooms) != 1 {
		t.Fatalf("want one room for alice's tag, got %v", rooms)
	}
	r := rooms[0].(map[string]any)
	if r["code"] != code || r["tag"] != tagFor("alice") || int(r["players"].(float64)) != 1 || int(r["max_players"].(float64)) != 8 {
		t.Fatalf("listing: %v", r)
	}
	if strings.EqualFold(r["code"].(string), plainCode) {
		t.Fatal("an untagged room must never be listed")
	}

	// A stranger's tag lists nothing, and an empty list lists nothing.
	if rooms := friend.listFor(tagFor("bob")); len(rooms) != 0 {
		t.Fatalf("someone else's tag listed %v", rooms)
	}
	if rooms := friend.listFor(); len(rooms) != 0 {
		t.Fatalf("an empty list listed %v", rooms)
	}

	// Several tags at once, duplicates harmless.
	bob, _ := hostWithTag(t, e, tagFor("bob"))
	if rooms := friend.listFor(tagFor("alice"), tagFor("bob"), tagFor("alice")); len(rooms) != 2 {
		t.Fatalf("want two rooms, got %v", rooms)
	}

	// A locked room is not offered; unlocking brings it back.
	alice.send(msg{"t": "lock_room", "locked": true})
	if rooms := friend.listFor(tagFor("alice")); len(rooms) != 0 {
		t.Fatalf("a locked room was listed: %v", rooms)
	}
	alice.send(msg{"t": "lock_room", "locked": false})
	if rooms := friend.listFor(tagFor("alice")); len(rooms) != 1 {
		t.Fatalf("an unlocked room should be listed again, got %v", rooms)
	}

	// A host that leaves takes the room off the list.
	bob.send(msg{"t": "leave"})
	if rooms := friend.listFor(tagFor("bob")); len(rooms) != 0 {
		t.Fatalf("a closed room was listed: %v", rooms)
	}

	// The listed code really joins.
	friend.send(msg{"t": "join_room", "code": code})
	friend.expect("joined")
	alice.expect("peer_joined")
}

func TestFriendsListingFullRoomIsHidden(t *testing.T) {
	e := setup(t, `{"name":"Game","max_players":2}`)
	_, _ = hostWithTag(t, e, tagFor("alice"))
	friend := e.connected(t)
	rooms := friend.listFor(tagFor("alice"))
	if len(rooms) != 1 {
		t.Fatalf("want the room, got %v", rooms)
	}
	friend.send(msg{"t": "join_room", "code": rooms[0].(map[string]any)["code"]})
	friend.expect("joined")

	other := e.connected(t)
	if rooms := other.listFor(tagFor("alice")); len(rooms) != 0 {
		t.Fatalf("a full room was listed: %v", rooms)
	}
}

func TestFriendsTagValidation(t *testing.T) {
	e := setup(t, `{"name":"Game"}`)
	c := e.connected(t)

	for _, bad := range []string{"abc", strings.Repeat("A", 64), strings.Repeat("g", 64), tagFor("x")[:63]} {
		c.send(msg{"t": "create_room", "host_tag": bad})
		c.expectErr("bad_message")
		c.send(msg{"t": "list_rooms", "tags": []string{bad}})
		c.expectErr("bad_message")
	}

	many := make([]string, 101)
	for i := range many {
		many[i] = tagFor(string(rune('a' + i%26)))
	}
	c.send(msg{"t": "list_rooms", "tags": many})
	c.expectErr("bad_message")

	// After all that the connection still works.
	if rooms := c.listFor(tagFor("x")); len(rooms) != 0 {
		t.Fatalf("got %v", rooms)
	}
}

func TestFriendsListRateLimit(t *testing.T) {
	e := setup(t, `{"name":"Game","limits":{"messages_per_second":1000}}`)
	c := e.connected(t)
	for i := 0; i < 60; i++ {
		c.listFor(tagFor("x"))
	}
	c.send(msg{"t": "list_rooms", "tags": []string{tagFor("x")}})
	c.expectErr("rate_limited")
	// The connection is not dropped for it.
	c.send(msg{"t": "ping"})
	c.expect("pong")
}
