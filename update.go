package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// updateRepo is the GitHub repository whose releases are offered as updates.
// A fork sets its own: -ldflags "-X main.updateRepo=owner/name".
var updateRepo = "tomDeprez/Tor6IA"

// Update is a release newer than the running program.
type Update struct {
	Version string `json:"version"`
	Notes   string `json:"notes"` // what changed, as written in the release
}

var tagPattern = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)

// newer tells whether release tag "vX.Y.Z" is above version "X.Y.Z".
func newer(tag, current string) bool {
	a, b := tagPattern.FindStringSubmatch(tag), tagPattern.FindStringSubmatch("v"+current)
	if a == nil || b == nil {
		return false
	}
	for i := 1; i <= 3; i++ {
		x, _ := strconv.Atoi(a[i])
		y, _ := strconv.Atoi(b[i])
		if x != y {
			return x > y
		}
	}
	return false
}

// checkUpdate asks GitHub for the latest release. A development build never updates: it returns nil.
func checkUpdate(ctx context.Context) (*Update, error) {
	if version == "dev" {
		return nil, nil
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/repos/"+updateRepo+"/releases/latest", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "tor6ia")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, errors.New("github: " + resp.Status)
	}
	var rel struct {
		Tag  string `json:"tag_name"`
		Body string `json:"body"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return nil, err
	}
	if !newer(rel.Tag, version) {
		return nil, nil
	}
	if len(rel.Body) > 4000 {
		rel.Body = rel.Body[:4000]
	}
	return &Update{Version: strings.TrimPrefix(rel.Tag, "v"), Notes: rel.Body}, nil
}

// updateLoop looks for a new release at startup, then four times a day.
func (n *Node) updateLoop(ctx context.Context) {
	for ctx.Err() == nil {
		u, err := checkUpdate(ctx)
		wait := 6 * time.Hour
		if err != nil {
			wait = 30 * time.Minute
		} else {
			n.mu.Lock()
			n.update = u
			n.mu.Unlock()
		}
		sleep(ctx, wait)
	}
}

// applyUpdate downloads the release binary for this system and puts it in place of the running one.
// The address is built here from the checked version: nothing in GitHub's answer chooses what is downloaded.
// shortcut: the download is trusted because it comes from GitHub over HTTPS; sign the releases and verify here once the project has a signing key.
func applyUpdate(ctx context.Context, ver string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	url := fmt.Sprintf("https://github.com/%s/releases/download/v%s/tor6ia-%s-%s%s", updateRepo, ver, runtime.GOOS, runtime.GOARCH, ext)
	fresh := filepath.Join(filepath.Dir(exe), "tor6ia.new"+ext)
	if err := download(ctx, url, fresh, func(float64) {}); err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer os.Remove(fresh)
	if err := os.Chmod(fresh, 0o755); err != nil {
		return err
	}
	// Run it once before trusting it: a cut download or a wrong file does not answer its version.
	out, err := exec.CommandContext(ctx, fresh, "version").Output()
	if err != nil || strings.TrimSpace(string(out)) != "tor6ia "+ver {
		return fmt.Errorf("the downloaded file is not Tor6IA %s", ver)
	}
	// A running program cannot be overwritten on Windows, but it can be renamed.
	old := exe + ".old"
	os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		return err
	}
	if err := os.Rename(fresh, exe); err != nil {
		os.Rename(old, exe)
		return err
	}
	return nil
}

// restart starts the program again with the same arguments, then stops this one.
// T6_RESTART tells the new one to wait for our port and not to open the browser again.
func restart() {
	exe, err := os.Executable()
	if err != nil {
		log.Printf("restart: %v", err)
		return
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = append(os.Environ(), "T6_RESTART=1")
	ownWindow(cmd)
	if err := cmd.Start(); err != nil {
		log.Printf("restart: %v", err)
		return
	}
	os.Exit(0)
}

func (n *Node) handleUpdate(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	u := n.update
	n.mu.Unlock()
	if u == nil {
		writeJSON(w, 409, map[string]string{"error": "no update available"})
		return
	}
	if err := applyUpdate(r.Context(), u.Version); err != nil {
		log.Printf("update: %v", err)
		writeJSON(w, 502, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]string{"version": u.Version})
	go func() {
		time.Sleep(time.Second) // let the answer reach the browser
		n.leave()
		restart()
	}()
}
