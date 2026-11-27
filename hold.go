package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Holding blocks is the other side of a split: this machine keeps some blocks of a model in
// memory for the head of a group, and computes its share of every answer. It downloads those
// blocks and nothing else. The block server runs locked in: it cannot read the user's files,
// connect anywhere or start a program.

// holdListen is where the block server listens, so that the other machines of the group reach it.
const holdListen = "0.0.0.0:50052"

// HoldStatus is what the interface shows of the blocks this machine holds.
type HoldStatus struct {
	Model    string `json:"model"`
	First    int    `json:"first"`
	Last     int    `json:"last"`
	Of       int    `json:"of"`    // how many blocks the model has
	State    string `json:"state"` // downloading, ready, error
	Pct      int    `json:"pct"`
	Announce string `json:"announce"` // what to give to the head of the group
	Error    string `json:"error,omitempty"`
}

type holdConfig struct {
	Model string `json:"model"`
	First int    `json:"first"`
	Last  int    `json:"last"`
	Of    int    `json:"of"`
}

func (n *Node) holdFile() string { return filepath.Join(n.home, "hold.json") }

var plans struct {
	sync.Mutex
	at   map[string]time.Time
	json map[string][]byte
}

// modelPlan asks the engine for the blocks of a model and how many this machine can hold.
// The engine reads the model's header from the public registry: nothing large is downloaded.
func (n *Node) modelPlan(ctx context.Context, model string) ([]byte, error) {
	plans.Lock()
	if raw, ok := plans.json[model]; ok && time.Since(plans.at[model]) < 2*time.Minute {
		plans.Unlock()
		return raw, nil
	}
	plans.Unlock()
	engine, err := ensureEngine(ctx, n.home)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var said bytes.Buffer
	cmd := exec.CommandContext(ctx, engine, "plan", "--model", model, "--json")
	cmd.Stderr = &said
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("the plan of %s could not be read: %s", model, lastLine(said.String()))
	}
	plans.Lock()
	if plans.json == nil {
		plans.json, plans.at = map[string][]byte{}, map[string]time.Time{}
	}
	plans.json[model], plans.at[model] = raw, time.Now()
	plans.Unlock()
	return raw, nil
}

func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	return strings.TrimPrefix(lines[len(lines)-1], "error: ")
}

func (n *Node) handlePlan(w http.ResponseWriter, r *http.Request) {
	model := r.URL.Query().Get("model")
	n.mu.Lock()
	known := false
	for _, m := range n.catalog {
		known = known || m.ID == model
	}
	n.mu.Unlock()
	if !known {
		writeJSON(w, 400, map[string]string{"error": "this network does not offer that model"})
		return
	}
	raw, err := n.modelPlan(r.Context(), model)
	if err != nil {
		writeJSON(w, 502, map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw)
}

// startHold downloads the chosen blocks and serves them until stopped.
func (n *Node) startHold(cfg holdConfig) {
	set := func(change func(*HoldStatus)) {
		n.mu.Lock()
		if n.hold != nil {
			change(n.hold)
		}
		n.mu.Unlock()
	}
	fail := func(problem string) {
		log.Printf("hold: %s", problem)
		set(func(h *HoldStatus) { h.State, h.Error = "error", problem })
	}
	n.mu.Lock()
	if n.hold != nil && n.hold.State != "error" { // one block server per machine: a second one would find the port taken
		n.mu.Unlock()
		return
	}
	n.hold = &HoldStatus{Model: cfg.Model, First: cfg.First, Last: cfg.Last, Of: cfg.Of, State: "downloading"}
	n.mu.Unlock()

	engine, err := ensureEngine(context.Background(), n.home)
	if err != nil {
		fail(err.Error())
		return
	}
	// --exit-with-stdin: the block server stops if this program dies, nothing is left running.
	cmd := exec.Command(engine, "block", "--model", cfg.Model, "--blocks", fmt.Sprintf("%d-%d", cfg.First, cfg.Last),
		"--listen", holdListen, "--json", "--exit-with-stdin")
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	var said bytes.Buffer
	cmd.Stderr = &said
	if err := cmd.Start(); err != nil {
		fail("the engine did not start: " + err.Error())
		return
	}
	done := make(chan struct{})
	defer close(done)
	n.mu.Lock()
	n.stopHold = func() {
		stdin.Close()
		select { // the block server has let go of its files when the engine has ended
		case <-done:
		case <-time.After(15 * time.Second):
		}
	}
	n.mu.Unlock()

	lines := bufio.NewScanner(stdout)
	for lines.Scan() {
		var ev struct {
			Progress *int   `json:"progress"`
			Ready    bool   `json:"ready"`
			Announce string `json:"announce"`
		}
		if json.Unmarshal(lines.Bytes(), &ev) != nil {
			continue
		}
		switch {
		case ev.Ready:
			set(func(h *HoldStatus) { h.State, h.Pct, h.Announce = "ready", 100, ev.Announce })
			log.Printf("holding blocks %d to %d of %s", cfg.First, cfg.Last, cfg.Model)
		case ev.Progress != nil:
			set(func(h *HoldStatus) { h.Pct = *ev.Progress })
		}
	}
	cmd.Wait()
	n.mu.Lock()
	stopped := n.stopHold == nil // stopped on purpose
	n.stopHold = nil
	n.mu.Unlock()
	if !stopped {
		fail("the block server stopped: " + lastLine(said.String()))
	}
}

func (n *Node) endHold() {
	n.mu.Lock()
	stop := n.stopHold
	n.stopHold, n.hold = nil, nil
	n.mu.Unlock()
	if stop != nil {
		stop()
		n.engineClean(false) // the blocks this machine no longer holds leave its disk
	}
	os.Remove(n.holdFile())
}

func (n *Node) handleHold(w http.ResponseWriter, r *http.Request) {
	var req struct {
		On    bool   `json:"on"`
		Model string `json:"model"`
		First int    `json:"first"`
		Last  int    `json:"last"`
	}
	refuse := func(status int, msg string) { writeJSON(w, status, map[string]string{"error": msg}) }
	if json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req) != nil {
		refuse(400, "bad request")
		return
	}
	if !req.On {
		n.endHold()
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	n.mu.Lock()
	busy := n.hold != nil && n.hold.State != "error"
	n.mu.Unlock()
	if busy {
		refuse(409, "this machine already holds blocks: stop them first")
		return
	}
	raw, err := n.modelPlan(r.Context(), req.Model)
	if err != nil {
		refuse(502, err.Error())
		return
	}
	var plan struct {
		Blocks []json.RawMessage `json:"blocks"`
	}
	json.Unmarshal(raw, &plan)
	if req.First < 1 || req.Last < req.First || req.Last > len(plan.Blocks) {
		refuse(400, fmt.Sprintf("choose blocks between 1 and %d", len(plan.Blocks)))
		return
	}
	cfg := holdConfig{Model: req.Model, First: req.First, Last: req.Last, Of: len(plan.Blocks)}
	saved, _ := json.Marshal(cfg)
	os.WriteFile(n.holdFile(), saved, 0o600)
	go n.startHold(cfg)
	writeJSON(w, 200, map[string]bool{"ok": true})
}
