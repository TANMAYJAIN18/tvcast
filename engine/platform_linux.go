package main

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

var (
	inhMu   sync.Mutex
	inh     *exec.Cmd
	inhDone chan struct{}
)

func inhRunning() bool {
	if inh == nil {
		return false
	}
	select {
	case <-inhDone:
		return false
	default:
		return true
	}
}

// setAwake holds a systemd inhibitor lock (idle, sleep and lid switch) while streaming.
func setAwake(on bool) {
	inhMu.Lock()
	defer inhMu.Unlock()
	if on {
		if inhRunning() {
			return
		}
		si, err := exec.LookPath("systemd-inhibit")
		if err != nil {
			return
		}
		for _, what := range []string{"idle:sleep:handle-lid-switch", "idle:sleep"} {
			c := exec.Command(si, "--what="+what, "--who=TV Cast", "--why=Streaming a movie to a TV", "--mode=block", "sleep", "infinity")
			c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if err := c.Start(); err != nil {
				continue
			}
			done := make(chan struct{})
			go func() { _ = c.Wait(); close(done) }()
			inh, inhDone = c, done
			return
		}
		log.Printf("systemd-inhibit unavailable; the computer may sleep while streaming")
		return
	}
	if inhRunning() {
		_ = syscall.Kill(-inh.Process.Pid, syscall.SIGTERM) // whole group: inhibit + sleep
	}
	inh = nil
}

func lidGuardInstalled() bool {
	_, err := exec.LookPath("systemd-inhibit")
	return err == nil
}

func installLidGuard() (bool, string) {
	if lidGuardInstalled() {
		return true, "Already handled automatically: Linux keeps streaming with the lid closed while a movie plays."
	}
	return false, "This Linux has no systemd-inhibit; set your lid-close action to “Do nothing” in power settings."
}

func home() string { h, _ := os.UserHomeDir(); return h }

func defaultFolders() []string {
	h := home()
	return []string{filepath.Join(h, "Videos"), filepath.Join(h, "Movies"), filepath.Join(h, "Downloads"),
		filepath.Join(h, "Desktop"), filepath.Join(h, "Documents")}
}

func extraVolumeRoots() []string {
	user := os.Getenv("USER")
	var out []string
	for _, base := range []string{filepath.Join("/media", user), filepath.Join("/run/media", user), "/mnt"} {
		ents, _ := os.ReadDir(base)
		for _, e := range ents {
			if e.IsDir() {
				out = append(out, filepath.Join(base, e.Name()))
			}
		}
	}
	return out
}

func skipDirs() map[string]bool {
	h := home()
	return map[string]bool{filepath.Join(h, "snap"): true, filepath.Join(h, ".cache"): true}
}

func extraToolDirs() []string { return []string{"/usr/local/bin", "/usr/bin"} }

func hideWindow(cmd *exec.Cmd) {}

func openUI() {
	for _, o := range []string{"xdg-open", "gio", "sensible-browser"} {
		if p, err := exec.LookPath(o); err == nil {
			args := []string{uiURL()}
			if o == "gio" {
				args = []string{"open", uiURL()}
			}
			if exec.Command(p, args...).Start() == nil {
				return
			}
		}
	}
}

func pickFolder() (string, bool) {
	return pick([]string{"zenity", "--file-selection", "--directory", "--title=Choose a folder with movies"},
		[]string{"kdialog", "--getexistingdirectory", home()})
}

func pickFile() (string, bool) {
	return pick([]string{"zenity", "--file-selection", "--title=Choose a movie"},
		[]string{"kdialog", "--getopenfilename", home(), "video/*"})
}

func pick(opts ...[]string) (string, bool) {
	for _, o := range opts {
		p, err := exec.LookPath(o[0])
		if err != nil {
			continue
		}
		out, _ := exec.Command(p, o[1:]...).Output()
		return strings.TrimSpace(string(out)), true
	}
	return "", false // no dialog tool: the UI will ask for a typed path
}
