// Tor6IA shares compute for AI: your machine runs a model for others and earns credits,
// which you spend to use the models other machines run.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
)

// version is "dev" for a build from source. Releases set it: -ldflags "-X main.version=1.2.3".
// A dev build never offers to update itself.
var version = "dev"

// defaultCoordinator is set at build time: -ldflags "-X main.defaultCoordinator=https://..."
// Empty means solo mode: the node runs its own private coordinator.
var defaultCoordinator = ""

func main() {
	if len(os.Args) == 1 && selfInstall() {
		return // double-clicked download: the installed copy is now running
	}
	cmd, args := "start", os.Args[1:]
	if len(args) > 0 && args[0][0] != '-' {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "start":
		err = runNode(args)
	case "coordinator":
		err = runCoordinator(args)
	case "audit":
		err = runAudit(args)
	case "version":
		fmt.Println("tor6ia", version)
	default:
		fmt.Println("usage: tor6ia [start|coordinator|audit|version] [flags]   (tor6ia start -h, tor6ia coordinator -h)")
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		if runtime.GOOS == "windows" {
			time.Sleep(8 * time.Second) // a double-clicked console closes at exit: leave time to read
		}
		os.Exit(1)
	}
}

func dataDir() (string, error) {
	if d := os.Getenv("T6_HOME"); d != "" {
		return d, nil
	}
	base, err := os.UserConfigDir()
	return filepath.Join(base, "Tor6IA"), err
}

func runNode(args []string) error {
	fs := flag.NewFlagSet("start", flag.ExitOnError)
	coord := fs.String("coordinator", defaultCoordinator, "coordinator URL (empty: solo mode on this machine)")
	model := fs.String("model", "", "model to share (default: the biggest your machine can hold)")
	port := fs.Int("port", 7606, "local port of the interface and of the OpenAI-compatible API")
	ollama := fs.String("ollama", "http://127.0.0.1:11434", "Ollama URL")
	noBrowser := fs.Bool("no-browser", false, "do not open the interface in the browser")
	fs.Parse(args)

	home, err := dataDir()
	if err != nil {
		return err
	}
	id, err := loadIdentity(filepath.Join(home, "identity.key"))
	if err != nil {
		return err
	}
	hostport := "127.0.0.1:" + strconv.Itoa(*port)
	url := "http://" + hostport
	restarted := os.Getenv("T6_RESTART") != "" // we replace a copy of ourselves that is still closing
	if exe, err := os.Executable(); err == nil {
		os.Remove(exe + ".old") // left behind by an update
	}
	ln, err := net.Listen("tcp", hostport)
	for i := 0; err != nil && restarted && i < 40; i++ {
		time.Sleep(250 * time.Millisecond)
		ln, err = net.Listen("tcp", hostport)
	}
	if err != nil {
		if isTor6IA(url) {
			fmt.Println("Tor6IA is already running:", url)
			if !*noBrowser {
				openBrowser(url)
			}
			return nil
		}
		return fmt.Errorf("port %d is used by another program, choose another one with --port", *port)
	}

	ctx := context.Background()
	solo := *coord == ""
	if solo {
		c, err := newCoordinator(filepath.Join(home, "solo"))
		if err != nil {
			return err
		}
		cl, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return err
		}
		go c.Run(ctx)
		go http.Serve(cl, c.Handler())
		*coord = "http://" + cl.Addr().String()
	}

	n := newNode(id, *coord, *ollama, home, solo)
	go n.setup(ctx, *model)
	go n.workLoop(ctx)
	go n.auditLoop(ctx)
	go n.updateLoop(ctx)

	fmt.Println("Tor6IA", version)
	fmt.Println("  interface   ", url)
	fmt.Println("  OpenAI API  ", url+"/v1")
	if solo {
		fmt.Println("  network      solo (this machine only)")
	} else {
		fmt.Println("  network     ", *coord)
	}
	fmt.Println("  account     ", id.Key()[:16]+"…")
	if !*noBrowser && !restarted {
		openBrowser(url)
	}
	return http.Serve(ln, n.Handler(hostport))
}

// runAudit verifies the whole public ledger of a network and prints what it found.
func runAudit(args []string) error {
	fs := flag.NewFlagSet("audit", flag.ExitOnError)
	coord := fs.String("coordinator", defaultCoordinator, "coordinator URL")
	fs.Parse(args)
	if *coord == "" {
		return errors.New("no network to audit: pass --coordinator URL")
	}
	home, err := dataDir()
	if err != nil {
		return err
	}
	id, err := loadIdentity(filepath.Join(home, "identity.key"))
	if err != nil {
		return err
	}
	res, err := newNode(id, *coord, "", home, false).audit(context.Background())
	if err != nil {
		return fmt.Errorf("AUDIT FAILED: %w", err)
	}
	fmt.Printf("ledger verified: %d entries, %d accounts, %d credits owed to members, head %.16s\n",
		res.Entries, res.Accounts, res.Circulating, res.Head)
	return nil
}

func isTor6IA(url string) bool {
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get(url + "/api/status")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var s Status
	return json.NewDecoder(resp.Body).Decode(&s) == nil && s.Key != ""
}

func openBrowser(url string) {
	switch runtime.GOOS {
	case "windows":
		exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		exec.Command("open", url).Start()
	default:
		exec.Command("xdg-open", url).Start()
	}
}

func runCoordinator(args []string) error {
	fs := flag.NewFlagSet("coordinator", flag.ExitOnError)
	addr := fs.String("addr", ":8787", "listen address")
	data := fs.String("data", "", "data directory (default: the Tor6IA config directory)")
	trust := fs.Bool("trust-proxy", false, "read client IPs from X-Forwarded-For (only behind your own reverse proxy)")
	cert := fs.String("tls-cert", "", "TLS certificate file: serve HTTPS directly, without a reverse proxy")
	key := fs.String("tls-key", "", "TLS private key file")
	fs.Parse(args)

	if *data == "" {
		home, err := dataDir()
		if err != nil {
			return err
		}
		*data = filepath.Join(home, "coordinator")
	}
	c, err := newCoordinator(*data)
	if err != nil {
		return err
	}
	c.TrustProxy = *trust
	go c.Run(context.Background())
	log.Printf("Tor6IA coordinator %s on %s, data in %s", version, *addr, *data)
	srv := &http.Server{Addr: *addr, Handler: c.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	if *cert != "" {
		return srv.ListenAndServeTLS(*cert, *key)
	}
	return srv.ListenAndServe()
}
