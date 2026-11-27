package main

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"
)

// testWorld is a coordinator with one head and two holders for each of two blocks. A test job
// through the chain fails when a machine listed in noisy is in it.
func testWorld(t *testing.T) (c *Coordinator, model string, test func(noisy string, unlucky bool) bool) {
	c, err := newCoordinator(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.ledger.f.Close() })
	model = "llama3.2:3b"
	c.Blocks[model], c.HoldersPerBlock = 2, 1
	secret := strings.Repeat("cd", 16)
	for i, name := range []string{"a1", "a2", "b1", "b2"} {
		block := 1 + i/2
		c.blockNodes[name] = &blockNode{key: name, seen: time.Now(),
			hold: &Hold{model, block, block, fmt.Sprintf("10.0.0.%d:50052=%d-%d#%s", i+1, block, block, secret)}}
	}
	c.blockNodes["head"] = &blockNode{key: "head", head: model, seen: time.Now()}
	// test plays one test job through the chain as it is now, and returns whether the search goes on.
	test = func(noisy string, unlucky bool) bool {
		for _, n := range c.blockNodes {
			n.seen = time.Now()
		}
		c.chainsLocked()
		ch := c.chains[model]
		if ch == nil {
			t.Fatal("no chain")
		}
		passed := !unlucky
		for _, l := range ch.links {
			if l.key == noisy {
				passed = false
			}
		}
		return c.chainTestedLocked(model, ch.head, ch.links, passed)
	}
	return c, model, test
}

func TestNoisyHolderIsExcluded(t *testing.T) {
	for _, noisy := range []string{"a1", "a2", "b1", "b2"} {
		c, _, test := testWorld(t)
		// The chain takes a1 and b1 first. For the other two to be in it, their twin is away when
		// the chain is made, and comes back after.
		if twin := map[string]string{"a2": "a1", "b2": "b1"}[noisy]; twin != "" {
			away := c.blockNodes[twin]
			delete(c.blockNodes, twin)
			c.chainsLocked()
			c.blockNodes[twin] = away
		}
		tests := 0
		for ; tests < 10 && time.Now().After(c.excluded[noisy]); tests++ {
			test(noisy, false)
		}
		if time.Now().After(c.excluded[noisy]) {
			t.Fatalf("%s returns noise and is still there after %d tests", noisy, tests)
		}
		if len(c.excluded) != 1 {
			t.Fatalf("%s returns noise, excluded: %v", noisy, c.excluded)
		}
		// With the machine gone the chain works, and stays as it is.
		for i := 0; i < 5; i++ {
			test(noisy, false)
		}
		if len(c.excluded) != 1 || len(c.doubts) != 0 {
			t.Fatalf("after %s was excluded: excluded %v, doubts %v", noisy, c.excluded, c.doubts)
		}
		t.Logf("%s excluded after %d tests", noisy, tests)
	}
}

func TestHonestHoldersStay(t *testing.T) {
	c, _, test := testWorld(t)
	// A thousand tests, one in a hundred failing by bad luck: nobody computed wrong, nobody leaves.
	luck := rand.New(rand.NewSource(1))
	failed := 0
	for i := 0; i < 1000; i++ {
		unlucky := luck.Intn(100) == 0
		if unlucky {
			failed++
		}
		test("", unlucky)
	}
	if len(c.excluded) != 0 {
		t.Fatalf("%d tests failed by bad luck and an honest machine was excluded: %v", failed, c.excluded)
	}
	// One failure and nothing to replace a holder with: the search stops, and the head is judged as a machine alone.
	delete(c.blockNodes, "a2")
	delete(c.blockNodes, "b2")
	c.tests = map[string]*chainTest{}
	if test("", true) {
		t.Fatal("no machine can be replaced, the search should stop")
	}
}
