package main

import (
	"log"
	"time"
)

// A machine of a chain can compute wrong, by fault or on purpose, and the answer is then noise.
// The coordinator already sends test jobs with a known answer. When one fails through a chain,
// any of its machines may be the cause. To find which, the coordinator changes one holder at a
// time and tests again:
//
//	the chain fails, one holder is replaced, it passes: the holder that left is doubted;
//	it is put back once: if the chain fails again, it is excluded.
//
// Two tests must agree before a machine is excluded, because a test can fail by bad luck. A
// machine in a chain that passes is cleared of every doubt.

const (
	excludeTime = 24 * time.Hour
	doubtsToOut = 2
)

type chainTest struct {
	failed []link // the chain of the last test, when it failed
	passed []link // the chain of the last test that passed
	swap   int    // which holder is replaced next
	fails  int    // failed tests in a row
	probe  bool   // the chain was changed to learn something: test it as soon as the head has it
}

// servedLocked returns the chain a machine answers with now, as the head of a model, or nil.
func (c *Coordinator) servedLocked(model, worker string) []link {
	ch, n := c.chains[model], c.blockNodes[worker]
	if ch == nil || ch.head != worker || n == nil || n.serving != ch.id(model) {
		return nil
	}
	return ch.links
}

// usableLocked says whether a machine can be given a job for a model now. A head is given none
// while it does not answer with the chain the coordinator has for it: for a few seconds after a
// change it still has the old one, or is loading the new one. A job would be paid to the wrong
// machines, and a test would judge the wrong chain.
func (c *Coordinator) usableLocked(worker, model string) bool {
	if n := c.blockNodes[worker]; n == nil || n.head != model {
		return true // not a head: it answers alone
	}
	return c.servedLocked(model, worker) != nil
}

// oneSwap compares two chains. It returns the place where they differ when they are the same
// blocks on the same machines except for one holder, and -1 otherwise.
func oneSwap(a, b []link) int {
	if a == nil || len(a) != len(b) {
		return -1
	}
	at := -1
	for i := range a {
		if a[i].first != b[i].first || a[i].last != b[i].last {
			return -1
		}
		if a[i].key != b[i].key {
			if at >= 0 {
				return -1
			}
			at = i
		}
	}
	return at
}

// chainTestedLocked takes the result of a test job that went through a chain. It reports false
// when it has nothing more to try: the head is then judged like any machine that answers alone.
func (c *Coordinator) chainTestedLocked(model, head string, links []link, passed bool) bool {
	t := c.tests[model]
	if t == nil {
		t = &chainTest{}
		c.tests[model] = t
	}
	doubt := func(key string) {
		c.doubts[key]++
		log.Printf("chain of %s: machine %.8s is doubted (%d)", model, key, c.doubts[key])
		if c.doubts[key] >= doubtsToOut {
			delete(c.doubts, key)
			c.excluded[key] = time.Now().Add(excludeTime)
			log.Printf("chain of %s: machine %.8s computed wrong twice, left out for %v", model, key, excludeTime)
		}
	}
	force := func(links []link) {
		c.chains[model] = &group{head: head, links: links}
		t.probe = true
	}
	if passed {
		t.fails = 0
		for _, l := range links {
			delete(c.doubts, l.key)
		}
		if i := oneSwap(t.failed, links); i >= 0 {
			// It failed with one machine and passes without it. Put that machine back once to be sure.
			was := t.failed
			doubt(was[i].key)
			if time.Now().After(c.excluded[was[i].key]) {
				force(was)
			}
		}
		t.passed, t.failed = links, nil
		return true
	}
	t.fails++
	if i := oneSwap(t.passed, links); i >= 0 {
		// It passed, one machine came in, and it fails: go back to the chain that passed.
		doubt(links[i].key)
		force(t.passed)
		t.failed = nil
		return true
	}
	t.failed, t.passed = links, nil
	// Replace one holder, a different one each time, by another machine that holds the same blocks.
	for try := 0; try < len(links) && t.fails <= len(links)+1; try++ {
		i := (t.swap + try) % len(links)
		for key, n := range c.blockNodes {
			inChain := false
			for _, l := range links {
				inChain = inChain || l.key == key
			}
			if inChain || n.hold == nil || n.hold.Model != model || n.hold.First > links[i].first || n.hold.Last < links[i].last ||
				time.Now().Before(c.excluded[key]) || time.Since(n.seen) > blocksAlive {
				continue
			}
			changed := append([]link{}, links...)
			changed[i] = link{key, n.hold.Line, links[i].first, links[i].last}
			force(changed)
			t.swap = i + 1
			log.Printf("chain of %s failed a test: trying %.8s in place of %.8s", model, key, links[i].key)
			return true
		}
	}
	// No holder can be replaced, or every one was and it still fails: what stays is the head.
	return false
}

// probeLocked sends a test job through every chain that was changed to learn something, as soon
// as its head has loaded it and is free.
func (c *Coordinator) probeLocked() {
	for model, t := range c.tests {
		ch := c.chains[model]
		if !t.probe || ch == nil {
			continue
		}
		w := c.workers[ch.head]
		if w == nil || w.job != nil || w.waiter == nil || c.servedLocked(model, ch.head) == nil || time.Now().Before(w.bannedUntil) {
			continue
		}
		t.probe = false
		go c.runCanary(c.newJobLocked(c.model(model), w, "", 32))
	}
}
