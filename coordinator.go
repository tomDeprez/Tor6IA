package main

import (
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	pollWait        = 25 * time.Second  // how long a worker poll hangs
	reserveWait     = 30 * time.Second  // how long a request waits for a free worker
	reserveTTL      = 15 * time.Second  // reserved worker is released if the job never starts
	deliverTimeout  = 20 * time.Second  // started job never picked up by its worker
	jobTimeout      = 180 * time.Second // silence from a working worker
	maxWorkersPerIP = 8
	maxStrikes      = 3
	banTime         = time.Hour
)

// The catalog is one model family at four sizes, so that people compare like with like.
// SizeGB is the real download size. Weight follows the work one token costs: gemma4:26b holds
// 26 billion parameters but uses 4 billion per token, so it is priced below the dense 31b.
var defaultCatalog = []Model{
	{ID: "gemma4:e2b", Name: "Gemma 4 E2B", Weight: 1, MinVRAMMB: 0, SizeGB: 4.6},
	{ID: "gemma4:12b", Name: "Gemma 4 12B", Weight: 4, MinVRAMMB: 10000, SizeGB: 8.0},
	{ID: "gemma4:26b", Name: "Gemma 4 26B", Weight: 6, MinVRAMMB: 22000, SizeGB: 18.7},
	{ID: "gemma4:31b", Name: "Gemma 4 31B", Weight: 10, MinVRAMMB: 24000, SizeGB: 20.4},
}

type worker struct {
	key         string
	xpub        []byte
	xsig        []byte // the account's signature of xpub
	models      []string
	ip          string
	lastSeen    time.Time
	job         *job      // reserved or running
	waiter      chan *job // set while a poll hangs
	speed       float64   // output bytes per second, moving average
	strikes     int
	bannedUntil time.Time
	coolUntil   time.Time // set when it fails a job: other machines are tried first
}

func (w *worker) alive(now time.Time) bool {
	return w.waiter != nil || w.job != nil || now.Sub(w.lastSeen) < 5*time.Second
}

type job struct {
	id        string
	model     string
	weight    int64
	requester string // empty for a canary
	w         *worker
	xpub      []byte // worker key at reservation time
	xsig      []byte
	ephPub    []byte
	payload   []byte // nil until the requester starts the job
	maxOut    int64
	inBytes   int64
	outBytes  int64
	hold      int64
	max       int64  // ceiling signed by the requester
	auth      []byte // that signature, written to the ledger with the payment
	nextSeq   uint64
	chunks    chan Chunk
	created   time.Time
	lastChunk time.Time
	delivered bool
	cancelled bool
	done      bool
}

// Coordinator matches requests to workers and keeps the credit ledger.
// It relays ciphertext only: prompts and answers are encrypted between requester and worker.
type Coordinator struct {
	Catalog     []Model
	CanaryEvery time.Duration // one rewarded canary per IP per period
	Tick        time.Duration
	TrustProxy  bool          // read the client IP from X-Forwarded-For
	CoolDown    time.Duration // how long a machine that failed a job is passed over
	RateBurst   float64       // requests one address may send at once
	RatePerSec  float64       // and how many more each second

	mu         sync.Mutex
	ledger     *Ledger
	workers    map[string]*worker
	jobs       map[string]*job
	held       map[string]int64
	lastCanary map[string]time.Time
	jobsDone   int64
	bytesOut   int64

	rateMu     sync.Mutex
	rate       map[string]*bucket // every request, per address
	ledgerRate map[string]*bucket // ledger downloads, which are large
}

type bucket struct {
	tokens float64
	last   time.Time
}

// allow is a token bucket per client address: a flood from one address gets 429s
// while everyone else is served.
func (c *Coordinator) allow(buckets map[string]*bucket, ip string, burst, perSecond float64) bool {
	c.rateMu.Lock()
	defer c.rateMu.Unlock()
	now := time.Now()
	b := buckets[ip]
	if b == nil {
		b = &bucket{tokens: burst, last: now}
		buckets[ip] = b
	}
	b.tokens = min(burst, b.tokens+now.Sub(b.last).Seconds()*perSecond)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func newCoordinator(dataDir string) (*Coordinator, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	id, err := loadIdentity(filepath.Join(dataDir, "coordinator.key"))
	if err != nil {
		return nil, err
	}
	l, err := openLedger(filepath.Join(dataDir, "chain.jsonl"), id)
	if err != nil {
		return nil, err
	}
	return &Coordinator{
		Catalog:     defaultCatalog,
		CanaryEvery: 10 * time.Minute,
		Tick:        5 * time.Second,
		CoolDown:    30 * time.Second,
		RateBurst:   300,
		RatePerSec:  60,
		rate:        map[string]*bucket{},
		ledgerRate:  map[string]*bucket{},
		ledger:      l,
		workers:     map[string]*worker{},
		jobs:        map[string]*job{},
		held:        map[string]int64{},
		lastCanary:  map[string]time.Time{},
	}, nil
}

func (c *Coordinator) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "Tor6IA coordinator "+version+"\n")
	})
	mux.HandleFunc("GET /v1/network", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		n := c.networkLocked()
		c.mu.Unlock()
		writeJSON(w, 200, n)
	})
	mux.HandleFunc("GET /v1/ledger", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		c.ledger.serve(w)
	})
	mux.HandleFunc("GET /v1/me", c.auth(c.handleMe))
	mux.HandleFunc("POST /v1/worker/poll", c.auth(c.handlePoll))
	mux.HandleFunc("POST /v1/worker/jobs/{id}/chunks", c.auth(c.handleChunk))
	mux.HandleFunc("POST /v1/worker/leave", c.auth(c.handleLeave))
	mux.HandleFunc("POST /v1/jobs", c.auth(c.handleNewJob))
	mux.HandleFunc("POST /v1/jobs/{id}/start", c.auth(c.handleStart))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := c.clientIP(r)
		ok := c.allow(c.rate, ip, c.RateBurst, c.RatePerSec)
		if ok && r.URL.Path == "/v1/ledger" {
			ok = c.allow(c.ledgerRate, ip, 4, 1.0/30) // a node audits every 30 minutes
		}
		if !ok {
			w.Header().Set("Retry-After", "5")
			httpErr(w, 429, "slow_down", "too many requests from this address")
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// Run drives the watchdog and the canaries until ctx ends.
func (c *Coordinator) Run(ctx context.Context) {
	t := time.NewTicker(c.Tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.sweep()
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}

type authed func(w http.ResponseWriter, r *http.Request, key string, body []byte)

func (c *Coordinator) auth(h authed) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
		if err != nil {
			httpErr(w, 413, "too_large", "request too large")
			return
		}
		key, err := verifyRequest(r, body)
		if err != nil {
			httpErr(w, 401, "unauthorized", err.Error())
			return
		}
		h(w, r, key, body)
	}
}

func (c *Coordinator) clientIP(r *http.Request) string {
	if c.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[len(parts)-1]) // the hop our own proxy added
		}
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}

func (c *Coordinator) model(id string) *Model {
	for i := range c.Catalog {
		if c.Catalog[i].ID == id {
			return &c.Catalog[i]
		}
	}
	return nil
}

func (c *Coordinator) networkLocked() Network {
	n := Network{Models: slices.Clone(c.Catalog), JobsDone: c.jobsDone, TokensOut: c.bytesOut / 4, LedgerKey: c.ledger.id.Key()}
	n.LedgerSeq, n.Circulating = c.ledger.summary()
	now := time.Now()
	for _, w := range c.workers {
		if !w.alive(now) || now.Before(w.bannedUntil) {
			continue
		}
		n.Workers++
		if w.job != nil {
			n.Busy++
		}
		for i := range n.Models {
			if slices.Contains(w.models, n.Models[i].ID) {
				n.Models[i].Workers++
			}
		}
	}
	return n
}

func (c *Coordinator) accountLocked(key string) Account {
	a, spendable := c.ledger.account(key)
	return Account{Key: key, Balance: a.Balance, Earned: a.Earned, Spent: a.Spent, Held: c.held[key],
		Spendable: spendable - c.held[key], Vouched: c.ledger.isVouched(key)}
}

func (c *Coordinator) handleMe(w http.ResponseWriter, r *http.Request, key string, _ []byte) {
	c.mu.Lock()
	me := Me{Account: c.accountLocked(key), Network: c.networkLocked()}
	c.mu.Unlock()
	writeJSON(w, 200, me)
}

// handlePoll is the worker's long poll: it returns a job, or 204 after pollWait.
func (c *Coordinator) handlePoll(rw http.ResponseWriter, r *http.Request, key string, body []byte) {
	var req PollReq
	if json.Unmarshal(body, &req) != nil || len(req.XPub) != 32 {
		httpErr(rw, 400, "bad_request", "bad poll")
		return
	}
	if pub, _ := hex.DecodeString(key); !ed25519.Verify(pub, xkeyMessage(req.XPub), req.XSig) {
		httpErr(rw, 400, "update_required", "this version of Tor6IA is too old for the network: update it")
		return
	}
	ip := c.clientIP(r)

	c.mu.Lock()
	w := c.workers[key]
	if w == nil {
		n := 0
		for _, o := range c.workers {
			if o.ip == ip {
				n++
			}
		}
		if n >= maxWorkersPerIP {
			c.mu.Unlock()
			httpErr(rw, 429, "too_many_workers", "too many workers on this IP address")
			return
		}
		w = &worker{key: key}
		c.workers[key] = w
	}
	w.ip, w.xpub, w.xsig, w.lastSeen = ip, req.XPub, req.XSig, time.Now()
	w.models = w.models[:0]
	for _, m := range req.Models {
		if c.model(m) != nil {
			w.models = append(w.models, m)
		}
	}
	if w.job != nil && w.job.delivered {
		// A worker that polls again while it holds a job has lost that job.
		c.failLocked(w.job, "the machine dropped the job", Chunk{})
	}
	j := w.job
	var ch chan *job
	if j == nil || j.payload == nil {
		j = nil
		ch = make(chan *job, 1)
		w.waiter = ch
	}
	c.mu.Unlock()

	if ch != nil {
		t := time.NewTimer(pollWait)
		select {
		case j = <-ch:
		case <-t.C:
		case <-r.Context().Done():
		}
		t.Stop()
	}

	c.mu.Lock()
	if ch != nil && w.waiter == ch {
		w.waiter = nil
	}
	if j == nil && ch != nil {
		select {
		case j = <-ch: // arrived between the timeout and the lock
		default:
		}
	}
	w.lastSeen = time.Now()
	if j != nil && (j.done || r.Context().Err() != nil) {
		j = nil // connection is gone: the job stays undelivered for the next poll
	}
	var msg JobMsg
	if j != nil {
		j.delivered = true
		j.lastChunk = time.Now()
		msg = JobMsg{ID: j.id, Model: j.model, EphPub: j.ephPub, Payload: j.payload, MaxOutBytes: j.maxOut}
	}
	c.mu.Unlock()

	if j == nil {
		rw.WriteHeader(204)
		return
	}
	writeJSON(rw, 200, msg)
}

// handleLeave removes a machine at once, so that everyone sees right away which models lost a machine.
// Without it the machine is only dropped a few seconds after its last poll.
func (c *Coordinator) handleLeave(rw http.ResponseWriter, r *http.Request, key string, _ []byte) {
	c.mu.Lock()
	if w := c.workers[key]; w != nil {
		if w.job != nil {
			c.failLocked(w.job, "the machine left the network", Chunk{})
		}
		delete(c.workers, key)
	}
	c.mu.Unlock()
	rw.WriteHeader(200)
}

func (c *Coordinator) handleChunk(rw http.ResponseWriter, r *http.Request, key string, body []byte) {
	var ch Chunk
	if json.Unmarshal(body, &ch) != nil {
		httpErr(rw, 400, "bad_request", "bad chunk")
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	j := c.jobs[r.PathValue("id")]
	if j == nil || j.w.key != key || !j.delivered {
		httpErr(rw, 410, "gone", "job is over") // tells the worker to stop generating
		return
	}
	now := time.Now()
	j.w.lastSeen = now
	if j.cancelled {
		c.finishLocked(j, "cancelled", Chunk{})
		httpErr(rw, 410, "gone", "job was cancelled")
		return
	}
	if ch.Seq < j.nextSeq {
		rw.WriteHeader(200) // duplicate of a chunk we already have
		return
	}
	if ch.Seq > j.nextSeq {
		httpErr(rw, 409, "out_of_order", "chunk out of order")
		return
	}
	j.nextSeq++
	j.lastChunk = now
	if n := int64(len(ch.Data)) - gcmTag; n > 0 {
		if j.outBytes+n > j.maxOut {
			c.finishLocked(j, "", Chunk{Truncated: true})
			httpErr(rw, 410, "gone", "output budget reached")
			return
		}
		j.outBytes += n
		select {
		case j.chunks <- Chunk{Seq: ch.Seq, Data: ch.Data}:
		default:
			c.finishLocked(j, "the requester stopped reading", Chunk{})
			httpErr(rw, 410, "gone", "requester stalled")
			return
		}
	}
	if ch.Error != "" {
		c.failLocked(j, ch.Error, ch)
	} else if ch.Done {
		c.finishLocked(j, "", ch)
	}
	rw.WriteHeader(200)
}

// failLocked ends a job that its machine could not finish. The requester's node asks another
// machine when it had received nothing yet, so this one is passed over for a while.
func (c *Coordinator) failLocked(j *job, errMsg string, last Chunk) {
	j.w.coolUntil = time.Now().Add(c.CoolDown)
	c.finishLocked(j, errMsg, last)
}

// finishLocked ends a job exactly once: it settles the payment, frees the worker and closes the stream.
func (c *Coordinator) finishLocked(j *job, errMsg string, last Chunk) {
	if j.done {
		return
	}
	j.done = true
	delete(c.jobs, j.id)
	w := j.w
	if w.job == j {
		w.job = nil
	}
	final := Chunk{Seq: j.nextSeq, Done: true, Error: errMsg, Truncated: last.Truncated,
		PromptTokens: last.PromptTokens, CompletionTokens: last.CompletionTokens}
	if j.requester != "" {
		if c.held[j.requester] -= j.hold; c.held[j.requester] <= 0 {
			delete(c.held, j.requester)
		}
		if j.outBytes > 0 {
			final.Cost = price(j.weight, j.inBytes, j.outBytes)
			if err := c.ledger.pay(j.requester, w.key, final.Cost, j.max, j.model, j.id, j.auth); err != nil {
				log.Printf("ledger: job %s not settled: %v", j.id, err)
				final.Cost = 0
			}
			c.jobsDone++
			c.bytesOut += j.outBytes
		}
		final.Balance = c.ledger.balance(j.requester)
	}
	if j.outBytes > 0 {
		s := float64(j.outBytes) / max(time.Since(j.created).Seconds(), 0.001)
		if w.speed == 0 {
			w.speed = s
		} else {
			w.speed = 0.7*w.speed + 0.3*s
		}
	}
	select {
	case j.chunks <- final:
	default:
	}
	close(j.chunks)
}

func (c *Coordinator) newJobLocked(m *Model, w *worker, requester string, maxTokens int64) *job {
	j := &job{id: randHex(12), model: m.ID, weight: m.Weight, requester: requester, w: w, xpub: w.xpub, xsig: w.xsig,
		maxOut: maxTokens * 4, chunks: make(chan Chunk, 1024), created: time.Now()}
	w.job = j
	c.jobs[j.id] = j
	return j
}

func (c *Coordinator) startLocked(j *job, ephPub, payload []byte) {
	j.ephPub, j.payload, j.lastChunk = ephPub, payload, time.Now()
	if w := j.w; w.waiter != nil {
		w.waiter <- j
		w.waiter = nil
	}
}

// pickLocked returns the fastest free worker for a model, and whether any worker serves it at all.
func (c *Coordinator) pickLocked(model string) (best *worker, any bool) {
	now := time.Now()
	for _, w := range c.workers {
		if !slices.Contains(w.models, model) || now.Before(w.bannedUntil) || !w.alive(now) {
			continue
		}
		any = true
		if w.job == nil && now.After(w.coolUntil) && (best == nil || w.speed > best.speed) {
			best = w
		}
	}
	return
}

// handleNewJob reserves a worker and returns its encryption key. The requester then starts the job.
func (c *Coordinator) handleNewJob(rw http.ResponseWriter, r *http.Request, key string, body []byte) {
	var req NewJobReq
	if json.Unmarshal(body, &req) != nil {
		httpErr(rw, 400, "bad_request", "bad job")
		return
	}
	m := c.model(req.Model)
	if m == nil {
		httpErr(rw, 404, "unknown_model", "unknown model: "+req.Model)
		return
	}
	if req.MaxTokens <= 0 {
		req.MaxTokens = maxTokensDefault
	}
	req.MaxTokens = min(req.MaxTokens, maxTokensCap)

	deadline := time.Now().Add(reserveWait)
	for {
		c.mu.Lock()
		if c.accountLocked(key).Spendable < m.Weight {
			c.mu.Unlock()
			httpErr(rw, 402, "insufficient_credits", "not enough credits")
			return
		}
		w, any := c.pickLocked(m.ID)
		if w != nil {
			j := c.newJobLocked(m, w, key, int64(req.MaxTokens))
			c.mu.Unlock()
			writeJSON(rw, 200, NewJobResp{ID: j.id, WorkerXPub: j.xpub, WorkerXSig: j.xsig, Worker: w.key, Weight: m.Weight, MaxTokens: req.MaxTokens})
			return
		}
		c.mu.Unlock()
		if !any {
			httpErr(rw, 503, "no_worker", "no node serves this model right now")
			return
		}
		if time.Now().After(deadline) {
			httpErr(rw, 503, "busy", "all nodes are busy, try again")
			return
		}
		// shortcut: waiting requests poll instead of queuing, so order is not strict; use a real queue once nodes are often saturated.
		select {
		case <-time.After(250 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
	}
}

// handleStart takes the encrypted prompt and the signed payment ceiling, puts the cost in escrow
// and streams back encrypted chunks.
func (c *Coordinator) handleStart(rw http.ResponseWriter, r *http.Request, key string, body []byte) {
	var req StartReq
	if json.Unmarshal(body, &req) != nil || len(req.EphPub) != 32 || len(req.Payload) <= gcmTag {
		httpErr(rw, 400, "bad_request", "bad start")
		return
	}
	c.mu.Lock()
	j := c.jobs[r.PathValue("id")]
	if j == nil || j.requester != key || j.payload != nil {
		c.mu.Unlock()
		httpErr(rw, 404, "unknown_job", "unknown or expired job")
		return
	}
	if pub, _ := hex.DecodeString(key); !ed25519.Verify(pub, payAuthMessage(j.id, j.w.key, j.model, req.Max), req.Auth) {
		c.finishLocked(j, "bad payment signature", Chunk{})
		c.mu.Unlock()
		httpErr(rw, 400, "bad_request", "bad payment signature")
		return
	}
	j.max, j.auth = req.Max, req.Auth
	j.inBytes = int64(len(req.Payload)) - gcmTag
	inCost := j.weight * ceilDiv(j.inBytes, 40)
	avail := c.accountLocked(key).Spendable
	afford := (min(avail, req.Max) - inCost) / j.weight * 4 // output bytes the balance and the signature can pay for
	if afford < 4 {
		c.finishLocked(j, "not enough credits", Chunk{})
		c.mu.Unlock()
		httpErr(rw, 402, "insufficient_credits", "not enough credits for this prompt")
		return
	}
	j.maxOut = min(j.maxOut, afford)
	j.hold = inCost + j.weight*ceilDiv(j.maxOut, 4)
	c.held[key] += j.hold
	c.startLocked(j, req.EphPub, req.Payload)
	c.mu.Unlock()

	rw.Header().Set("Content-Type", "application/x-ndjson")
	rw.Header().Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(rw)
	enc := json.NewEncoder(rw)
	rc.Flush()
	for {
		select {
		case ch, ok := <-j.chunks:
			if !ok {
				return
			}
			enc.Encode(ch)
			rc.Flush()
		case <-r.Context().Done():
			c.mu.Lock()
			j.cancelled = true // the worker is told at its next chunk
			c.mu.Unlock()
			return
		}
	}
}

// sweep ends stuck jobs, forgets dead workers and sends canaries to idle ones.
func (c *Coordinator) sweep() {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for _, j := range c.jobs {
		switch {
		case j.payload == nil && now.Sub(j.created) > reserveTTL:
			c.finishLocked(j, "the job was never started", Chunk{})
		case j.payload != nil && !j.delivered && now.Sub(j.lastChunk) > deliverTimeout:
			c.failLocked(j, "the machine did not answer", Chunk{})
		case j.delivered && now.Sub(j.lastChunk) > jobTimeout:
			c.failLocked(j, "the machine stopped answering", Chunk{})
		}
	}
	for k, w := range c.workers {
		if w.job == nil && w.waiter == nil && now.Sub(w.lastSeen) > 2*time.Minute {
			delete(c.workers, k)
		}
	}
	c.rateMu.Lock()
	for _, buckets := range []map[string]*bucket{c.rate, c.ledgerRate} {
		for ip, b := range buckets {
			if now.Sub(b.last) > 3*time.Minute { // long enough for any bucket to be full again
				delete(buckets, ip)
			}
		}
	}
	c.rateMu.Unlock()
	for ip, t := range c.lastCanary {
		if now.Sub(t) > c.CanaryEvery {
			delete(c.lastCanary, ip)
		}
	}
	for _, w := range c.workers {
		if w.job != nil || w.waiter == nil || len(w.models) == 0 || now.Before(w.bannedUntil) {
			continue
		}
		if _, recent := c.lastCanary[w.ip]; recent {
			continue
		}
		c.lastCanary[w.ip] = now
		go c.runCanary(c.newJobLocked(c.model(w.models[0]), w, "", 32))
	}
}

var canaryWords = strings.Fields("river amber stone maple cloud tiger lemon silver garden window bridge candle forest harbor meadow pepper " +
	"rabbit saddle thunder velvet walnut yellow anchor basket copper dragon feather island jacket kettle ladder marble")

// canaryPhrase is three random plain words. Plain words on purpose: llama3.2:3b refuses to repeat
// anything called a "code", and copied these 30 times out of 30.
func canaryPhrase() string {
	b := make([]byte, 3)
	rand.Read(b)
	return canaryWords[b[0]%32] + " " + canaryWords[b[1]%32] + " " + canaryWords[b[2]%32]
}

// runCanary sends a job whose answer is known. It is the spot check for honest workers,
// and the first pass vouches for the account: it opens the overdraft that lets a new member start.
// No credit is created.
// shortcut: it proves the worker runs some model, not the model it claims; add cross-worker answer comparison if model fraud appears.
func (c *Coordinator) runCanary(j *job) {
	code := canaryPhrase()
	var out strings.Builder
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err == nil {
		aead, kerr := jobKey(priv, j.xpub, j.id)
		if err = kerr; err == nil {
			zero := 0.0 // no sampling: an honest worker must pass every time
			pl, _ := json.Marshal(Payload{MaxTokens: 32, Temperature: &zero, Messages: []Message{
				{Role: "user", Content: "Copy these three words exactly, with nothing else: " + code}}})
			c.mu.Lock()
			c.startLocked(j, priv.PublicKey().Bytes(), seal(aead, dirRequest, 0, j.id, pl))
			c.mu.Unlock()
			for ch := range j.chunks {
				if pt, err := open(aead, dirResponse, ch.Seq, j.id, ch.Data); err == nil {
					out.Write(pt)
				}
			}
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.finishLocked(j, "bad worker key", Chunk{})
	}
	w := j.w
	if strings.Contains(strings.ToLower(out.String()), code) {
		w.strikes = 0
		if !c.ledger.isVouched(w.key) { // later canaries are only spot checks
			if err := c.ledger.vouch(w.key, j.id); err != nil {
				log.Printf("ledger: vouch not written: %v", err)
			}
		}
		return
	}
	log.Printf("canary failed for worker %.8s (strike %d): answered %q, want %s", w.key, w.strikes+1, out.String(), code)
	if w.strikes++; w.strikes >= maxStrikes {
		w.strikes = 0
		w.bannedUntil = time.Now().Add(banTime)
	}
}
