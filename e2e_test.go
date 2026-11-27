package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// deletedModel is the last model the fake Ollama was asked to delete.
var deletedModel atomic.Value

// fakeOllama answers /api/chat by echoing the last message word by word.
// Setting failNext makes the next call fail, like a machine whose model crashed.
func fakeOllama(t *testing.T) (string, *atomic.Bool) {
	failNext := new(atomic.Bool)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/delete" {
			var req struct{ Model string }
			json.NewDecoder(r.Body).Decode(&req)
			deletedModel.Store(req.Model)
			return
		}
		if failNext.CompareAndSwap(true, false) {
			http.Error(w, "model crashed", 500)
			return
		}
		var req struct{ Messages []Message }
		json.NewDecoder(r.Body).Decode(&req)
		words := strings.Fields(req.Messages[len(req.Messages)-1].Content)
		enc := json.NewEncoder(w)
		for _, word := range words {
			enc.Encode(map[string]any{"message": map[string]string{"content": word + " "}})
			w.(http.Flusher).Flush()
		}
		enc.Encode(map[string]any{"done": true, "prompt_eval_count": 7, "eval_count": len(words)})
	}))
	t.Cleanup(srv.Close)
	return srv.URL, failNext
}

func testNode(t *testing.T, coord, ollama, model string) *Node {
	home := t.TempDir()
	id, err := loadIdentity(filepath.Join(home, "identity.key"))
	if err != nil {
		t.Fatal(err)
	}
	n := newNode(id, coord, ollama, home, false)
	if model != "" {
		n.models, n.phase = []string{model}, "ready"
	}
	return n
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatal("timeout waiting for " + what)
}

func TestNetwork(t *testing.T) {
	dir := t.TempDir()
	c, err := newCoordinator(dir)
	if err != nil {
		t.Fatal(err)
	}
	c.Tick, c.CanaryEvery = 20*time.Millisecond, 60*time.Millisecond
	c.Catalog = []Model{{ID: "big", Name: "Big", Weight: 3}, {ID: "small", Name: "Small", Weight: 1}}
	ctx, cancel := context.WithCancel(context.Background())
	go c.Run(ctx)
	srv := httptest.NewServer(c.Handler())
	t.Cleanup(func() {
		cancel() // ends the workers' long polls, so the server can close
		srv.Close()
		c.ledger.f.Close()
	})
	ollama, failNext := fakeOllama(t)
	c.CoolDown = 50 * time.Millisecond
	c.RateBurst, c.RatePerSec = 1e6, 1e6 // every node of this test shares one address
	ask := func(text string, maxTokens int) Payload {
		return Payload{MaxTokens: maxTokens, Messages: []Message{{Role: "user", Content: text}}}
	}

	// A node that passes an encrypted canary is vouched for. No credit is created: it stays at zero.
	worker := testNode(t, srv.URL, ollama, "big")
	go worker.workLoop(ctx)
	waitFor(t, "worker vouch", func() bool { return c.ledger.isVouched(worker.id.Key()) })
	if got := c.ledger.balance(worker.id.Key()); got != 0 {
		t.Fatalf("a vouched account has %d credits, want 0", got)
	}

	// An account that never computed for anyone cannot spend.
	stranger := testNode(t, srv.URL, ollama, "")
	_, _, err = stranger.chat(ctx, "big", ask("hi", 0), func(string) {})
	var ae *apiError
	if !errors.As(err, &ae) || ae.Status != 402 || ae.Code != "insufficient_credits" {
		t.Fatalf("stranger: got %v, want 402 insufficient_credits", err)
	}

	// A user who shares a small model is vouched for too, and can then use the big one on credit.
	user := testNode(t, srv.URL, ollama, "small")
	go user.workLoop(ctx)
	waitFor(t, "user vouch", func() bool { return c.ledger.isVouched(user.id.Key()) })
	c.mu.Lock()
	c.CanaryEvery = time.Hour // no more canaries: the rest of the test owns the workers
	c.mu.Unlock()

	local := httptest.NewUnstartedServer(nil)
	local.Config.Handler = user.Handler(local.Listener.Addr().String())
	local.Start()
	defer local.Close()

	body := `{"model":"big","stream":true,"messages":[{"role":"user","content":"bonjour le monde"}]}`
	resp, err := http.Post(local.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("chat: %v %v", err, resp)
	}
	var answer string
	var cost, balance int64
	sawDone := false
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		if data == "[DONE]" {
			sawDone = true
			continue
		}
		var ev struct {
			Choices []struct {
				Delta struct{ Content string }
			}
			Tor6ia *struct {
				Cost, Balance int64
				Local         bool
			}
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			t.Fatalf("bad event %q: %v", data, err)
		}
		answer += ev.Choices[0].Delta.Content
		if ev.Tor6ia != nil {
			cost, balance = ev.Tor6ia.Cost, ev.Tor6ia.Balance
			if ev.Tor6ia.Local {
				t.Fatal("answer marked local, want network")
			}
		}
	}
	resp.Body.Close()
	if answer != "bonjour le monde " || !sawDone {
		t.Fatalf("answer %q done=%v", answer, sawDone)
	}

	// The user pays exactly what the worker earns: the price of the bytes computed, times the model weight.
	wantCost := price(3, int64(len(`{"messages":[{"role":"user","content":"bonjour le monde"}]}`)), int64(len(answer)))
	if cost != wantCost || balance != -cost {
		t.Fatalf("cost %d balance %d, want cost %d balance %d", cost, balance, wantCost, -wantCost)
	}
	if u, w := c.ledger.balance(user.id.Key()), c.ledger.balance(worker.id.Key()); u != -cost || w != cost {
		t.Fatalf("user %d worker %d, want %d and %d", u, w, -cost, cost)
	}
	c.mu.Lock()
	held := len(c.held)
	c.mu.Unlock()
	if held != 0 || user.alert != "" {
		t.Fatalf("escrow left on %d accounts, alert %q", held, user.alert)
	}

	// max_tokens caps the output, and the bill with it.
	var got strings.Builder
	final, _, err := user.chat(ctx, "big", ask("one two three four five six seven eight nine ten", 2), func(d string) { got.WriteString(d) })
	if err != nil || !final.Truncated || got.String() != "one two " {
		t.Fatalf("cap: err=%v truncated=%v answer=%q", err, final.Truncated, got.String())
	}

	// Failover: the machine fails before sending anything, the user's node asks again and gets its answer.
	// The failed attempt costs nothing.
	before := c.ledger.balance(user.id.Key())
	failNext.Store(true)
	got.Reset()
	final, _, err = user.chat(ctx, "big", ask("still here", 0), func(d string) { got.WriteString(d) })
	if err != nil || got.String() != "still here " || failNext.Load() {
		t.Fatalf("failover: err=%v answer=%q", err, got.String())
	}
	if paid := before - c.ledger.balance(user.id.Key()); paid != final.Cost || paid <= 0 {
		t.Fatalf("failover: paid %d, the one answer cost %d", paid, final.Cost)
	}

	// The ledger refuses what the rules forbid, even when the coordinator itself asks.
	wk, uk := worker.id.Key(), user.id.Key()
	sign := func(job string, max int64) []byte {
		return ed25519.Sign(user.id.priv, payAuthMessage(job, wk, "big", max))
	}
	if err := c.ledger.pay(uk, wk, 50, 50, "big", "forged", []byte("not a signature")); err == nil {
		t.Fatal("payment without the payer's signature was accepted")
	}
	if err := c.ledger.pay(uk, wk, 60, 50, "big", "above", sign("above", 50)); err == nil {
		t.Fatal("payment above the signed ceiling was accepted")
	}
	if err := c.ledger.pay(uk, wk, 1000, 1000, "big", "deep", sign("deep", 1000)); err == nil {
		t.Fatal("payment beyond the overdraft was accepted")
	}

	// Credits are only moved, never created: all balances add up to zero.
	var sum int64
	for _, a := range c.ledger.accts {
		sum += a.Balance
	}
	if sum != 0 {
		t.Fatalf("balances add up to %d, want 0", sum)
	}

	// Any node can verify the whole public ledger, twice in a row (the second run uses the checkpoint).
	for i := 0; i < 2; i++ {
		res, err := user.audit(ctx)
		if err != nil || res.Entries != 5 || res.Circulating != c.ledger.balance(wk) {
			t.Fatalf("audit %d: %+v %v", i, res, err)
		}
	}

	// A tampered ledger is caught.
	r, err := http.Get(srv.URL + "/v1/ledger")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(r.Body)
	r.Body.Close()
	key := c.ledger.id.Key()
	lines := bytes.SplitAfter(raw, []byte("\n"))
	var rule ruleError
	cheats := map[string][]byte{
		"changed amount": bytes.Replace(raw, []byte(`"amount":`), []byte(`"amount":1`), 1),
		"removed entry":  bytes.Join(lines[1:], nil),
		"swapped payer":  bytes.Replace(raw, []byte(`"from":"`+uk), []byte(`"from":"`+stranger.id.Key()), 1),
	}
	for name, bad := range cheats {
		if _, err := auditChain(bytes.NewReader(bad), key, 0, Checkpoint{}); !errors.As(err, &rule) {
			t.Fatalf("%s: audit said %v, want a rule violation", name, err)
		}
	}
	if _, err := auditChain(bytes.NewReader(raw), key, 5, Checkpoint{Key: key, Seq: 2, Hash: "other"}); !errors.As(err, &rule) {
		t.Fatalf("rewritten history: audit said %v, want a rule violation", err)
	}
	if _, err := auditChain(bytes.NewReader(raw), key, 6, Checkpoint{}); err != errIncomplete {
		t.Fatalf("short download: audit said %v, want errIncomplete", err)
	}

	// The user stops sharing its only model and deletes it: the files go, and the machine leaves the
	// network at once, so everyone sees that the model lost a machine.
	resp, err = http.Post(local.URL+"/api/share", "application/json", strings.NewReader(`{"model":"small","on":false,"delete":true}`))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("unshare: %v %v", err, resp)
	}
	resp.Body.Close()
	c.mu.Lock()
	_, stillThere := c.workers[uk]
	smallMachines := c.networkLocked().Models[1].Workers
	c.mu.Unlock()
	if deletedModel.Load() != "small" || user.hosts("small") || stillThere || smallMachines != 0 {
		t.Fatalf("unshare: deleted=%v hosts=%v worker listed=%v machines=%d", deletedModel.Load(), user.hosts("small"), stillThere, smallMachines)
	}

	// Another website cannot use the local API.
	req, _ := http.NewRequest("GET", local.URL+"/api/status", nil)
	req.Header.Set("Origin", "https://evil.example")
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != 403 {
		t.Fatalf("foreign origin got %d, want 403", r.StatusCode)
	}

	// After a restart, the coordinator replays the ledger to the same state.
	l2, err := openLedger(filepath.Join(dir, "chain.jsonl"), c.ledger.id)
	if err != nil {
		t.Fatal(err)
	}
	defer l2.f.Close()
	for _, n := range []*Node{worker, user, stranger} {
		a, as := c.ledger.account(n.id.Key())
		b, bs := l2.account(n.id.Key())
		if a != b || as != bs {
			t.Fatalf("replay differs: %+v/%d vs %+v/%d", a, as, b, bs)
		}
	}
}

func TestCryptoAndSignature(t *testing.T) {
	home := t.TempDir()
	id, _ := loadIdentity(filepath.Join(home, "k"))
	body := []byte(`{"a":1}`)
	req, _ := http.NewRequest("POST", "http://x/v1/jobs", nil)
	id.sign(req, body)
	if key, err := verifyRequest(req, body); err != nil || key != id.Key() {
		t.Fatalf("valid signature rejected: %v", err)
	}
	if _, err := verifyRequest(req, []byte(`{"a":2}`)); err == nil {
		t.Fatal("tampered body accepted")
	}
	req.URL.Path = "/v1/me"
	if _, err := verifyRequest(req, body); err == nil {
		t.Fatal("signature accepted on another path")
	}

	a, b := newNode(id, "", "", home, false), newNode(id, "", "", home, false)
	ka, _ := jobKey(a.xpriv, b.xpriv.PublicKey().Bytes(), "job1")
	kb, _ := jobKey(b.xpriv, a.xpriv.PublicKey().Bytes(), "job1")
	sealed := seal(ka, dirResponse, 4, "job1", []byte("secret"))
	if plain, err := open(kb, dirResponse, 4, "job1", sealed); err != nil || string(plain) != "secret" {
		t.Fatalf("round trip failed: %v", err)
	}
	if _, err := open(kb, dirResponse, 5, "job1", sealed); err == nil {
		t.Fatal("chunk accepted at the wrong position")
	}
	if _, err := open(kb, dirResponse, 4, "job2", sealed); err == nil {
		t.Fatal("chunk accepted for another job")
	}

	// A job offer is accepted only when its encryption key is signed by the account it pays.
	xpub := a.xpriv.PublicKey().Bytes()
	offer := NewJobResp{Worker: id.Key(), WorkerXPub: xpub, WorkerXSig: ed25519.Sign(id.priv, xkeyMessage(xpub))}
	if !validWorkerKey(offer) {
		t.Fatal("genuine worker key refused")
	}
	offer.WorkerXPub = b.xpriv.PublicKey().Bytes() // a key slipped in by someone in the middle
	if validWorkerKey(offer) {
		t.Fatal("substituted encryption key accepted")
	}
}

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		tag, current string
		want         bool
	}{
		{"v0.1.1", "0.1.0", true}, {"v0.2.0", "0.1.9", true}, {"v1.0.0", "0.9.9", true}, {"v0.10.0", "0.9.0", true},
		{"v0.1.0", "0.1.0", false}, {"v0.1.0", "0.1.1", false}, {"v0.1.1", "dev", false}, {"latest", "0.1.0", false},
	} {
		if got := newer(c.tag, c.current); got != c.want {
			t.Errorf("newer(%q, %q) = %v, want %v", c.tag, c.current, got, c.want)
		}
	}
}

// The engine answers like an OpenAI server: text in deltas, then the token counts.
func TestEngineChat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			MaxTokens int `json:"max_tokens"`
			Stream    bool
		}
		json.NewDecoder(r.Body).Decode(&req)
		if !req.Stream || req.MaxTokens != 7 {
			http.Error(w, "bad request", 400)
			return
		}
		for _, line := range []string{
			`{"choices":[{"delta":{"role":"assistant","content":""},"finish_reason":null}]}`,
			`{"choices":[{"delta":{"content":"Bon"},"finish_reason":null}]}`,
			`{"choices":[{"delta":{"content":"jour"},"finish_reason":null}]}`,
			`{"choices":[{"delta":{},"finish_reason":"length"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":7}}`,
			`[DONE]`,
		} {
			io.WriteString(w, "data: "+line+"\n\n")
		}
	}))
	defer srv.Close()
	var got strings.Builder
	res, err := engineChat(context.Background(), srv.URL, Payload{MaxTokens: 7, Messages: []Message{{Role: "user", Content: "hi"}}},
		func(d string) error { got.WriteString(d); return nil })
	if err != nil || got.String() != "Bonjour" || !res.Truncated || res.PromptTokens != 12 || res.CompletionTokens != 7 {
		t.Fatalf("got %q %+v %v", got.String(), res, err)
	}
	if _, err := engineChat(context.Background(), srv.URL, Payload{MaxTokens: 1}, func(string) error { return nil }); err == nil {
		t.Fatal("a refused request was reported as an answer")
	}
}
