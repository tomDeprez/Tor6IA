package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

// A split runs the shared model across several machines instead of this one alone: the model is
// cut into blocks of layers and each machine of the group holds some. The Tor6IA engine does it
// (github.com/tomDeprez/Tor6IA-engine). This machine is the head: it holds the conversation and
// is the one the network sees and pays.
//
// The machines of a group must trust each other: a block server has no login and no encryption.
// That is why the user names them himself, and why the network cannot yet form groups of strangers.

// engineRelease is the engine version this program drives, with the checksum of each build.
const engineRelease = "v0.2.4"

var engineBuilds = map[string]string{
	"windows/amd64": "3e75239d3692837cd827eef9cd98de950c3d92e9a594ef9514fcb0ed7352d357",
	"linux/amd64":   "8c7f978e55882f8e53dbef8b59125f69815058c75df6dd46079079f3cc05a2de",
	"linux/arm64":   "fc264d7cb1c140f007f7ea77520ed318197ad1f8a797895ee81ae230a9fbfb43",
	"darwin/amd64":  "28c60e2f7c2acd7c5d7cddf099ff2d58b961dff9d4909aecb6e359d0b394c985",
	"darwin/arm64":  "6900c118e44d4f48b5378edd216776df41702e17f3fbe2678ec8eb8c6186a0f0",
}

type SplitBlock struct {
	Machine string `json:"machine"`
	First   int    `json:"first"`
	Last    int    `json:"last"`
	Share   int    `json:"share"`  // percent of the model's layers
	Blocks  string `json:"blocks"` // the blocks it announced, like 4-9, when it announced some
	Route   string `json:"route"`  // how it is reached: its address, or relay:host:port
}

// SplitStatus is what the interface shows of the group.
type SplitStatus struct {
	Model    string       `json:"model"`
	State    string       `json:"state"` // starting, ready, error
	Error    string       `json:"error,omitempty"`
	Machines []string     `json:"machines"` // block servers the user named
	Blocks   []SplitBlock `json:"blocks"`   // who holds which layers, once loaded
	Of       int          `json:"of"`       // how many blocks the model has, when machines announced theirs
}

type splitConfig struct {
	Model    string   `json:"model"`
	Machines []string `json:"machines"`
}

func (n *Node) splitFile() string { return filepath.Join(n.home, "split.json") }

// ensureEngine returns the engine program, downloading the pinned release the first time.
// T6_ENGINE points to another copy, for people who build the engine themselves.
func ensureEngine(ctx context.Context, home string) (string, error) {
	if p := engineHere(home); p != "" {
		return p, nil
	}
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	target := runtime.GOOS + "/" + runtime.GOARCH
	want := engineBuilds[target]
	if want == "" {
		return "", fmt.Errorf("the Tor6IA engine has no build for %s", target)
	}
	path := filepath.Join(home, "engine", engineRelease, "tor6ia-engine"+ext)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	url := fmt.Sprintf("https://github.com/tomDeprez/Tor6IA-engine/releases/download/%s/tor6ia-engine-%s-%s%s", engineRelease, runtime.GOOS, runtime.GOARCH, ext)
	fresh := path + ".download"
	if err := download(ctx, url, fresh, func(float64) {}); err != nil {
		return "", fmt.Errorf("engine download failed: %w", err)
	}
	raw, err := os.ReadFile(fresh)
	if err != nil {
		return "", err
	}
	if sum := sha256.Sum256(raw); hex.EncodeToString(sum[:]) != want {
		os.Remove(fresh)
		return "", errors.New("the downloaded engine does not have the expected checksum: it was refused")
	}
	if err := os.Chmod(fresh, 0o755); err != nil {
		return "", err
	}
	return path, os.Rename(fresh, path)
}

// engineHere returns the engine program when this machine already has it, "" otherwise.
func engineHere(home string) string {
	if p := os.Getenv("T6_ENGINE"); p != "" {
		return p
	}
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	path := filepath.Join(home, "engine", engineRelease, "tor6ia-engine"+ext)
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}

// engineClean makes the engine remove what it keeps on this machine: the blocks it downloaded,
// and with all, llama.cpp and everything else. A machine that never used the engine has nothing.
func (n *Node) engineClean(all bool) {
	engine := engineHere(n.home)
	if engine == "" {
		return
	}
	args := []string{"clean"}
	if all {
		args = append(args, "--all")
	}
	if out, err := exec.Command(engine, args...).CombinedOutput(); err != nil {
		log.Printf("engine clean: %v: %s", err, lastLine(string(out)))
	}
}

// startSplit loads the model across the group and makes it the model this machine shares.
func (n *Node) startSplit(cfg splitConfig) {
	fail := func(err error) {
		log.Printf("split: %v", err)
		n.mu.Lock()
		n.split = &SplitStatus{Model: cfg.Model, State: "error", Error: err.Error(), Machines: cfg.Machines}
		n.mu.Unlock()
	}
	n.mu.Lock()
	n.split = &SplitStatus{Model: cfg.Model, State: "starting", Machines: cfg.Machines}
	n.mu.Unlock()

	ctx := context.Background()
	// The head reads the model file Ollama keeps: have Ollama download it when it is missing.
	if !hasModel(ctx, n.ollama, cfg.Model) {
		progress := func(pct float64) {
			n.mu.Lock()
			n.pull = &PullStatus{Model: cfg.Model, Pct: pct}
			n.mu.Unlock()
		}
		progress(0)
		err := pullModel(ctx, n.ollama, cfg.Model, progress)
		n.mu.Lock()
		n.pull = nil
		n.mu.Unlock()
		if err != nil {
			fail(fmt.Errorf("model download failed: %w", err))
			return
		}
	}
	engine, err := ensureEngine(ctx, n.home)
	if err != nil {
		fail(err)
		return
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fail(err)
		return
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()

	// --exit-with-stdin: the engine and the model stop if this program dies, nothing is left running.
	cmd := exec.Command(engine, "head", "--model", cfg.Model, "--blocks", strings.Join(cfg.Machines, ","),
		"--port", strconv.Itoa(port), "--json", "--exit-with-stdin")
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	var said bytes.Buffer
	cmd.Stderr = &said
	if err := cmd.Start(); err != nil {
		fail(fmt.Errorf("the engine did not start: %w", err))
		return
	}
	var ready struct {
		URL    string       `json:"url"`
		Blocks []SplitBlock `json:"blocks"`
		Of     int          `json:"model_blocks"`
	}
	if err := json.NewDecoder(stdout).Decode(&ready); err != nil || ready.URL == "" {
		stdin.Close()
		cmd.Wait()
		lines := strings.Split(strings.TrimSpace(said.String()), "\n")
		fail(errors.New("the model could not be loaded across the group: " + lines[len(lines)-1]))
		return
	}
	go io.Copy(io.Discard, stdout)

	n.mu.Lock()
	n.split = &SplitStatus{Model: cfg.Model, State: "ready", Machines: cfg.Machines, Blocks: ready.Blocks, Of: ready.Of}
	n.engineURL = ready.URL
	n.stopEngine = func() { stdin.Close() }
	n.models = []string{cfg.Model}
	n.repollLocked()
	n.mu.Unlock()
	n.saveModels()
	log.Printf("sharing %s split in %d blocks", cfg.Model, len(ready.Blocks))

	cmd.Wait()
	n.mu.Lock()
	if n.engineURL != ready.URL {
		n.mu.Unlock()
		return // stopped on purpose
	}
	// The engine stopped by itself: a machine of the group left. Without it this machine may not
	// have the memory for the whole model, and then it must stop offering it.
	n.engineURL, n.stopEngine = "", nil
	n.split = &SplitStatus{Model: cfg.Model, State: "error", Machines: cfg.Machines,
		Error: "a machine of the group left: the model is no longer split"}
	alone := slices.ContainsFunc(n.catalog, func(m Model) bool { return m.ID == cfg.Model && n.capacity(m) != "no" })
	if !alone {
		n.models = nil
	}
	n.repollLocked()
	n.mu.Unlock()
	if !alone {
		n.saveModels()
		n.leave()
	}
}

// stopSplit ends the group. The model stays shared when this machine can hold it alone.
func (n *Node) stopSplit() {
	n.mu.Lock()
	if n.split == nil {
		n.mu.Unlock()
		return
	}
	model := n.split.Model
	if n.stopEngine != nil {
		n.stopEngine()
	}
	n.engineURL, n.stopEngine, n.split = "", nil, nil
	alone := slices.ContainsFunc(n.catalog, func(m Model) bool { return m.ID == model && n.capacity(m) != "no" })
	left := false
	if !alone && slices.Contains(n.models, model) {
		n.models = nil
		left = true
	}
	n.repollLocked()
	n.mu.Unlock()
	os.Remove(n.splitFile())
	if left {
		n.saveModels()
		n.leave()
	}
}

func (n *Node) handleSplit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		On       bool     `json:"on"`
		Model    string   `json:"model"`
		Machines []string `json:"machines"`
	}
	refuse := func(status int, msg string) { writeJSON(w, status, map[string]string{"error": msg}) }
	if json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&req) != nil {
		refuse(400, "bad request")
		return
	}
	if !req.On {
		n.stopSplit()
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	n.mu.Lock()
	ready := n.phase == "ready"
	heading := n.head != nil
	running := n.split != nil && n.split.State != "error"
	known := slices.ContainsFunc(n.catalog, func(m Model) bool { return m.ID == req.Model })
	n.mu.Unlock()
	switch {
	case !ready:
		refuse(409, "the machine is not ready yet")
		return
	case heading:
		refuse(409, "this machine holds conversations for the network: stop that first")
		return
	case running:
		refuse(409, "a model is already split: stop it first")
		return
	case !known:
		refuse(400, "this network does not offer that model")
		return
	case len(req.Machines) < 1 || len(req.Machines) > 16:
		refuse(400, "give between 1 and 16 machines")
		return
	}
	for i, m := range req.Machines {
		m = strings.TrimSpace(m)
		// The line a machine shows: its address, the blocks it holds, the secret of its link.
		// 192.168.1.20:50052=4-9#5f1c… The secret is never repeated in an answer.
		addr := m
		if i := strings.IndexAny(m, "=#"); i >= 0 {
			addr = m[:i]
		}
		if host, port, err := net.SplitHostPort(addr); err != nil || host == "" || port == "" || !strings.Contains(m, "#") {
			refuse(400, "not the line of a machine (copy the whole line it shows, like 192.168.1.20:50052=4-9#…): "+addr)
			return
		}
		req.Machines[i] = m
	}
	cfg := splitConfig{Model: req.Model, Machines: req.Machines}
	raw, _ := json.Marshal(cfg)
	if err := os.WriteFile(n.splitFile(), raw, 0o600); err != nil {
		refuse(500, err.Error())
		return
	}
	go n.startSplit(cfg)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// runModel computes one answer with whatever runs the model: the group when it is split, Ollama otherwise.
func (n *Node) runModel(ctx context.Context, model string, pl Payload, onDelta func(string) error) (chatResult, error) {
	n.mu.Lock()
	url := ""
	if n.split != nil && n.split.Model == model {
		url = n.engineURL
	}
	n.mu.Unlock()
	if url != "" {
		return engineChat(ctx, url, pl, onDelta)
	}
	return ollamaChat(ctx, n.ollama, model, pl, onDelta)
}

// engineChat streams one answer from the engine's OpenAI-compatible API.
func engineChat(ctx context.Context, url string, pl Payload, onDelta func(string) error) (chatResult, error) {
	body := map[string]any{"messages": pl.Messages, "stream": true, "stream_options": map[string]bool{"include_usage": true},
		// Same rule as with Ollama: no thinking. Asked here too, so that it does not depend on how the engine was started.
		"chat_template_kwargs": map[string]bool{"enable_thinking": false}}
	if pl.MaxTokens > 0 {
		body["max_tokens"] = pl.MaxTokens
	}
	if pl.Temperature != nil {
		body["temperature"] = *pl.Temperature
	}
	if pl.TopP != nil {
		body["top_p"] = *pl.TopP
	}
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, "POST", url+"/v1/chat/completions", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return chatResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return chatResult{}, fmt.Errorf("engine: %s %s", resp.Status, bytes.TrimSpace(msg))
	}
	var res chatResult
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		var ev struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return res, fmt.Errorf("engine stream: %w", err)
		}
		if ev.Error != nil {
			return res, errors.New("engine: " + ev.Error.Message)
		}
		if ev.Usage != nil {
			res.PromptTokens, res.CompletionTokens = ev.Usage.PromptTokens, ev.Usage.CompletionTokens
		}
		for _, c := range ev.Choices {
			if c.FinishReason == "length" {
				res.Truncated = true
			}
			if c.Delta.Content != "" {
				if err := onDelta(c.Delta.Content); err != nil {
					return res, err
				}
			}
		}
	}
	return res, sc.Err()
}
