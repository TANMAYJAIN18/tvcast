package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const lidFlag = "/private/tmp/tvcast-active"
const lidGuardPlist = "/Library/LaunchDaemons/com.tvcast.sleepguard.plist"

var (
	caffMu   sync.Mutex
	caff     *exec.Cmd
	caffDone chan struct{}
)

func caffRunning() bool {
	if caff == nil {
		return false
	}
	select {
	case <-caffDone:
		return false
	default:
		return true
	}
}

// setAwake uses Apple's caffeinate (idle + disk + system sleep) while streaming, and
// touches a flag that the optional lid guard (root helper) reads to allow lid-closed streaming.
func setAwake(on bool) {
	caffMu.Lock()
	defer caffMu.Unlock()
	if on {
		_ = os.WriteFile(lidFlag, []byte(strconv.FormatInt(time.Now().Unix(), 10)), 0o644)
		if !caffRunning() {
			c := exec.Command("/usr/bin/caffeinate", "-i", "-m", "-s", "-w", strconv.Itoa(os.Getpid()))
			if err := c.Start(); err == nil {
				done := make(chan struct{})
				caff, caffDone = c, done
				go func() { _ = c.Wait(); close(done) }()
			} else {
				log.Printf("caffeinate: %v", err)
			}
		}
		return
	}
	_ = os.Remove(lidFlag)
	if caffRunning() {
		_ = caff.Process.Kill()
	}
	caff = nil
}

func lidGuardInstalled() bool {
	_, err := os.Stat(lidGuardPlist)
	return err == nil
}

// installLidGuard asks macOS for the user's admin password (native dialog) and installs
// the small root helper that keeps the Mac awake with the lid closed — only while streaming.
func installLidGuard() (bool, string) {
	script := ""
	exe, _ := os.Executable()
	for _, p := range []string{
		filepath.Join(filepath.Dir(exe), "lidguard-install.sh"),
		filepath.Join(filepath.Dir(exe), "..", "Resources", "lidguard-install.sh"),
	} {
		if _, err := os.Stat(p); err == nil {
			script = p
			break
		}
	}
	if script == "" {
		return false, "lidguard-install.sh not found next to the engine; reinstall TV Cast"
	}
	as := fmt.Sprintf(`do shell script "/bin/sh " & quoted form of %q with administrator privileges with prompt "TV Cast needs your password once so the Mac keeps streaming when the lid is closed."`, script)
	out, err := exec.Command("/usr/bin/osascript", "-e", as).CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "-128") {
			return false, "Cancelled"
		}
		return false, strings.TrimSpace(string(out))
	}
	return true, "Lid-closed streaming is on. Keep the Mac plugged in for long movies."
}

func defaultFolders() []string {
	h, _ := os.UserHomeDir()
	return []string{filepath.Join(h, "Movies"), filepath.Join(h, "Downloads"), filepath.Join(h, "Desktop"), filepath.Join(h, "Documents")}
}

func extraVolumeRoots() []string {
	var out []string
	ents, _ := os.ReadDir("/Volumes")
	for _, e := range ents {
		p := filepath.Join("/Volumes", e.Name())
		if fi, err := os.Lstat(p); err == nil && fi.Mode()&os.ModeSymlink == 0 && fi.IsDir() {
			out = append(out, p)
		}
	}
	return out
}

func skipDirs() map[string]bool {
	h, _ := os.UserHomeDir()
	return map[string]bool{filepath.Join(h, "Library"): true, filepath.Join(h, ".Trash"): true}
}

func extraToolDirs() []string {
	return []string{"/opt/homebrew/bin", "/usr/local/bin", "/opt/local/bin"}
}

func hideWindow(cmd *exec.Cmd) {}

func openUI() {
	// Prefer the native app window; fall back to the browser.
	if err := exec.Command("/usr/bin/open", "-b", "com.tvcast.app").Run(); err != nil {
		_ = exec.Command("/usr/bin/open", uiURL()).Run()
	}
}

func pickFolder() (string, bool) {
	return osascript(`POSIX path of (choose folder with prompt "Choose a folder with movies")`)
}

func pickFile() (string, bool) {
	return osascript(`POSIX path of (choose file with prompt "Choose a movie" of type {"public.movie"})`)
}

func osascript(s string) (string, bool) {
	out, err := exec.Command("/usr/bin/osascript", "-e", s).Output()
	if err != nil {
		return "", true
	}
	p := strings.TrimRight(strings.TrimSpace(string(out)), "/")
	if p == "" {
		return "", true
	}
	return p, true
}
