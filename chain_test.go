package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestChains(t *testing.T) {
	c, err := newCoordinator(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer c.ledger.f.Close()
	const model = "gemma4:12b"
	c.Blocks[model] = 6
	secret := strings.Repeat("ab", 16)
	holds := func(key string, first, last int) {
		line := fmt.Sprintf("10.0.0.%s:50052|relay:r.example:8444=%d-%d#%s", key, first, last, secret)
		if !validLine(line) {
			t.Fatalf("line refused: %s", line)
		}
		c.blockNodes[key] = &blockNode{key: key, hold: &Hold{model, first, last, line}, seen: time.Now()}
	}
	// chainOf returns the chain as "holder:first-last" items, after checking it covers every block once.
	chainOf := func() string {
		c.chainsLocked()
		ch := c.chains[model]
		if ch == nil {
			return ""
		}
		var parts []string
		next := 1
		for _, l := range ch.links {
			if l.first != next {
				t.Fatalf("chain has a hole or an overlap at block %d: %+v", next, ch.links)
			}
			next = l.last + 1
			parts = append(parts, fmt.Sprintf("%s:%d-%d", l.key, l.first, l.last))
		}
		if next != 7 {
			t.Fatalf("chain ends at block %d: %+v", next-1, ch.links)
		}
		return ch.head + " " + strings.Join(parts, " ")
	}

	// Holders alone make no chain: a head is needed.
	holds("1", 1, 3)
	holds("2", 4, 6)
	if got := chainOf(); got != "" {
		t.Fatalf("chain without a head: %s", got)
	}
	// A head, but one holder for each block where two are asked: still nothing.
	c.blockNodes["h"] = &blockNode{key: "h", head: model, seen: time.Now()}
	if got := chainOf(); got != "" {
		t.Fatalf("chain with one holder for each block: %s", got)
	}
	// Two holders for each block: the chain is made, each holder taken as far as it goes.
	holds("3", 1, 2)
	holds("4", 3, 6)
	first := chainOf()
	if first != "h 1:1-3 2:4-6" {
		t.Fatalf("chain: %q", first)
	}
	// The line a holder gets in a chain names the blocks it was given, with its routes and secret.
	if got := lineFor(c.blockNodes["4"].hold.Line, 3, 3); got != "10.0.0.4:50052|relay:r.example:8444=3-3#"+secret {
		t.Fatalf("line: %s", got)
	}
	// Nothing changed: the chain stays as it is.
	if got := chainOf(); got != first {
		t.Fatalf("chain changed for no reason: %q", got)
	}
	// A holder of the chain leaves: the others take over, with the same head, though one block has one holder left.
	delete(c.blockNodes, "1")
	if got := chainOf(); got != "h 3:1-2 4:3-6" {
		t.Fatalf("chain after a holder left: %q", got)
	}
	// A machine that went silent has left too. Block 1 has no holder then: no chain.
	c.blockNodes["3"].seen = time.Now().Add(-2 * blocksAlive)
	if got := chainOf(); got != "" {
		t.Fatalf("chain with a block nobody holds: %q", got)
	}
	// What a machine announces is checked.
	for _, bad := range []string{"", "10.0.0.1:50052=1-2", "10.0.0.1:50052=1-2#abc", "10.0.0.1:50052#" + secret, "a=1-2#" + secret + ",b=3#" + secret} {
		if validLine(bad) {
			t.Fatalf("line accepted: %q", bad)
		}
	}
	// The network says what is covered. A machine came back, but blocks 1 and 2 have one holder
	// where a new chain asks for two.
	holds("1", 1, 3)
	chainOf()
	c.mu.Lock()
	net := c.networkLocked()
	c.mu.Unlock()
	for _, m := range net.Models {
		if m.ID == model && (m.Blocks != 6 || fmt.Sprint(m.Cover) != "[1 1 2 2 2 2]" || m.Heads != 1 || m.Chained) {
			t.Fatalf("network: %+v", m)
		}
	}
}
