//go:build !windows

package main

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// totalRAMMB returns the installed memory, 0 when unknown.
func totalRAMMB() int {
	if raw, err := os.ReadFile("/proc/meminfo"); err == nil { // Linux: "MemTotal:  16303428 kB"
		if f := strings.Fields(string(raw)); len(f) > 1 {
			kb, _ := strconv.Atoi(f[1])
			return kb >> 10
		}
	}
	if out, err := exec.Command("sysctl", "-n", "hw.memsize").Output(); err == nil { // macOS: bytes
		b, _ := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
		return int(b >> 20)
	}
	return 0
}

// detach lets the child outlive us.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// selfInstall is for Windows, where people double-click a download. Elsewhere install.sh installs.
func selfInstall() bool { return false }

// removeProgram deletes the program installed by install.sh.
func removeProgram() {
	if exe, err := os.Executable(); err == nil && strings.HasSuffix(exe, "/.local/bin/tor6ia") {
		os.Remove(exe)
	}
}

// ownWindow lets a restarted copy live on after we exit.
func ownWindow(cmd *exec.Cmd) { detach(cmd) }
