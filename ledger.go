package main

import (
	"bufio"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"time"
)

// The money rule. Anyone can check it against the public ledger (tor6ia audit).
//
// Credits are never created. A payment moves credits from the account that asked to the
// account that computed, or to the accounts of a chain that computed together, so all balances
// always add up to zero. Everyone starts at zero;
// an account that proved it computes (one passed canary, recorded as a "vouch") may go
// creditLine below zero. That overdraft is what lets the first request happen.
const creditLine = 200

// ruleError means the ledger itself breaks a rule: the coordinator cheated or the file is damaged.
type ruleError string

func (e ruleError) Error() string { return string(e) }

func broken(format string, args ...any) error { return ruleError(fmt.Sprintf(format, args...)) }

// entry is one line of the public ledger. Entries form a hash chain: changing or
// removing an old one changes every hash after it.
type entry struct {
	Seq    uint64 `json:"seq"`
	Time   int64  `json:"t"`
	Type   string `json:"type"` // "vouch" or "pay"
	From   string `json:"from,omitempty"`
	To     string `json:"to"`
	Amount int64  `json:"amount,omitempty"`
	Max    int64  `json:"max,omitempty"`   // pay: the ceiling the payer signed
	Model  string `json:"model,omitempty"` // pay: the model that was used
	Job    string `json:"job"`
	Sig    string `json:"sig,omitempty"` // pay: the payer's signature of (job, to, model, max, shares)
	// pay, when a chain computed: the accounts that share Amount and the parts of each. To is one
	// of them and also gets what the division leaves over.
	Shares []Share `json:"shares,omitempty"`
	Prev   string  `json:"prev"`
	Hash   string  `json:"hash"`
	CSig   string  `json:"csig"` // the coordinator's signature of Hash
}

func (e entry) digest() string {
	e.Hash, e.CSig = "", ""
	b, _ := json.Marshal(e)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// payAuthMessage is what a requester signs before a job: without that signature
// the coordinator cannot take a single credit from the account. It names who is paid: the
// coordinator cannot send a part of the payment to anyone else.
func payAuthMessage(job, to, model string, max int64, shares []Share) []byte {
	msg := "tor6ia/pay\n" + job + "\n" + to + "\n" + model + "\n" + strconv.FormatInt(max, 10)
	for _, s := range shares {
		msg += "\n" + s.To + " " + strconv.FormatInt(s.Parts, 10)
	}
	return []byte(msg)
}

// payout is one account's amount in a payment.
type payout struct {
	to     string
	amount int64
}

// payouts divides a payment between its shares. The rule is public: each account gets the whole
// number of credits its parts give, and the account that answered gets what is left.
func (e entry) payouts() []payout {
	if len(e.Shares) == 0 {
		return []payout{{e.To, e.Amount}}
	}
	var parts, given int64
	for _, s := range e.Shares {
		parts += s.Parts
	}
	out := make([]payout, len(e.Shares))
	main := 0
	for i, s := range e.Shares {
		out[i] = payout{s.To, e.Amount * s.Parts / parts}
		given += out[i].amount
		if s.To == e.To {
			main = i
		}
	}
	out[main].amount += e.Amount - given
	return out
}

// validShares checks the shares of a payment: real accounts, each once, positive parts, and the
// account that answered among them.
func validShares(to string, shares []Share) bool {
	if len(shares) == 0 {
		return true
	}
	seen, hasTo, total := map[string]bool{}, false, int64(0)
	for _, s := range shares {
		key, _ := hex.DecodeString(s.To)
		if len(key) != ed25519.PublicKeySize || seen[s.To] || s.Parts <= 0 || s.Parts > 1<<20 {
			return false
		}
		seen[s.To] = true
		hasTo = hasTo || s.To == to
		total += s.Parts
	}
	return hasTo && len(shares) <= 64 && total > 0
}

type acct struct{ Balance, Earned, Spent int64 }

// chain is the state obtained by reading the ledger from its first entry.
// The coordinator and every auditing node run the same checks.
type chain struct {
	key      ed25519.PublicKey // the coordinator's ledger key
	seq      uint64
	head     string
	lastTime int64
	owed     int64 // sum of the positive balances: what the network owes its members
	accts    map[string]*acct
	vouched  map[string]bool
	// shortcut: every job id is kept in memory to refuse a replayed payment (about 70 MB per million jobs); bind payments to a ledger position instead once that hurts.
	jobs map[string]struct{}
}

func newChain(key ed25519.PublicKey) chain {
	return chain{key: key, accts: map[string]*acct{}, vouched: map[string]bool{}, jobs: map[string]struct{}{}}
}

func (c *chain) get(key string) *acct {
	a := c.accts[key]
	if a == nil {
		a = &acct{}
		c.accts[key] = a
	}
	return a
}

// spendable is the balance plus the overdraft the account is allowed.
func (c *chain) spendable(key string) int64 {
	var n int64
	if a := c.accts[key]; a != nil {
		n = a.Balance
	}
	if c.vouched[key] {
		n += creditLine
	}
	return n
}

// check says whether e is a legal next entry.
func (c *chain) check(e entry) error {
	csig, _ := hex.DecodeString(e.CSig)
	_, dup := c.jobs[e.Job]
	switch {
	case e.Seq != c.seq+1:
		return broken("entry %d is out of sequence", e.Seq)
	case e.Prev != c.head:
		return broken("entry %d does not follow the previous one: history was changed", e.Seq)
	case e.Hash != e.digest():
		return broken("entry %d was modified", e.Seq)
	case !ed25519.Verify(c.key, []byte(e.Hash), csig):
		return broken("entry %d is not signed by the coordinator", e.Seq)
	case e.Time < c.lastTime:
		return broken("entry %d goes back in time", e.Seq)
	case e.Job == "" || dup:
		return broken("entry %d reuses job %q", e.Seq, e.Job)
	}
	switch e.Type {
	case "vouch":
		if to, _ := hex.DecodeString(e.To); len(to) != ed25519.PublicKeySize || e.Amount != 0 || c.vouched[e.To] {
			return broken("entry %d is not a valid vouch", e.Seq)
		}
	case "pay":
		from, _ := hex.DecodeString(e.From)
		sig, _ := hex.DecodeString(e.Sig)
		switch {
		case len(from) != ed25519.PublicKeySize || !ed25519.Verify(from, payAuthMessage(e.Job, e.To, e.Model, e.Max, e.Shares), sig):
			return broken("entry %d: payment was not signed by the payer", e.Seq)
		case !validShares(e.To, e.Shares):
			return broken("entry %d: the shares of the payment are not valid", e.Seq)
		case e.Amount <= 0 || e.Amount > e.Max:
			return broken("entry %d: payment is above what the payer signed", e.Seq)
		case c.spendable(e.From) < e.Amount:
			return broken("entry %d: payment is above what the payer can spend", e.Seq)
		}
	default:
		return broken("entry %d has an unknown type %q", e.Seq, e.Type)
	}
	return nil
}

func (c *chain) move(key string, delta int64) *acct {
	a := c.get(key)
	c.owed -= max(a.Balance, 0)
	a.Balance += delta
	c.owed += max(a.Balance, 0)
	return a
}

func (c *chain) apply(e entry) {
	c.seq, c.head, c.lastTime = e.Seq, e.Hash, e.Time
	c.jobs[e.Job] = struct{}{}
	if e.Type == "vouch" {
		c.vouched[e.To] = true
		return
	}
	c.move(e.From, -e.Amount).Spent += e.Amount
	for _, p := range e.payouts() {
		c.move(p.to, p.amount).Earned += p.amount
	}
}

// readChain checks and applies every entry of r. It returns how many bytes were valid.
func (c *chain) readChain(r io.Reader, each func(entry) error) (int64, error) {
	br := bufio.NewReader(r)
	var off int64
	for {
		line, rerr := br.ReadBytes('\n')
		if rerr == io.EOF {
			return off, nil // a partial last line is a write cut by a crash
		}
		if rerr != nil {
			return off, rerr
		}
		var e entry
		if err := json.Unmarshal(line, &e); err != nil {
			return off, broken("ledger damaged at byte %d: %v", off, err)
		}
		if err := c.check(e); err != nil {
			return off, err
		}
		if each != nil {
			if err := each(e); err != nil {
				return off, err
			}
		}
		c.apply(e)
		off += int64(len(line))
	}
}

// Ledger is the coordinator's copy of the chain: an append-only file, replayed and verified at startup.
type Ledger struct {
	mu   sync.Mutex
	f    *os.File
	path string
	id   Identity
	chain
}

func openLedger(path string, id Identity) (*Ledger, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	l := &Ledger{f: f, path: path, id: id, chain: newChain(id.priv.Public().(ed25519.PublicKey))}
	off, err := l.readChain(f, nil)
	if err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Truncate(off); err != nil {
		return nil, err
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	return l, nil
}

// add completes, signs, checks and writes one entry. An entry that breaks a rule is never written.
func (l *Ledger) add(e entry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	e.Seq, e.Prev, e.Time = l.seq+1, l.head, max(time.Now().Unix(), l.lastTime)
	e.Hash = e.digest()
	e.CSig = hex.EncodeToString(ed25519.Sign(l.id.priv, []byte(e.Hash)))
	if err := l.check(e); err != nil {
		return err
	}
	line, _ := json.Marshal(e)
	if _, err := l.f.Write(append(line, '\n')); err != nil {
		return err
	}
	if err := l.f.Sync(); err != nil {
		return err
	}
	l.apply(e)
	return nil
}

// vouch records that an account passed a canary: it may now use its overdraft.
func (l *Ledger) vouch(to, job string) error {
	return l.add(entry{Type: "vouch", To: to, Job: job})
}

func (l *Ledger) isVouched(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.vouched[key]
}

func (l *Ledger) pay(from, to string, amount, max int64, model, job string, auth []byte, shares []Share) error {
	return l.add(entry{Type: "pay", From: from, To: to, Amount: amount, Max: max, Model: model, Job: job,
		Sig: hex.EncodeToString(auth), Shares: shares})
}

// account returns the totals of an account and what it can still spend.
func (l *Ledger) account(key string) (a acct, spendable int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if p := l.accts[key]; p != nil {
		a = *p
	}
	return a, l.spendable(key)
}

func (l *Ledger) balance(key string) int64 {
	a, _ := l.account(key)
	return a.Balance
}

func (l *Ledger) summary() (seq uint64, owed int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seq, l.owed
}

// serve writes the whole ledger, one JSON entry per line.
func (l *Ledger) serve(w io.Writer) error {
	l.mu.Lock()
	size, err := l.f.Seek(0, io.SeekCurrent) // entries past this point may still be half written
	l.mu.Unlock()
	if err != nil {
		return err
	}
	f, err := os.Open(l.path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, io.NewSectionReader(f, 0, size))
	return err
}

// AuditResult is what a full verification of a ledger found.
type AuditResult struct {
	Entries     uint64 `json:"entries"`
	Head        string `json:"head"`
	Accounts    int    `json:"accounts"`
	Circulating int64  `json:"circulating"`
}

// Checkpoint is the last ledger position a node verified. The next audit must find
// the same entry at the same place: a coordinator cannot quietly rewrite the past.
type Checkpoint struct {
	Coordinator string `json:"coordinator"`
	Key         string `json:"key"`
	Seq         uint64 `json:"seq"`
	Hash        string `json:"hash"`
}

var errIncomplete = errors.New("the ledger download was incomplete")

// auditChain verifies every entry of a ledger against the rules and against an earlier checkpoint.
// announced is the entry count the coordinator claims. A ruleError means the coordinator cheated.
// shortcut: it reads the ledger from the first entry each time; audit from the checkpoint with a balance snapshot once the ledger passes about 50 000 entries.
func auditChain(r io.Reader, keyHex string, announced uint64, cp Checkpoint) (AuditResult, error) {
	key, err := hex.DecodeString(keyHex)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return AuditResult{}, errors.New("the coordinator did not give a valid ledger key")
	}
	if cp.Key != "" && cp.Key != keyHex {
		return AuditResult{}, broken("the coordinator changed its ledger key")
	}
	c := newChain(key)
	_, err = c.readChain(r, func(e entry) error {
		if e.Seq == cp.Seq && e.Hash != cp.Hash {
			return broken("entry %d is not the one verified before: history was rewritten", e.Seq)
		}
		return nil
	})
	switch {
	case err != nil:
		return AuditResult{}, err
	case announced < cp.Seq:
		return AuditResult{}, broken("the ledger has %d entries, %d were verified before: history was removed", announced, cp.Seq)
	case c.seq < announced:
		return AuditResult{}, errIncomplete
	}
	return AuditResult{Entries: c.seq, Head: c.head, Accounts: len(c.accts), Circulating: c.owed}, nil
}
