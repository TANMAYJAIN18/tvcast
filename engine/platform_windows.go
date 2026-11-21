package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

const isAndroidBuild = false

var (
	kernel32                    = syscall.NewLazyDLL("kernel32.dll")
	procSetThreadExecutionState = kernel32.NewProc("SetThreadExecutionState")
)

const (
	esSystemRequired = 0x00000001
	esContinuous     = 0x80000000
)

// setAwake resets Windows' idle-sleep timer. It is called every 20 s while streaming,
// so the PC never reaches its idle timeout; when streaming stops, normal sleep resumes.
func setAwake(on bool) {
	if on {
		_, _, _ = procSetThreadExecutionState.Call(uintptr(esSystemRequired))
	}
}

func powercfg(args ...string) (string, error) {
	c := exec.Command("powercfg", args...)
	hideWindow(c)
	out, err := c.CombinedOutput()
	return string(out), err
}

func lidGuardInstalled() bool {
	out, err := powercfg("/q", "SCHEME_CURRENT", "SUB_BUTTONS", "LIDACTION")
	if err != nil {
		return false
	}
	return strings.Contains(out, "Current AC Power Setting Index: 0x00000000")
}

// installLidGuard sets "When I close the lid: Do nothing" (plugged in and on battery)
// for the current power plan, so streaming continues with the lid closed.
func installLidGuard() (bool, string) {
	for _, a := range [][]string{
		{"/setacvalueindex", "SCHEME_CURRENT", "SUB_BUTTONS", "LIDACTION", "0"},
		{"/setdcvalueindex", "SCHEME_CURRENT", "SUB_BUTTONS", "LIDACTION", "0"},
		{"/setactive", "SCHEME_CURRENT"},
	} {
		if out, err := powercfg(a...); err != nil {
			return false, "powercfg failed: " + strings.TrimSpace(out)
		}
	}
	return true, "Closing the lid no longer puts this PC to sleep. Keep it plugged in for long movies."
}

func home() string { h, _ := os.UserHomeDir(); return h }

func defaultFolders() []string {
	h := home()
	return []string{filepath.Join(h, "Videos"), filepath.Join(h, "Downloads"), filepath.Join(h, "Desktop"), filepath.Join(h, "Documents")}
}

func extraVolumeRoots() []string {
	var out []string
	for c := 'D'; c <= 'Z'; c++ {
		root := string(c) + `:\`
		if st, err := os.Stat(root); err == nil && st.IsDir() {
			out = append(out, root)
		}
	}
	return out
}

func skipDirs() map[string]bool {
	h := home()
	return map[string]bool{filepath.Join(h, "AppData"): true, `C:\Windows`: true, `C:\Program Files`: true,
		`C:\Program Files (x86)`: true, `C:\ProgramData`: true}
}

func extraToolDirs() []string { return nil }

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}

func openUI() {
	c := exec.Command("rundll32", "url.dll,FileProtocolHandler", uiURL())
	hideWindow(c)
	_ = c.Start()
}

const psFolder = `Add-Type -AssemblyName System.Windows.Forms; $f = New-Object System.Windows.Forms.FolderBrowserDialog; $f.Description = 'Choose a folder with movies'; $w = New-Object System.Windows.Forms.Form; $w.TopMost = $true; if ($f.ShowDialog($w) -eq 'OK') { $f.SelectedPath }`
const psFile = `Add-Type -AssemblyName System.Windows.Forms; $f = New-Object System.Windows.Forms.OpenFileDialog; $f.Title = 'Choose a movie'; $f.Filter = 'Videos|*.mkv;*.mp4;*.m4v;*.avi;*.mov;*.ts;*.m2ts;*.webm;*.wmv;*.mpg;*.mpeg|All files|*.*'; $w = New-Object System.Windows.Forms.Form; $w.TopMost = $true; if ($f.ShowDialog($w) -eq 'OK') { $f.FileName }`

func pickFolder() (string, bool) { return ps(psFolder) }
func pickFile() (string, bool)   { return ps(psFile) }

func ps(script string) (string, bool) {
	c := exec.Command("powershell", "-NoProfile", "-STA", "-Command", script)
	hideWindow(c)
	out, err := c.Output()
	if err != nil {
		return "", true
	}
	return strings.TrimSpace(string(out)), true
}

// isLaptop is only used for the macOS lid warning.
func isLaptop() bool { return false }
