package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const ollamaInstallerURL = "https://ollama.com/download/OllamaSetup.exe"

func ollamaUp(ctx context.Context, base string) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/api/version", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

func findOllama() string {
	if p, err := exec.LookPath("ollama"); err == nil {
		return p
	}
	if runtime.GOOS == "windows" {
		p := filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Ollama", "ollama.exe")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// ensureOllama makes the local Ollama API answer: it installs Ollama on Windows and starts it when needed.
func ensureOllama(ctx context.Context, base string, installing func(pct float64)) error {
	if ollamaUp(ctx, base) {
		return nil
	}
	exe := findOllama()
	if exe == "" {
		if runtime.GOOS != "windows" {
			return errors.New("Ollama is not installed. Install it from https://ollama.com/download then start Tor6IA again")
		}
		setup := filepath.Join(os.TempDir(), "OllamaSetup.exe")
		if err := download(ctx, ollamaInstallerURL, setup, installing); err != nil {
			return fmt.Errorf("Ollama download failed: %w", err)
		}
		defer os.Remove(setup)
		if err := exec.CommandContext(ctx, setup, "/VERYSILENT", "/NORESTART", "/SUPPRESSMSGBOXES").Run(); err != nil {
			return fmt.Errorf("Ollama install failed: %w", err)
		}
		if exe = findOllama(); exe == "" {
			return errors.New("Ollama was installed but cannot be found")
		}
	}
	// The installer usually starts Ollama by itself: give it a moment before starting our own.
	for i := 0; i < 5 && !ollamaUp(ctx, base); i++ {
		time.Sleep(time.Second)
	}
	if !ollamaUp(ctx, base) {
		cmd := exec.Command(exe, "serve")
		detach(cmd)
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("cannot start Ollama: %w", err)
		}
	}
	for i := 0; i < 60; i++ {
		if ollamaUp(ctx, base) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return errors.New("Ollama does not answer on " + base)
}

func download(ctx context.Context, url, dest string, progress func(pct float64)) error {
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return errors.New(resp.Status)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	var done int64
	buf := make([]byte, 1<<20)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := f.Write(buf[:n]); err != nil {
				f.Close()
				return err
			}
			done += int64(n)
			if resp.ContentLength > 0 {
				progress(100 * float64(done) / float64(resp.ContentLength))
			}
		}
		if rerr == io.EOF {
			return f.Close()
		}
		if rerr != nil {
			f.Close()
			return rerr
		}
	}
}

func hasModel(ctx context.Context, base, model string) bool {
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/api/tags", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	json.NewDecoder(resp.Body).Decode(&tags)
	for _, m := range tags.Models {
		if m.Name == model {
			return true
		}
	}
	return false
}

func pullModel(ctx context.Context, base, model string, progress func(pct float64)) error {
	body, _ := json.Marshal(map[string]any{"model": model, "stream": true})
	req, _ := http.NewRequestWithContext(ctx, "POST", base+"/api/pull", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	for {
		var m struct {
			Status    string `json:"status"`
			Error     string `json:"error"`
			Total     int64  `json:"total"`
			Completed int64  `json:"completed"`
		}
		if err := dec.Decode(&m); err != nil {
			return fmt.Errorf("model download interrupted: %w", err)
		}
		if m.Error != "" {
			return errors.New(m.Error)
		}
		// Small layers report 100% at once: only the big one moves the bar.
		if m.Total > 100<<20 {
			progress(100 * float64(m.Completed) / float64(m.Total))
		}
		if m.Status == "success" {
			return nil
		}
	}
}

// deleteModel removes a model's files from the disk.
func deleteModel(ctx context.Context, base, model string) error {
	body, _ := json.Marshal(map[string]string{"model": model})
	req, _ := http.NewRequestWithContext(ctx, "DELETE", base+"/api/delete", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 404 { // 404: already gone
		return errors.New("ollama: " + resp.Status)
	}
	return nil
}

type chatResult struct {
	PromptTokens, CompletionTokens int
	Truncated                      bool
}

// ollamaChat streams one chat completion. Only role and text reach the model: no tools, no files.
func ollamaChat(ctx context.Context, base, model string, pl Payload, onDelta func(string) error) (chatResult, error) {
	// Half of the processors: the machine stays usable while it computes. The same value for
	// every request, because Ollama reloads the model when it changes.
	opts := map[string]any{"num_thread": max(1, runtime.NumCPU()/2)}
	if pl.MaxTokens > 0 {
		opts["num_predict"] = pl.MaxTokens
	}
	if pl.Temperature != nil {
		opts["temperature"] = *pl.Temperature
	}
	if pl.TopP != nil {
		opts["top_p"] = *pl.TopP
	}
	body, _ := json.Marshal(map[string]any{
		"model": model, "messages": pl.Messages, "stream": true, "options": opts, "keep_alive": "30m",
		// Reasoning models think before they answer. That text is not sent to the requester, so it is
		// work nobody is paid for, and a short answer budget can be spent on it entirely.
		"think": false})
	req, _ := http.NewRequestWithContext(ctx, "POST", base+"/api/chat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return chatResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return chatResult{}, fmt.Errorf("ollama: %s %s", resp.Status, bytes.TrimSpace(msg))
	}
	dec := json.NewDecoder(resp.Body)
	for {
		var m struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			Done       bool   `json:"done"`
			DoneReason string `json:"done_reason"`
			Error      string `json:"error"`
			PromptEval int    `json:"prompt_eval_count"`
			Eval       int    `json:"eval_count"`
		}
		if err := dec.Decode(&m); err != nil {
			return chatResult{}, fmt.Errorf("ollama stream: %w", err)
		}
		if m.Error != "" {
			return chatResult{}, errors.New("ollama: " + m.Error)
		}
		if m.Message.Content != "" {
			if err := onDelta(m.Message.Content); err != nil {
				return chatResult{}, err
			}
		}
		if m.Done {
			return chatResult{m.PromptEval, m.Eval, m.DoneReason == "length"}, nil
		}
	}
}

// detectVRAMMB returns the memory a model can use on a GPU, 0 when unknown (CPU only).
// shortcut: only NVIDIA and Apple Silicon are detected, AMD and Intel GPUs get the CPU model; pass --model to override.
func detectVRAMMB() int {
	if out, err := exec.Command("nvidia-smi", "--query-gpu=memory.total", "--format=csv,noheader,nounits").Output(); err == nil {
		best := 0
		for _, line := range strings.Fields(string(out)) {
			if v, err := strconv.Atoi(line); err == nil && v > best {
				best = v
			}
		}
		return best
	}
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		if out, err := exec.Command("sysctl", "-n", "hw.memsize").Output(); err == nil {
			if v, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64); err == nil {
				return int(v >> 20 * 2 / 3) // unified memory: leave a third to the system
			}
		}
	}
	return 0
}
