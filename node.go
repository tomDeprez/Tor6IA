package main

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

//go:embed ui/index.html
var indexHTML []byte

const flushEvery = 150 * time.Millisecond

var errBudget = errors.New("output budget reached")

// Node is one machine on the network: it serves jobs with its local model (worker)
// and sends its user's prompts to other machines (requester).
type Node struct {
	id     Identity
	coord  string
	ollama string
	home   string // where the audit checkpoint is kept
	solo   bool
	xpriv  *ecdh.PrivateKey
	hc     *http.Client

	mu         sync.Mutex
	phase      string // connecting, ollama, ollama-install, model-pull, ready, error
	pct        float64
	detail     string
	warn       string
	models     []string    // offered to the network, all on disk
	pull       *PullStatus // a model being added
	stopPull   context.CancelFunc
	vramMB     int
	ramMB      int
	sharing    bool
	working    bool
	served     int
	alert      string       // proof that the coordinator cheated: never cleared
	audited    *AuditStatus // last successful ledger audit
	update     *Update      // a newer release, when there is one
	catalog    []Model      // the network's models, as last read
	split      *SplitStatus // the shared model runs across a group of machines
	engineURL  string       // the engine's local API while a split is ready
	stopEngine func()
	hold       *HoldStatus // blocks this machine holds for the head of a group
	stopHold   func()
	head       *HeadStatus // this machine offers to hold the conversations of a model in blocks
	chain      string      // the chain the coordinator gave it, "" when none
	cancelPoll context.CancelFunc
}

// AuditStatus is the result of the last ledger audit this node ran itself.
type AuditStatus struct {
	Entries     uint64 `json:"entries"`
	Circulating int64  `json:"circulating"`
	At          int64  `json:"at"`
}

func newNode(id Identity, coord, ollama, home string, solo bool) *Node {
	xpriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	n := &Node{id: id, coord: strings.TrimRight(coord, "/"), ollama: ollama, home: home, solo: solo, xpriv: xpriv,
		hc: &http.Client{}, phase: "connecting", sharing: true}
	n.vramMB, n.ramMB = detectVRAMMB(), totalRAMMB()
	return n
}

// PullStatus is the download of a model the user asked to share.
type PullStatus struct {
	Model string  `json:"model"`
	Pct   float64 `json:"pct"`
	Error string  `json:"error,omitempty"`
}

// capacity says how this machine would run a model: "fast" on its graphics card, or on the
// processor for a model made for it; "slow" on the processor for a larger one; "no" when the
// memory is not there. It is what keeps a machine from loading a model that would freeze it.
func (n *Node) capacity(m Model) string {
	onCPU := "slow"
	if m.MinVRAMMB == 0 {
		onCPU = "fast" // made for processors
	}
	// Measured: Ollama asks 6.9 GiB of free memory to run the 4.6 GB gemma4:e2b, 1.5 times its file.
	// The system keeps 2 GB.
	need := m.SizeGB*1500 + 2000
	switch {
	case m.MinVRAMMB > 0 && m.MinVRAMMB <= n.vramMB:
		return "fast"
	case n.ramMB == 0 && m.MinVRAMMB == 0:
		return onCPU // memory size unknown: let a light model try, Ollama refuses what does not fit
	case float64(n.ramMB) >= need:
		return onCPU
	}
	return "no"
}

// bestModel is the heaviest model of the catalog this machine runs fast, "" when there is none.
func (n *Node) bestModel(catalog []Model) string {
	best := ""
	for _, m := range catalog {
		if n.capacity(m) == "fast" {
			best = m.ID
		}
	}
	return best
}

func (n *Node) modelsFile() string { return filepath.Join(n.home, "models.json") }

func (n *Node) savedModels() (list []string) {
	if raw, err := os.ReadFile(n.modelsFile()); err == nil {
		json.Unmarshal(raw, &list)
	}
	return list
}

func (n *Node) saveModels() {
	n.mu.Lock()
	raw, _ := json.Marshal(n.models)
	n.mu.Unlock()
	if err := os.WriteFile(n.modelsFile(), raw, 0o600); err != nil {
		log.Printf("cannot save the model list: %v", err)
	}
}

type apiError struct {
	Status int
	Code   string `json:"code"`
	Msg    string `json:"error"`
}

func (e *apiError) Error() string { return e.Msg }

func (n *Node) call(ctx context.Context, method, path string, in any) (*http.Response, error) {
	var body []byte
	if in != nil {
		body, _ = json.Marshal(in)
	}
	req, err := http.NewRequestWithContext(ctx, method, n.coord+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	n.id.sign(req, body)
	resp, err := n.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		e := &apiError{Status: resp.StatusCode}
		json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(e)
		if e.Msg == "" {
			e.Msg = "coordinator: " + resp.Status
		}
		return nil, e
	}
	return resp, nil
}

func (n *Node) callJSON(ctx context.Context, method, path string, in, out any) error {
	resp, err := n.call(ctx, method, path, in)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

func (n *Node) setPhase(phase string, pct float64, detail string) {
	n.mu.Lock()
	n.phase, n.pct, n.detail = phase, pct, detail
	n.mu.Unlock()
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// setup gets the machine ready to serve: Ollama running and the model on disk.
func (n *Node) setup(ctx context.Context, want string) {
	var net Network
	for {
		err := n.callJSON(ctx, "GET", "/v1/network", nil, &net)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return
		}
		n.setPhase("connecting", 0, err.Error())
		sleep(ctx, 3*time.Second)
	}
	// Which models to offer: the --model flag, else what the user chose before, else the biggest that fits.
	list := n.savedModels()
	if want != "" {
		list = []string{want}
	}
	n.mu.Lock()
	n.catalog = net.Models
	n.mu.Unlock()
	// Blocks held before the restart are held again. This comes first: holding blocks needs neither
	// Ollama nor the memory for a whole model, so a machine that can do nothing else still does this.
	if raw, err := os.ReadFile(n.holdFile()); err == nil {
		var cfg holdConfig
		if json.Unmarshal(raw, &cfg) == nil && cfg.Model != "" {
			go n.startHold(cfg)
		}
	}
	offered := func(id string) int { return slices.IndexFunc(net.Models, func(m Model) bool { return m.ID == id }) }
	if want != "" && offered(want) < 0 {
		n.setPhase("error", 0, "this network does not offer the model "+want)
		return
	}
	// The network may have changed its models since the list was saved: forget the ones it dropped.
	list = slices.DeleteFunc(list, func(id string) bool { return offered(id) < 0 })
	if len(list) > 1 {
		list = list[len(list)-1:] // lists from before "one model per machine": keep the last one chosen
	}
	if len(list) == 1 && want == "" && n.capacity(net.Models[offered(list[0])]) == "no" {
		list = nil // the saved model no longer fits this machine: choose again
	}
	if len(list) == 0 {
		best := n.bestModel(net.Models)
		if best == "" {
			n.setPhase("error", 0, fmt.Sprintf("this machine has too little memory to share a model: %d GB are needed", int(net.Models[0].SizeGB*1.5+2.99)))
			return
		}
		list = []string{best}
	}

	n.setPhase("ollama", 0, "")
	err := ensureOllama(ctx, n.ollama, func(pct float64) { n.setPhase("ollama-install", pct, "") })
	for _, id := range list {
		if err == nil && !hasModel(ctx, n.ollama, id) {
			n.setPhase("model-pull", 0, id)
			err = pullModel(ctx, n.ollama, id, func(pct float64) { n.setPhase("model-pull", pct, id) })
		}
	}
	if err != nil {
		log.Printf("setup: %v", err)
		n.setPhase("error", 0, err.Error())
		return
	}
	n.mu.Lock()
	n.models = list
	n.mu.Unlock()
	n.saveModels()
	log.Printf("ready: sharing %v", list)
	n.setPhase("ready", 100, "")
	// A machine that offered to be a head offers it again.
	if raw, err := os.ReadFile(n.headFile()); err == nil && offered(strings.TrimSpace(string(raw))) >= 0 {
		go n.becomeHead(strings.TrimSpace(string(raw)))
	}
	// A group set up before the restart is formed again.
	if raw, err := os.ReadFile(n.splitFile()); err == nil {
		var cfg splitConfig
		if json.Unmarshal(raw, &cfg) == nil && len(cfg.Machines) > 0 {
			go n.startSplit(cfg)
		}
	}
}

// share makes id the model this machine offers. A machine offers one model at a time, so its
// memory serves one model well instead of swapping several. The download comes first: the
// machine keeps offering its current model until the new one is ready, then switches.
// deleteOld also removes the replaced model's files.
func (n *Node) share(id string, deleteOld bool) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	progress := func(pct float64, problem string) {
		n.mu.Lock()
		n.pull = &PullStatus{Model: id, Pct: pct, Error: problem}
		n.mu.Unlock()
	}
	n.mu.Lock()
	n.pull, n.stopPull = &PullStatus{Model: id}, cancel
	n.mu.Unlock()
	if !hasModel(ctx, n.ollama, id) {
		if err := pullModel(ctx, n.ollama, id, func(pct float64) { progress(pct, "") }); err != nil {
			if ctx.Err() != nil { // the user cancelled: nothing to report
				n.mu.Lock()
				n.pull = nil
				n.mu.Unlock()
				return
			}
			log.Printf("share %s: %v", id, err)
			progress(0, err.Error())
			return
		}
	}
	n.mu.Lock()
	n.pull = nil
	replaced := slices.DeleteFunc(slices.Clone(n.models), func(m string) bool { return m == id })
	n.models = []string{id}
	n.repollLocked()
	n.mu.Unlock()
	n.saveModels()
	log.Printf("now sharing %s", id)
	for _, old := range replaced {
		if !deleteOld {
			break
		}
		if err := deleteModel(ctx, n.ollama, old); err != nil {
			log.Printf("replaced model %s not deleted: %v", old, err)
		}
	}
}

// leave tells the coordinator that this machine stops computing, so that the model
// counts everyone sees are right at once.
func (n *Node) leave() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if resp, err := n.call(ctx, "POST", "/v1/worker/leave", nil); err == nil {
		resp.Body.Close()
	}
}

// repollLocked restarts the current poll so that the coordinator learns the new model list now.
func (n *Node) repollLocked() {
	if n.cancelPoll != nil {
		n.cancelPoll()
	}
}

// handleShare changes what this machine offers: share a model, stop sharing it (and optionally
// delete its files), or cancel its download.
func (n *Node) handleShare(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model  string `json:"model"`
		On     bool   `json:"on"`
		Delete bool   `json:"delete"` // also remove from the disk: this model (on=false) or the one it replaces (on=true)
	}
	refuse := func(status int, msg string) { writeJSON(w, status, map[string]string{"error": msg}) }
	if json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&req) != nil {
		refuse(400, "bad request")
		return
	}
	n.mu.Lock()
	ready := n.phase == "ready"
	hosted := slices.Contains(n.models, req.Model)
	pulling := n.pull != nil && n.pull.Error == ""
	left := false
	if ready && !req.On {
		if n.pull != nil && n.pull.Model == req.Model {
			if n.stopPull != nil {
				n.stopPull() // cancels the download; share() clears the status
			}
			n.pull = nil
		}
		if hosted {
			n.models = slices.DeleteFunc(slices.Clone(n.models), func(id string) bool { return id == req.Model })
			n.repollLocked()
			left = len(n.models) == 0
		}
	}
	n.mu.Unlock()
	switch {
	case !ready:
		refuse(409, "the machine is not ready yet")
		return
	case !req.On:
		n.mu.Lock()
		grouped := n.split != nil && n.split.Model == req.Model
		n.mu.Unlock()
		if grouped {
			n.stopSplit()
		}
		n.saveModels()
		if left {
			n.leave()
		}
		if req.Delete {
			if err := deleteModel(r.Context(), n.ollama, req.Model); err != nil {
				refuse(502, "the files could not be deleted: "+err.Error())
				return
			}
		}
	case hosted:
	case pulling:
		refuse(409, "another model is being downloaded")
		return
	default:
		var net Network
		if err := n.callJSON(r.Context(), "GET", "/v1/network", nil, &net); err != nil {
			refuse(502, err.Error())
			return
		}
		i := slices.IndexFunc(net.Models, func(m Model) bool { return m.ID == req.Model })
		if i < 0 || n.capacity(net.Models[i]) == "no" {
			refuse(400, "this machine cannot run that model")
			return
		}
		n.stopSplit() // the group ran the model this one replaces
		go n.share(req.Model, req.Delete)
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// handleUninstall removes Tor6IA from this machine: it leaves the network, deletes the shared
// models, the program and its shortcuts, then stops. The account is kept unless forget is set.
func (n *Node) handleUninstall(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Forget bool `json:"forget"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&req) != nil {
		http.Error(w, "bad request", 400)
		return
	}
	n.stopSplit()
	n.endHold()
	n.endHead()
	n.engineClean(true)                           // llama.cpp and the blocks the engine downloaded
	os.RemoveAll(filepath.Join(n.home, "engine")) // the engine program this node downloaded
	n.mu.Lock()
	models := n.models
	n.models, n.sharing = nil, false
	if n.stopPull != nil {
		n.stopPull()
	}
	n.repollLocked()
	n.mu.Unlock()
	n.leave()
	var failed []string
	for _, id := range models {
		if err := deleteModel(r.Context(), n.ollama, id); err != nil {
			log.Printf("uninstall: %s not deleted: %v", id, err)
			failed = append(failed, id)
		}
	}
	// Only files Tor6IA created are removed, by name: the folder may be one the user chose.
	names := []string{"models.json", "checkpoint.json"}
	if req.Forget {
		names = append(names, "identity.key")
		os.RemoveAll(filepath.Join(n.home, "solo"))
	}
	for _, name := range names {
		os.Remove(filepath.Join(n.home, name))
	}
	os.Remove(n.home) // succeeds only when nothing is left in it
	writeJSON(w, 200, map[string]any{"ok": true, "not_deleted": failed})
	go func() {
		time.Sleep(time.Second) // let the answer reach the browser
		removeProgram()
		os.Exit(0)
	}()
}

// workLoop asks the coordinator for jobs while sharing is on.
func (n *Node) workLoop(ctx context.Context) {
	xpub := n.xpriv.PublicKey().Bytes()
	xsig := ed25519.Sign(n.id.priv, xkeyMessage(xpub))
	for ctx.Err() == nil {
		n.mu.Lock()
		ready := n.sharing && n.phase == "ready" && len(n.models) > 0
		models := slices.Clone(n.models)
		pctx, cancel := context.WithCancel(ctx)
		n.cancelPoll = cancel
		n.mu.Unlock()
		if !ready {
			cancel()
			sleep(ctx, time.Second)
			continue
		}
		resp, err := n.call(pctx, "POST", "/v1/worker/poll", PollReq{Models: models, XPub: xpub, XSig: xsig, Version: version})
		if err != nil {
			cancel()
			if pctx.Err() == nil {
				n.mu.Lock()
				n.warn = err.Error()
				n.mu.Unlock()
				sleep(ctx, 3*time.Second)
			}
			continue
		}
		n.mu.Lock()
		n.warn = ""
		n.mu.Unlock()
		var job JobMsg
		got := resp.StatusCode == 200 && json.NewDecoder(resp.Body).Decode(&job) == nil
		resp.Body.Close()
		cancel()
		if !got {
			continue
		}
		n.mu.Lock()
		n.working = true
		n.mu.Unlock()
		ok := n.runJob(ctx, job)
		n.mu.Lock()
		n.working = false
		if ok {
			n.served++
		}
		n.mu.Unlock()
	}
}

// runJob decrypts one job, runs it on the local model and streams the encrypted answer back.
func (n *Node) runJob(ctx context.Context, job JobMsg) bool {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var seq uint64
	post := func(c Chunk) bool {
		c.Seq = seq
		seq++
		resp, err := n.call(ctx, "POST", "/v1/worker/jobs/"+job.ID+"/chunks", c)
		if err != nil {
			cancel() // the coordinator ended the job, or is unreachable: stop generating
			return false
		}
		resp.Body.Close()
		return true
	}

	aead, err := jobKey(n.xpriv, job.EphPub, job.ID)
	var pl Payload
	if err == nil {
		var plain []byte
		if plain, err = open(aead, dirRequest, 0, job.ID, job.Payload); err == nil {
			err = json.Unmarshal(plain, &pl)
		}
	}
	if err != nil {
		post(Chunk{Error: "the machine could not read the job"})
		return false
	}
	if limit := int(job.MaxOutBytes / 4); pl.MaxTokens <= 0 || pl.MaxTokens > limit {
		pl.MaxTokens = limit
	}

	var buf []byte
	var sent int64
	last := time.Now()
	flush := func() bool {
		if len(buf) == 0 {
			return true
		}
		ok := post(Chunk{Data: seal(aead, dirResponse, seq, job.ID, buf)})
		sent += int64(len(buf))
		buf, last = buf[:0], time.Now()
		return ok
	}
	res, err := n.runModel(ctx, job.Model, pl, func(delta string) error {
		// The requester pays per byte up to MaxOutBytes. Stop exactly there, on a whole character:
		// the coordinator drops any chunk that goes over.
		if room := job.MaxOutBytes - sent - int64(len(buf)); int64(len(delta)) > room {
			buf = append(buf, strings.ToValidUTF8(delta[:max(room, 0)], "")...)
			return errBudget
		}
		buf = append(buf, delta...)
		if time.Since(last) >= flushEvery || len(buf) > 2048 {
			if !flush() {
				return context.Canceled
			}
		}
		return nil
	})
	if errors.Is(err, errBudget) {
		res, err = chatResult{Truncated: true}, nil
	}
	if ctx.Err() != nil || !flush() {
		return false
	}
	final := Chunk{Done: true, PromptTokens: res.PromptTokens, CompletionTokens: res.CompletionTokens, Truncated: res.Truncated}
	if err != nil {
		log.Printf("job %s: %v", job.ID, err)
		final.Error = "the machine failed to run the model" // local details stay in the local log
	}
	return post(final) && err == nil
}

func (n *Node) hosts(model string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.phase == "ready" && slices.Contains(n.models, model)
}

// chat answers a prompt: for free on the local model when this machine hosts it,
// otherwise encrypted through the network, paid in credits.
func (n *Node) chat(ctx context.Context, model string, pl Payload, onDelta func(string)) (Chunk, bool, error) {
	if n.hosts(model) {
		res, err := n.runModel(ctx, model, pl, func(d string) error { onDelta(d); return nil })
		return Chunk{Done: true, PromptTokens: res.PromptTokens, CompletionTokens: res.CompletionTokens, Truncated: res.Truncated}, true, err
	}
	// Failover: when a machine goes away, another one is asked. If text had already reached the
	// user, the next machine is given that text as the start of its answer and goes on from there:
	// the answer is finished, not started again.
	var final Chunk
	var err error
	var written strings.Builder
	for attempt := 0; attempt < 3; attempt++ {
		ask := pl
		if written.Len() > 0 {
			ask.Messages = append(slices.Clone(pl.Messages), Message{Role: "assistant", Content: written.String()})
			if pl.MaxTokens > 0 {
				ask.MaxTokens = max(16, pl.MaxTokens-written.Len()/4)
			}
		}
		final, _, err = n.chatRemote(ctx, model, ask, func(d string) { written.WriteString(d); onDelta(d) })
		var refused *apiError
		// unknown_job: the reserved machine left before the job started, so nothing was spent either.
		if err == nil || ctx.Err() != nil || (errors.As(err, &refused) && refused.Code != "unknown_job") {
			break
		}
		log.Printf("machine failed after %d bytes of answer (%v): asking another one", written.Len(), err)
	}
	return final, false, err
}

// chatRemote runs one job on one machine of the network. started tells whether text was received.
func (n *Node) chatRemote(ctx context.Context, model string, pl Payload, onDelta func(string)) (final Chunk, started bool, err error) {
	var nj NewJobResp
	if err := n.callJSON(ctx, "POST", "/v1/jobs", NewJobReq{Model: model, MaxTokens: pl.MaxTokens}, &nj); err != nil {
		return Chunk{}, false, err
	}
	if !validWorkerKey(nj) {
		// The key offered is not the paid machine's: someone in the middle wants to read the prompt.
		n.raise("the coordinator offered an encryption key that does not belong to the machine being paid")
		return Chunk{}, false, &apiError{Code: "bad_key", Msg: "job refused: the encryption key is not the paid machine's"} // a refusal: not tried again
	}
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return Chunk{}, false, err
	}
	aead, err := jobKey(priv, nj.WorkerXPub, nj.ID)
	if err != nil {
		return Chunk{}, false, err
	}
	raw, _ := json.Marshal(pl)
	// This signature is the only thing that lets credits leave the account: it names the job,
	// the node that is paid and the highest price. The coordinator cannot take more or reuse it.
	maxCost := price(nj.Weight, int64(len(raw)), int64(nj.MaxTokens)*4)
	auth := ed25519.Sign(n.id.priv, payAuthMessage(nj.ID, nj.Worker, model, maxCost))
	resp, err := n.call(ctx, "POST", "/v1/jobs/"+nj.ID+"/start", StartReq{EphPub: priv.PublicKey().Bytes(),
		Payload: seal(aead, dirRequest, 0, nj.ID, raw), Max: maxCost, Auth: auth})
	if err != nil {
		return Chunk{}, false, err
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	var got int64
	for {
		var c Chunk
		if err := dec.Decode(&c); err != nil {
			return Chunk{}, got > 0, errors.New("the answer was cut off")
		}
		if len(c.Data) > 0 {
			plain, err := open(aead, dirResponse, c.Seq, nj.ID, c.Data)
			if err != nil {
				return Chunk{}, got > 0, errors.New("the answer could not be decrypted")
			}
			got += int64(len(plain))
			onDelta(string(plain))
		}
		if c.Done {
			// The node counted the bytes it received: it knows the fair price to the credit.
			if fair := price(nj.Weight, int64(len(raw)), got); c.Cost > fair || (got == 0 && c.Cost > 0) {
				n.raise(fmt.Sprintf("the coordinator charged %d credits for an answer worth %d", c.Cost, fair))
			}
			if c.Error != "" {
				return c, got > 0, errors.New(c.Error)
			}
			return c, got > 0, nil
		}
	}
}

// Handler is the local API and interface. hostport is the only address it answers to:
// that blocks other websites (CSRF, DNS rebinding) from spending the user's credits.
func (n *Node) Handler(hostport string) http.Handler {
	port := hostport[strings.LastIndex(hostport, ":"):]
	allowed := map[string]bool{"127.0.0.1" + port: true, "localhost" + port: true}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(indexHTML)
	})
	mux.HandleFunc("GET /api/status", n.handleStatus)
	mux.HandleFunc("POST /api/sharing", n.handleSharing)
	mux.HandleFunc("POST /api/share", n.handleShare)
	mux.HandleFunc("POST /api/split", n.handleSplit)
	mux.HandleFunc("GET /api/plan", n.handlePlan)
	mux.HandleFunc("POST /api/hold", n.handleHold)
	mux.HandleFunc("POST /api/head", n.handleHead)
	mux.HandleFunc("POST /api/uninstall", n.handleUninstall)
	mux.HandleFunc("POST /api/update", n.handleUpdate)
	mux.HandleFunc("GET /v1/models", n.handleModels)
	mux.HandleFunc("POST /v1/chat/completions", n.handleChat)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if !allowed[r.Host] || (origin != "" && !allowed[strings.TrimPrefix(origin, "http://")]) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

type Status struct {
	Version     string            `json:"version"`
	Key         string            `json:"key"`
	Coordinator string            `json:"coordinator"`
	Solo        bool              `json:"solo"`
	Offline     bool              `json:"offline"`
	Phase       string            `json:"phase"`
	Pct         float64           `json:"pct"`
	Detail      string            `json:"detail"`
	Warn        string            `json:"warn"`
	Hosted      []string          `json:"hosted"`   // models this machine offers
	Pull        *PullStatus       `json:"pull"`     // model being added
	Capacity    map[string]string `json:"capacity"` // per model: fast, slow or no
	Sharing     bool              `json:"sharing"`
	Working     bool              `json:"working"`
	Served      int               `json:"served"`
	Alert       string            `json:"alert"`
	Audit       *AuditStatus      `json:"audit"`
	Update      *Update           `json:"update"`
	Split       *SplitStatus      `json:"split"`
	Hold        *HoldStatus       `json:"hold"`
	Head        *HeadStatus       `json:"head"`
	Account     *Account          `json:"account"`
	Network     *Network          `json:"network"`
}

func (n *Node) handleStatus(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	s := Status{Version: version, Key: n.id.Key(), Coordinator: n.coord, Solo: n.solo, Phase: n.phase, Pct: n.pct,
		Detail: n.detail, Warn: n.warn, Hosted: slices.Clone(n.models), Sharing: n.sharing, Working: n.working, Served: n.served,
		Alert: n.alert, Audit: n.audited, Update: n.update}
	if n.pull != nil {
		pull := *n.pull
		s.Pull = &pull
	}
	if n.split != nil {
		split := *n.split
		// The interface shows who is in the group, not the secrets of their lines.
		split.Machines = make([]string, len(n.split.Machines))
		for i, m := range n.split.Machines {
			split.Machines[i], _, _ = strings.Cut(m, "#")
		}
		s.Split = &split
	}
	if n.hold != nil {
		hold := *n.hold
		s.Hold = &hold
	}
	if n.head != nil {
		head := *n.head
		s.Head = &head
	}
	n.mu.Unlock()
	var me Me
	if err := n.callJSON(r.Context(), "GET", "/v1/me", nil, &me); err == nil {
		s.Account, s.Network = &me.Account, &me.Network
		s.Capacity = map[string]string{}
		for _, m := range me.Network.Models {
			s.Capacity[m.ID] = n.capacity(m)
		}
	} else {
		s.Offline = true
	}
	writeJSON(w, 200, s)
}

func (n *Node) raise(alert string) {
	log.Print("ALERT: ", alert)
	n.mu.Lock()
	n.alert = alert
	n.mu.Unlock()
}

// audit downloads the public ledger and verifies all of it: every hash, every signature,
// every payment against the payer's means, and that nothing verified earlier has changed.
func (n *Node) audit(ctx context.Context) (AuditResult, error) {
	var net Network
	if err := n.callJSON(ctx, "GET", "/v1/network", nil, &net); err != nil {
		return AuditResult{}, err
	}
	path := filepath.Join(n.home, "checkpoint.json")
	var cp Checkpoint
	if raw, err := os.ReadFile(path); err == nil {
		json.Unmarshal(raw, &cp)
	}
	if cp.Coordinator != n.coord {
		cp = Checkpoint{} // first audit of this network
	}
	resp, err := n.call(ctx, "GET", "/v1/ledger", nil)
	if err != nil {
		return AuditResult{}, err
	}
	defer resp.Body.Close()
	res, err := auditChain(resp.Body, net.LedgerKey, net.LedgerSeq, cp)
	if err != nil {
		return res, err
	}
	raw, _ := json.Marshal(Checkpoint{Coordinator: n.coord, Key: net.LedgerKey, Seq: res.Entries, Hash: res.Head})
	return res, os.WriteFile(path, raw, 0o600)
}

// auditLoop makes every node an auditor: a coordinator that cheats is caught by all of them.
func (n *Node) auditLoop(ctx context.Context) {
	sleep(ctx, 5*time.Second)
	for ctx.Err() == nil {
		res, err := n.audit(ctx)
		wait := 30 * time.Minute
		var cheat ruleError
		switch {
		case err == nil:
			n.mu.Lock()
			n.audited = &AuditStatus{Entries: res.Entries, Circulating: res.Circulating, At: time.Now().Unix()}
			n.mu.Unlock()
		case errors.As(err, &cheat):
			n.raise("the ledger audit failed: " + err.Error())
		default:
			wait = time.Minute // network trouble: not a proof of anything, try again soon
		}
		sleep(ctx, wait)
	}
}

func (n *Node) handleSharing(w http.ResponseWriter, r *http.Request) {
	var req struct {
		On bool `json:"on"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&req) != nil {
		http.Error(w, "bad request", 400)
		return
	}
	n.mu.Lock()
	n.sharing = req.On
	if !req.On && n.cancelPoll != nil {
		n.cancelPoll() // leave the queue now; a job already running is finished
	}
	n.mu.Unlock()
	if !req.On {
		n.leave()
	}
	writeJSON(w, 200, map[string]bool{"sharing": req.On})
}

func (n *Node) handleModels(w http.ResponseWriter, r *http.Request) {
	var net Network
	if err := n.callJSON(r.Context(), "GET", "/v1/network", nil, &net); err != nil {
		openAIError(w, err)
		return
	}
	data := []map[string]any{}
	for _, m := range net.Models {
		data = append(data, map[string]any{"id": m.ID, "object": "model", "owned_by": "tor6ia"})
	}
	writeJSON(w, 200, map[string]any{"object": "list", "data": data})
}

func openAIError(w http.ResponseWriter, err error) {
	writeJSON(w, errStatus(err), errBody(err))
}

func errStatus(err error) int {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.Status
	}
	return 502
}

func errBody(err error) map[string]any {
	code := "upstream_error"
	var ae *apiError
	if errors.As(err, &ae) && ae.Code != "" {
		code = ae.Code
	}
	return map[string]any{"error": map[string]string{"message": err.Error(), "type": "tor6ia_error", "code": code}}
}

// messageText accepts the two OpenAI content shapes: a string, or a list of parts.
func messageText(raw json.RawMessage) (string, bool) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, true
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return "", false
	}
	for _, p := range parts {
		if p.Type != "text" {
			return "", false // images and audio are not supported
		}
		s += p.Text
	}
	return s, true
}

// handleChat is the OpenAI-compatible endpoint: any OpenAI client can point at this node.
func (n *Node) handleChat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Stream              bool     `json:"stream"`
		MaxTokens           int      `json:"max_tokens"`
		MaxCompletionTokens int      `json:"max_completion_tokens"`
		Temperature         *float64 `json:"temperature"`
		TopP                *float64 `json:"top_p"`
	}
	bad := func(msg string) {
		writeJSON(w, 400, errBody(&apiError{Status: 400, Code: "bad_request", Msg: msg}))
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody/2)).Decode(&req); err != nil {
		bad("invalid JSON body, or prompt above 512 KB")
		return
	}
	pl := Payload{MaxTokens: max(req.MaxTokens, req.MaxCompletionTokens), Temperature: req.Temperature, TopP: req.TopP}
	for _, m := range req.Messages {
		text, ok := messageText(m.Content)
		role := m.Role
		if role == "developer" {
			role = "system"
		}
		if !ok || (role != "system" && role != "user" && role != "assistant") {
			bad("only text messages with role system, user or assistant are supported")
			return
		}
		pl.Messages = append(pl.Messages, Message{Role: role, Content: text})
	}
	if req.Model == "" || len(pl.Messages) == 0 {
		bad("model and messages are required")
		return
	}

	id, created := "chatcmpl-"+randHex(8), time.Now().Unix()
	envelope := func(object string, choice map[string]any) map[string]any {
		return map[string]any{"id": id, "object": object, "created": created, "model": req.Model, "choices": []any{choice}}
	}
	finish := func(final Chunk) string {
		if final.Truncated {
			return "length"
		}
		return "stop"
	}
	usage := func(final Chunk, local bool) (map[string]any, map[string]any) {
		t6 := map[string]any{"cost": final.Cost, "local": local}
		if !local {
			t6["balance"] = final.Balance // a local answer never asks the coordinator, so it has no balance to report
		}
		return map[string]any{"prompt_tokens": final.PromptTokens, "completion_tokens": final.CompletionTokens,
			"total_tokens": final.PromptTokens + final.CompletionTokens}, t6
	}

	if !req.Stream {
		var out strings.Builder
		final, local, err := n.chat(r.Context(), req.Model, pl, func(d string) { out.WriteString(d) })
		if err != nil {
			openAIError(w, err)
			return
		}
		body := envelope("chat.completion", map[string]any{"index": 0, "finish_reason": finish(final),
			"message": map[string]string{"role": "assistant", "content": out.String()}})
		body["usage"], body["tor6ia"] = usage(final, local)
		writeJSON(w, 200, body)
		return
	}

	rc := http.NewResponseController(w)
	started := false
	send := func(v any) {
		if !started {
			started = true
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
		}
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", b)
		rc.Flush()
	}
	final, local, err := n.chat(r.Context(), req.Model, pl, func(d string) {
		send(envelope("chat.completion.chunk", map[string]any{"index": 0, "finish_reason": nil,
			"delta": map[string]string{"role": "assistant", "content": d}}))
	})
	if err != nil {
		if !started {
			openAIError(w, err) // nothing sent yet: a real HTTP status is still possible
			return
		}
		send(errBody(err))
		return
	}
	last := envelope("chat.completion.chunk", map[string]any{"index": 0, "finish_reason": finish(final), "delta": map[string]string{}})
	last["usage"], last["tor6ia"] = usage(final, local)
	send(last)
	fmt.Fprint(w, "data: [DONE]\n\n")
	rc.Flush()
}
