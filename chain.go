package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"slices"
	"sort"
	"strings"
	"time"
)

// Blocks let strangers hold one model together. A machine that holds blocks tells the
// coordinator which ones, and the line that reaches it. A machine that has the whole file of a
// model offers to hold its conversations: it is a head. When every block of a model has enough
// holders and a head is there, the coordinator gives the head a chain, one holder for each
// block. The head loads the model across them and offers it to the network like any other model.

// Hold is what a machine says about the blocks it holds.
type Hold struct {
	Model string `json:"model"`
	First int    `json:"first"`
	Last  int    `json:"last"`
	Line  string `json:"line"` // the routes that reach it and the secret of its link
}

// BlocksReq is sent every few seconds by a machine that holds blocks or offers to be a head.
type BlocksReq struct {
	Hold *Hold  `json:"hold,omitempty"`
	Head string `json:"head,omitempty"` // the model whose conversations it offers to hold
}

type BlocksResp struct {
	Chain *Chain `json:"chain,omitempty"` // the group this machine must form now, as its head
}

// Chain is one holder for each block of a model, in block order.
type Chain struct {
	Model string   `json:"model"`
	Lines []string `json:"lines"`
}

const (
	blocksEvery = 10 * time.Second // how often a machine reports
	blocksAlive = 40 * time.Second // silence after which it has gone
)

type blockNode struct {
	key  string
	hold *Hold
	head string
	seen time.Time
}

type link struct {
	key         string // the holder
	line        string // what it announced when the chain was made
	first, last int    // the blocks it has in this chain
}

type group struct {
	head  string
	links []link
}

// validLine checks the line a holder announced: routes, "=", blocks, "#", a secret of 32 hex digits.
func validLine(line string) bool {
	rest, secret, _ := strings.Cut(line, "#")
	routes, _, hasBlocks := strings.Cut(rest, "=")
	raw, err := hex.DecodeString(secret)
	return len(line) <= 512 && err == nil && len(raw) == 16 && hasBlocks && routes != "" && !strings.ContainsAny(line, ", \t\r\n")
}

// lineFor rewrites a holder's line for the blocks it is given in a chain, which may be fewer
// than the ones it holds.
func lineFor(line string, first, last int) string {
	rest, secret, _ := strings.Cut(line, "#")
	routes, _, _ := strings.Cut(rest, "=")
	return fmt.Sprintf("%s=%d-%d#%s", routes, first, last, secret)
}

// cover says how many machines hold each block of a model. Index 0 is block 1.
func cover(holds []*Hold, of int) []int {
	n := make([]int, of)
	for _, h := range holds {
		for b := h.First; b <= h.Last && b <= of; b++ {
			n[b-1]++
		}
	}
	return n
}

// handleBlocks records what a machine holds or offers, and answers with its chain when it is a head.
func (c *Coordinator) handleBlocks(rw http.ResponseWriter, r *http.Request, key string, body []byte) {
	var req BlocksReq
	if json.Unmarshal(body, &req) != nil {
		httpErr(rw, 400, "bad_request", "bad blocks report")
		return
	}
	if h := req.Hold; h != nil && (c.model(h.Model) == nil || h.First < 1 || h.Last < h.First || h.Last > 4096 || !validLine(h.Line)) {
		httpErr(rw, 400, "bad_request", "bad blocks report")
		return
	}
	if req.Head != "" && c.model(req.Head) == nil {
		httpErr(rw, 400, "bad_request", "this network does not offer that model")
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if req.Hold == nil && req.Head == "" {
		delete(c.blockNodes, key)
	} else {
		c.blockNodes[key] = &blockNode{key: key, hold: req.Hold, head: req.Head, seen: time.Now()}
		if req.Hold != nil {
			c.wantPlanLocked(req.Hold.Model)
		}
		if req.Head != "" {
			c.wantPlanLocked(req.Head)
		}
	}
	c.chainsLocked()
	var resp BlocksResp
	for model, ch := range c.chains {
		if ch.head == key {
			resp.Chain = &Chain{Model: model}
			for _, l := range ch.links {
				resp.Chain.Lines = append(resp.Chain.Lines, lineFor(l.line, l.first, l.last))
			}
		}
	}
	writeJSON(rw, 200, resp)
}

// wantPlanLocked finds out, once, how many blocks a model has. The engine reads it from the
// model's header in the public registry: the coordinator downloads no model.
func (c *Coordinator) wantPlanLocked(model string) {
	if c.Blocks[model] > 0 || time.Since(c.planTried[model]) < 5*time.Minute {
		return
	}
	c.planTried[model] = time.Now()
	go func() {
		n, err := c.PlanOf(model)
		if err != nil || n < 1 {
			log.Printf("plan of %s: %v", model, err)
			return
		}
		c.mu.Lock()
		c.Blocks[model] = n
		c.mu.Unlock()
		log.Printf("%s has %d blocks", model, n)
	}()
}

// enginePlan asks the engine how many blocks a model has.
func enginePlan(home string) func(model string) (int, error) {
	return func(model string) (int, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		engine, err := ensureEngine(ctx, home)
		if err != nil {
			return 0, err
		}
		raw, err := exec.CommandContext(ctx, engine, "plan", "--model", model, "--json").Output()
		if err != nil {
			return 0, err
		}
		var plan struct {
			Blocks []json.RawMessage `json:"blocks"`
		}
		err = json.Unmarshal(raw, &plan)
		return len(plan.Blocks), err
	}
}

// chainsLocked keeps a chain for every model that can have one. A chain that still holds is
// left alone: changing it makes the head load the model again.
func (c *Coordinator) chainsLocked() {
	now := time.Now()
	for key, n := range c.blockNodes {
		if now.Sub(n.seen) > blocksAlive {
			delete(c.blockNodes, key)
		}
	}
	heading := map[string]bool{} // machines that are the head of a chain that stays
	for model, ch := range c.chains {
		if c.holdsLocked(model, ch) {
			heading[ch.head] = true
		}
	}
	for _, m := range c.Catalog {
		cur := c.chains[m.ID]
		if cur != nil && heading[cur.head] {
			continue
		}
		delete(c.chains, m.ID)
		of := c.Blocks[m.ID]
		var holders []*blockNode
		var holds []*Hold
		var heads []string
		for _, n := range c.blockNodes {
			if n.hold != nil && n.hold.Model == m.ID && n.hold.Last <= of {
				holders = append(holders, n)
				holds = append(holds, n.hold)
			}
			if n.head == m.ID && !heading[n.key] {
				heads = append(heads, n.key)
			}
		}
		if of == 0 || len(heads) == 0 {
			continue
		}
		// A new chain waits until every block has enough holders, so that one can leave without
		// the model stopping. A chain that lost a machine goes on with whoever is left.
		need := c.HoldersPerBlock
		if cur != nil {
			need = 1
		}
		if slices.Min(cover(holds, of)) < need {
			continue
		}
		// One holder for each block: at every step, the machine that goes furthest.
		sort.Slice(holders, func(i, j int) bool { return holders[i].key < holders[j].key })
		made := &group{}
		for next := 1; next <= of; {
			var best *blockNode
			for _, n := range holders {
				if n.hold.First <= next && n.hold.Last >= next && (best == nil || n.hold.Last > best.hold.Last) {
					best = n
				}
			}
			made.links = append(made.links, link{best.key, best.hold.Line, next, best.hold.Last})
			next = best.hold.Last + 1
		}
		// The head it had stays the head when it is still there: it has the model in memory.
		sort.Strings(heads)
		made.head = heads[0]
		if cur != nil && slices.Contains(heads, cur.head) {
			made.head = cur.head
		}
		heading[made.head] = true
		c.chains[m.ID] = made
		log.Printf("chain for %s: head %.8s, %d holders", m.ID, made.head, len(made.links))
	}
}

// holdsLocked says whether a chain can go on as it is: its head still offers the model and
// every holder still holds the blocks it was given, on the same line.
func (c *Coordinator) holdsLocked(model string, ch *group) bool {
	if n := c.blockNodes[ch.head]; n == nil || n.head != model {
		return false
	}
	for _, l := range ch.links {
		n := c.blockNodes[l.key]
		if n == nil || n.hold == nil || n.hold.Model != model || n.hold.Line != l.line || n.hold.First > l.first || n.hold.Last < l.last {
			return false
		}
	}
	return true
}
