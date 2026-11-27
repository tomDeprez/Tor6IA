//go:build windows

package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// totalRAMMB returns the installed memory, 0 when unknown.
func totalRAMMB() int {
	var m struct {
		Length, MemoryLoad                                                                            uint32
		TotalPhys, AvailPhys, TotalPageFile, AvailPageFile, TotalVirtual, AvailVirtual, AvailExtended uint64
	}
	m.Length = uint32(unsafe.Sizeof(m))
	ok, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx").Call(uintptr(unsafe.Pointer(&m)))
	if ok == 0 {
		return 0
	}
	return int(m.TotalPhys >> 20)
}

// detach lets the child outlive us, without a console window.
func detach(cmd *exec.Cmd) {
	const detachedProcess, newProcessGroup = 0x00000008, 0x00000200
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: detachedProcess | newProcessGroup}
}

// selfInstall makes the downloaded exe its own installer. Double-clicked from anywhere, it copies
// itself to %LOCALAPPDATA%\Tor6IA, adds a Start menu shortcut and starts from there.
// It returns true when the installed copy took over.
func selfInstall() bool {
	exe, err := os.Executable()
	local := os.Getenv("LOCALAPPDATA")
	dir := filepath.Join(local, "Tor6IA")
	if err != nil || local == "" || strings.EqualFold(filepath.Dir(exe), dir) {
		return false
	}
	target := filepath.Join(dir, "tor6ia.exe")
	if os.MkdirAll(dir, 0o755) != nil {
		return false
	}
	// An older version may be running from the install folder: stop it, or its file cannot be replaced.
	exec.Command("taskkill", "/F", "/IM", "tor6ia.exe", "/FI", "PID ne "+strconv.Itoa(os.Getpid())).Run()
	time.Sleep(500 * time.Millisecond)
	if copyFile(exe, target) != nil {
		return false // cannot install here: run from where we are
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	menu := filepath.Join(os.Getenv("APPDATA"), `Microsoft\Windows\Start Menu\Programs\Tor6IA.lnk`)
	script := "$s=(New-Object -ComObject WScript.Shell).CreateShortcut(" + quote(menu) + ");$s.TargetPath=" +
		quote(target) + ";$s.WorkingDirectory=" + quote(dir) + ";$s.Save()"
	exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).Run() // no shortcut is not worth failing for

	const newConsole = 0x00000010
	cmd := exec.Command(target)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: newConsole}
	return cmd.Start() == nil
}

func copyFile(from, to string) error {
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.Create(to)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return err
	}
	return dst.Close()
}
