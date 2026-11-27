package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// A head holds the conversations of a model whose layers are on other machines. It needs the
// whole file of the model on its disk, and little memory. It tells the coordinator it is there;
// when every block has its holders, the coordinator answers with a chain and this machine loads
// the model across it, then offers it to the network like any model it shares.

// HeadStatus is what the interface shows of that role.
type HeadStatus struct {
	Model string  `json:"model"`
	State string  `json:"state"` // downloading, waiting, chained, error
	Pct   float64 `json:"pct"`
	Error string  `json:"error,omitempty"`
}

func (n *Node) headFile() string { return filepath.Join(n.home, "head.json") }

// becomeHead downloads the model's file when it is missing, then offers this machine as a head.
func (n *Node) becomeHead(model string) {
	set := func(state string, pct float64, problem string) {
		n.mu.Lock()
		if n.head != nil && n.head.Model == model {
			n.head.State, n.head.Pct, n.head.Error = state, pct, problem
		}
		n.mu.Unlock()
	}
	n.mu.Lock()
	n.head = &HeadStatus{Model: model, State: "downloading"}
	n.mu.Unlock()
	ctx := context.Background()
	if !hasModel(ctx, n.ollama, model) {
		if err := pullModel(ctx, n.ollama, model, func(pct float64) { set("downloading", pct, "") }); err != nil {
			log.Printf("head %s: %v", model, err)
			set("error", 0, err.Error())
			return
		}
	}
	set("waiting", 100, "")
}

func (n *Node) endHead() {
	n.mu.Lock()
	n.head = nil
	chained := n.chain != ""
	n.chain = ""
	n.mu.Unlock()
	os.Remove(n.headFile())
	if chained {
		n.stopSplit()
	}
}

func (n *Node) handleHead(w http.ResponseWriter, r *http.Request) {
	var req struct {
		On    bool   `json:"on"`
		Model string `json:"model"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req) != nil {
		writeJSON(w, 400, map[string]string{"error": "bad request"})
		return
	}
	n.endHead()
	if req.On {
		n.mu.Lock()
		known := slices.ContainsFunc(n.catalog, func(m Model) bool { return m.ID == req.Model })
		n.mu.Unlock()
		if !known {
			writeJSON(w, 400, map[string]string{"error": "this network does not offer that model"})
			return
		}
		n.stopSplit() // a group set up by hand gives way: one model for each machine
		os.WriteFile(n.headFile(), []byte(req.Model), 0o600)
		go n.becomeHead(req.Model)
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// blocksLoop tells the coordinator what this machine holds and offers, and follows the chain it
// is given.
func (n *Node) blocksLoop(ctx context.Context) {
	said := false // the coordinator knows something about this machine
	var tried time.Time
	for ctx.Err() == nil {
		sleep(ctx, blocksEvery/2)
		n.mu.Lock()
		var req BlocksReq
		if h := n.hold; h != nil && h.State == "ready" {
			req.Hold = &Hold{Model: h.Model, First: h.First, Last: h.Last, Line: h.Announce}
		}
		if h := n.head; h != nil && (h.State == "waiting" || h.State == "chained") {
			req.Head = h.Model
		}
		if n.chain != "" && n.split != nil && n.split.State == "ready" {
			req.Serving = chainID(n.chain) // the chain that answers now: the coordinator tests it knowingly
		}
		n.mu.Unlock()
		if req.Hold == nil && req.Head == "" && !said {
			continue
		}
		var resp BlocksResp
		if err := n.callJSON(ctx, "POST", "/v1/blocks", req, &resp); err != nil {
			continue
		}
		said = req.Hold != nil || req.Head != ""

		want := ""
		if resp.Chain != nil {
			want = resp.Chain.Model + " " + strings.Join(resp.Chain.Lines, ",")
		}
		n.mu.Lock()
		same := want == n.chain
		failed := n.split != nil && n.split.State == "error"
		if n.head != nil && n.head.State != "downloading" && n.head.State != "error" {
			n.head.State = "waiting"
			if want != "" {
				n.head.State = "chained"
			}
		}
		if n.head == nil || req.Head == "" {
			want, same = "", n.chain == "" // not a head any more: whatever the answer says
		}
		n.mu.Unlock()
		// The same chain is formed again when it broke: a holder may only have blinked.
		if same && !(want != "" && failed && time.Since(tried) > 30*time.Second) {
			continue
		}
		n.mu.Lock()
		had := n.chain != ""
		n.chain = want
		n.mu.Unlock()
		if had {
			n.stopSplit()
		}
		if want != "" {
			tried = time.Now()
			go n.startSplit(splitConfig{Model: resp.Chain.Model, Machines: resp.Chain.Lines})
		}
	}
}
