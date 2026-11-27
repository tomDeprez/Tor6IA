package main

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSharedPayment(t *testing.T) {
	dir := t.TempDir()
	c, err := newCoordinator(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer c.ledger.f.Close()
	account := func(name string) Identity {
		id, err := loadIdentity(filepath.Join(dir, name+".key"))
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	payer, head, a, b := account("payer"), account("head"), account("a"), account("b")
	if err := c.ledger.vouch(payer.Key(), "canary"); err != nil {
		t.Fatal(err)
	}

	// A chain of 6 blocks: a holds 1 to 2, b holds 3 to 6, head holds the conversation.
	const model = "gemma4:12b"
	c.chains[model] = &group{head: head.Key(), links: []link{{a.Key(), "", 1, 2}, {b.Key(), "", 3, 6}}}
	shares := c.sharesLocked(model, head.Key())
	if got := fmt.Sprint(shares); got != fmt.Sprintf("[{%s 12} {%s 16} {%s 32}]", head.Key(), a.Key(), b.Key()) {
		t.Fatalf("shares: %s", got)
	}
	if c.sharesLocked(model, a.Key()) != nil || c.sharesLocked("llama3.2:3b", head.Key()) != nil {
		t.Fatal("a machine that answers alone must be paid alone")
	}

	// The payer signs who is paid. 55 credits: 2 parts in 10 to the head, the rest by blocks.
	sign := func(job string, max int64, shares []Share) []byte {
		return ed25519.Sign(payer.priv, payAuthMessage(job, head.Key(), model, max, shares))
	}
	if err := c.ledger.pay(payer.Key(), head.Key(), 55, 60, model, "job1", sign("job1", 60, shares), shares); err != nil {
		t.Fatal(err)
	}
	// a: 55*16/60 = 14, b: 55*32/60 = 29, head: 55*12/60 = 11, and the credit the division leaves.
	for who, want := range map[string]int64{payer.Key(): -55, head.Key(): 12, a.Key(): 14, b.Key(): 29} {
		if got := c.ledger.balance(who); got != want {
			t.Fatalf("balance of %.8s: %d, want %d", who, got, want)
		}
	}

	// The coordinator cannot change the shares the payer signed, nor add itself.
	var rule ruleError
	stolen := append([]Share{}, shares...)
	stolen[1] = Share{To: c.ledger.id.Key(), Parts: 16}
	if err := c.ledger.pay(payer.Key(), head.Key(), 10, 60, model, "job2", sign("job2", 60, shares), stolen); !errors.As(err, &rule) {
		t.Fatalf("shares changed after the signature: %v", err)
	}
	// Shares the rules forbid are refused even when signed: the same account twice, or without the one that answered.
	twice := []Share{{head.Key(), 1}, {a.Key(), 1}, {a.Key(), 1}}
	if err := c.ledger.pay(payer.Key(), head.Key(), 10, 60, model, "job3", sign("job3", 60, twice), twice); !errors.As(err, &rule) {
		t.Fatalf("an account twice in the shares: %v", err)
	}
	others := []Share{{a.Key(), 1}, {b.Key(), 1}}
	if err := c.ledger.pay(payer.Key(), head.Key(), 10, 60, model, "job4", sign("job4", 60, others), others); !errors.As(err, &rule) {
		t.Fatalf("shares without the machine that answered: %v", err)
	}

	// Any node verifies the ledger with its shared payment, and catches a part that was changed.
	raw, err := os.ReadFile(filepath.Join(dir, "chain.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := auditChain(bytes.NewReader(raw), c.ledger.id.Key(), 2, Checkpoint{})
	if err != nil || res.Entries != 2 || res.Circulating != 55 {
		t.Fatalf("audit: %+v %v", res, err)
	}
	changed := strings.Replace(string(raw), `"parts":16`, `"parts":17`, 1)
	if changed == string(raw) {
		t.Fatal("the ledger does not hold the shares")
	}
	if _, err := auditChain(strings.NewReader(changed), c.ledger.id.Key(), 2, Checkpoint{}); !errors.As(err, &rule) {
		t.Fatalf("a changed part: audit said %v, want a rule violation", err)
	}
}
