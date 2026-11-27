package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	hdrKey  = "X-T6-Key"
	hdrTime = "X-T6-Time"
	hdrSig  = "X-T6-Sig"

	gcmTag           = 16
	maxBody          = 1 << 20
	maxTokensDefault = 1024
	maxTokensCap     = 4096
)

// Model is one entry of the network catalog. Weight is the price multiplier.
type Model struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Weight    int64   `json:"weight"`
	MinVRAMMB int     `json:"min_vram_mb"`
	SizeGB    float64 `json:"size_gb"`
	Workers   int     `json:"workers"`
	Busy      int     `json:"busy"`    // machines of this model that are computing right now
	Waiting   int     `json:"waiting"` // requests waiting for a machine of this model
	// For a model held in blocks by several machines:
	Blocks  int   `json:"blocks,omitempty"`  // how many blocks it has, once known
	Cover   []int `json:"cover,omitempty"`   // how many machines hold each block
	Heads   int   `json:"heads,omitempty"`   // machines that offer to hold its conversations
	Chained bool  `json:"chained,omitempty"` // a chain of machines offers it now
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Payload is what the requester encrypts for the worker. The coordinator never sees it.
type Payload struct {
	Messages    []Message `json:"messages"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature *float64  `json:"temperature,omitempty"`
	TopP        *float64  `json:"top_p,omitempty"`
}

type PollReq struct {
	Models  []string `json:"models"`
	XPub    []byte   `json:"xpub"`
	XSig    []byte   `json:"xsig"` // the account's signature of XPub
	Version string   `json:"version"`
}

type JobMsg struct {
	ID          string `json:"id"`
	Model       string `json:"model"`
	EphPub      []byte `json:"eph_pub"`
	Payload     []byte `json:"payload"`
	MaxOutBytes int64  `json:"max_out_bytes"`
}

// Chunk carries encrypted output text. The last one has Done set and no data.
type Chunk struct {
	Seq              uint64 `json:"seq"`
	Data             []byte `json:"data,omitempty"`
	Done             bool   `json:"done,omitempty"`
	Error            string `json:"error,omitempty"`
	Truncated        bool   `json:"truncated,omitempty"`
	PromptTokens     int    `json:"prompt_tokens,omitempty"`
	CompletionTokens int    `json:"completion_tokens,omitempty"`
	Cost             int64  `json:"cost,omitempty"`
	Balance          int64  `json:"balance,omitempty"`
}

type NewJobReq struct {
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens"`
}

type NewJobResp struct {
	ID         string  `json:"id"`
	WorkerXPub []byte  `json:"worker_xpub"`
	WorkerXSig []byte  `json:"worker_xsig"`      // proves WorkerXPub belongs to the account that is paid
	Worker     string  `json:"worker"`           // the account that will be paid
	Shares     []Share `json:"shares,omitempty"` // when a chain answers: every account that is paid, and its parts
	Weight     int64   `json:"weight"`
	MaxTokens  int     `json:"max_tokens"`
}

// Share is one account's part of a payment. An answer computed by a chain pays the machine that
// holds the conversation and the machines that hold the blocks, each according to its parts.
type Share struct {
	To    string `json:"to"`
	Parts int64  `json:"parts"`
}

type StartReq struct {
	EphPub  []byte `json:"eph_pub"`
	Payload []byte `json:"payload"`
	Max     int64  `json:"max"`  // the most this job may cost
	Auth    []byte `json:"auth"` // the requester's signature of payAuthMessage
}

type Account struct {
	Key     string `json:"key"`
	Balance int64  `json:"balance"`
	Earned  int64  `json:"earned"`
	Spent   int64  `json:"spent"`
	Held    int64  `json:"held"`
	// Spendable is what the account can pay now: its balance, plus its overdraft, minus Held.
	Spendable int64 `json:"spendable"`
	Vouched   bool  `json:"vouched"`
}

type Network struct {
	Models    []Model `json:"models"`
	Workers   int     `json:"workers"`
	Busy      int     `json:"busy"`
	JobsDone  int64   `json:"jobs_done"`
	TokensOut int64   `json:"tokens_out"`
	LedgerKey string  `json:"ledger_key"` // signs every ledger entry
	LedgerSeq uint64  `json:"ledger_seq"`
	// Circulating is the sum of the positive balances. Negative balances mirror it exactly.
	Circulating int64 `json:"circulating"`
	// HoldersPerBlock is how many machines must hold each block before a chain is formed.
	HoldersPerBlock int    `json:"holders_per_block"`
	Relay           string `json:"relay,omitempty"` // host:port that joins machines unable to reach each other
}

type Me struct {
	Account Account `json:"account"`
	Network Network `json:"network"`
}

// price is measured by the coordinator from ciphertext sizes, so a worker cannot inflate it.
// 4 bytes of output or 40 bytes of input cost one credit on a weight-1 model.
func price(weight, inBytes, outBytes int64) int64 {
	return weight * (ceilDiv(outBytes, 4) + ceilDiv(inBytes, 40))
}

func ceilDiv(a, b int64) int64 { return (a + b - 1) / b }

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Identity is an Ed25519 key. The public key is the account: no signup, no email.
type Identity struct{ priv ed25519.PrivateKey }

func (id Identity) Key() string {
	return hex.EncodeToString(id.priv.Public().(ed25519.PublicKey))
}

func loadIdentity(path string) (Identity, error) {
	if raw, err := os.ReadFile(path); err == nil {
		seed, err := hex.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil || len(seed) != ed25519.SeedSize {
			return Identity{}, errors.New("identity file is damaged: " + path)
		}
		return Identity{ed25519.NewKeyFromSeed(seed)}, nil
	}
	seed := make([]byte, ed25519.SeedSize)
	rand.Read(seed)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return Identity{}, err
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(seed)), 0o600); err != nil {
		return Identity{}, err
	}
	return Identity{ed25519.NewKeyFromSeed(seed)}, nil
}

func sigMessage(method, path, ts string, body []byte) []byte {
	sum := sha256.Sum256(body)
	return []byte(method + "\n" + path + "\n" + ts + "\n" + hex.EncodeToString(sum[:]))
}

func (id Identity) sign(r *http.Request, body []byte) {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	r.Header.Set(hdrKey, id.Key())
	r.Header.Set(hdrTime, ts)
	r.Header.Set(hdrSig, hex.EncodeToString(ed25519.Sign(id.priv, sigMessage(r.Method, r.URL.Path, ts, body))))
}

// shortcut: a captured request can be replayed for 5 minutes, TLS is the only guard; add a nonce cache if the coordinator is ever served without TLS.
func verifyRequest(r *http.Request, body []byte) (string, error) {
	pub, err := hex.DecodeString(r.Header.Get(hdrKey))
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return "", errors.New("bad key")
	}
	ts := r.Header.Get(hdrTime)
	t, err := strconv.ParseInt(ts, 10, 64)
	if d := time.Now().Unix() - t; err != nil || d > 300 || d < -300 {
		return "", errors.New("bad timestamp, check your computer clock")
	}
	sig, err := hex.DecodeString(r.Header.Get(hdrSig))
	if err != nil || !ed25519.Verify(pub, sigMessage(r.Method, r.URL.Path, ts, body), sig) {
		return "", errors.New("bad signature")
	}
	return hex.EncodeToString(pub), nil
}

// xkeyMessage is what a machine signs with its account key to claim an encryption key.
// The requester checks it: the only one who can read a job is the account paid for it,
// so the coordinator cannot slip its own key in front of someone else's machine.
func xkeyMessage(xpub []byte) []byte {
	return []byte("tor6ia/xkey\n" + hex.EncodeToString(xpub))
}

// validWorkerKey tells whether a job offer's encryption key is signed by the account it pays.
func validWorkerKey(nj NewJobResp) bool {
	payee, err := hex.DecodeString(nj.Worker)
	return err == nil && len(payee) == ed25519.PublicKeySize && ed25519.Verify(payee, xkeyMessage(nj.WorkerXPub), nj.WorkerXSig)
}

// jobKey derives the AES-GCM key shared by the requester and the worker of one job.
func jobKey(priv *ecdh.PrivateKey, peerPub []byte, jobID string) (cipher.AEAD, error) {
	pub, err := ecdh.X25519().NewPublicKey(peerPub)
	if err != nil {
		return nil, err
	}
	shared, err := priv.ECDH(pub)
	if err != nil {
		return nil, err
	}
	key, err := hkdf.Key(sha256.New, shared, nil, "tor6ia/job/"+jobID, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

const (
	dirRequest  = 0
	dirResponse = 1
)

// The nonce is direction + sequence number, unique per job key.
func nonce(dir byte, seq uint64) []byte {
	n := make([]byte, 12)
	n[0] = dir
	binary.BigEndian.PutUint64(n[4:], seq)
	return n
}

func seal(a cipher.AEAD, dir byte, seq uint64, jobID string, plain []byte) []byte {
	return a.Seal(nil, nonce(dir, seq), plain, []byte(jobID))
}

func open(a cipher.AEAD, dir byte, seq uint64, jobID string, sealed []byte) ([]byte, error) {
	return a.Open(nil, nonce(dir, seq), sealed, []byte(jobID))
}
