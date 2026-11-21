package main

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const kodiPkg = "org.xbmc.kodi"

type ProcResult struct {
	Code int
	Out  string
}

func (r ProcResult) OK() bool { return r.Code == 0 }

func exeName(n string) string {
	if runtime.GOOS == "windows" {
		return n + ".exe"
	}
	return n
}

// findTool looks in TV Cast's own folder first, then common install locations, then PATH.
func findTool(name string) string {
	n := exeName(name)
	dirs := []string{filepath.Join(appDir(), "platform-tools")}
	dirs = append(dirs, extraToolDirs()...)
	for _, d := range dirs {
		p := filepath.Join(d, n)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	if p, err := exec.LookPath(n); err == nil {
		return p
	}
	return ""
}

func runCmd(exe string, args []string, timeout time.Duration) ProcResult {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	hideWindow(cmd)
	// adb may fork a long-lived server that inherits our pipes; don't wait for it.
	cmd.WaitDelay = 2 * time.Second
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	out := strings.TrimSpace(buf.String())
	if ctx.Err() == context.DeadlineExceeded {
		return ProcResult{-2, "timed out: " + out}
	}
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ProcResult{ee.ExitCode(), out}
		}
		if out == "" {
			out = err.Error()
		}
		// WaitDelay expiry: the command finished but a child (adb server) kept our pipe open
		if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil {
			return ProcResult{cmd.ProcessState.ExitCode(), out}
		}
		return ProcResult{-1, out}
	}
	return ProcResult{0, out}
}

func adb(args []string, serial string, timeout time.Duration) ProcResult {
	a := findTool("adb")
	if a == "" {
		return ProcResult{-1, "adb is not installed yet (TV Cast downloads it automatically; check your internet)"}
	}
	if serial != "" {
		args = append([]string{"-s", serial}, args...)
	}
	return runCmd(a, args, timeout)
}

func adbStartServer() {
	a := findTool("adb")
	if a == "" {
		return
	}
	cmd := exec.Command(a, "start-server") // no pipes: the daemon must not hold our output
	hideWindow(cmd)
	_ = cmd.Run()
}

func serialOf(ip string) string { return ip + ":5555" }

func shell(ip, command string, timeout time.Duration) ProcResult {
	return adb([]string{"shell", command}, serialOf(ip), timeout)
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func adbState(ip string) string {
	for _, line := range strings.Split(adb([]string{"devices"}, "", 10*time.Second).Out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == serialOf(ip) {
			return f[1]
		}
	}
	return ""
}

var (
	adbStatesMu sync.Mutex
	adbStates   = map[string]string{}
	adbStatesAt time.Time
)

// adbStateCached avoids running "adb devices" for every UI refresh.
func adbStateCached(ip string) string {
	adbStatesMu.Lock()
	defer adbStatesMu.Unlock()
	if time.Since(adbStatesAt) > 3*time.Second {
		m := map[string]string{}
		for _, line := range strings.Split(adb([]string{"devices"}, "", 10*time.Second).Out, "\n") {
			if f := strings.Fields(line); len(f) >= 2 && strings.HasSuffix(f[0], ":5555") {
				m[strings.TrimSuffix(f[0], ":5555")] = f[1]
			}
		}
		adbStates, adbStatesAt = m, time.Now()
	}
	return adbStates[ip]
}

var adbConnectMu sync.Mutex

func ensureADB(ip string) (bool, string) {
	adbConnectMu.Lock()
	defer adbConnectMu.Unlock()
	st := adbState(ip)
	if st == "device" {
		return true, "ok"
	}
	if st != "" {
		adb([]string{"disconnect", serialOf(ip)}, "", 10*time.Second)
	}
	r := adb([]string{"connect", serialOf(ip)}, "", 12*time.Second)
	time.Sleep(800 * time.Millisecond)
	switch adbState(ip) {
	case "device":
		return true, "ok"
	case "unauthorized":
		return false, "Look at the TV: accept “Allow USB debugging?” (tick “Always allow”), then try again"
	}
	if !portOpen(ip, 5555, 1500*time.Millisecond) {
		return false, "Can't reach the TV at " + ip + ". Is it on, on the same Wi-Fi, with ADB debugging turned on?"
	}
	return false, "Could not connect: " + r.Out
}

// ---------------------------------------------------------------- network helpers

func portOpen(ip string, port int, timeout time.Duration) bool {
	c, err := net.DialTimeout("tcp4", net.JoinHostPort(ip, strconv.Itoa(port)), timeout)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// localIPToward returns this computer's address on the network that reaches ip.
func localIPToward(ip string) string {
	c, err := net.Dial("udp4", net.JoinHostPort(ip, "9"))
	if err != nil {
		return ""
	}
	defer c.Close()
	if a, ok := c.LocalAddr().(*net.UDPAddr); ok {
		return a.IP.String()
	}
	return ""
}

// ---------------------------------------------------------------- devices

type DeviceInfo struct {
	IP, Name, Model, Serial, KodiVer string
	MemMB                            int
	Kodi                             bool
	ADB                              bool
	Msg                              string
}

var memRe = regexp.MustCompile(`(\d+)`)

func describe(ip string) DeviceInfo {
	ok, msg := ensureADB(ip)
	if !ok {
		return DeviceInfo{IP: ip, Msg: msg}
	}
	model := shell(ip, "getprop ro.product.model", 10*time.Second).Out
	name := shell(ip, "settings get global device_name", 10*time.Second).Out
	if name == "" || name == "null" || strings.Contains(name, "rror") {
		name = model
	}
	serial := shell(ip, "getprop ro.serialno", 10*time.Second).Out
	mem := 0
	if m := memRe.FindString(shell(ip, "grep MemTotal /proc/meminfo", 10*time.Second).Out); m != "" {
		kb, _ := strconv.Atoi(m)
		mem = kb / 1024
	}
	ver := kodiInstalledVersion(ip)
	return DeviceInfo{IP: ip, Name: name, Model: model, Serial: serial, MemMB: mem, Kodi: ver != "", KodiVer: ver, ADB: true}
}

// remember stores what we learned about a device. If the same TV (by serial) was known
// under another IP address (router gave it a new one), its settings move to the new IP.
func remember(d DeviceInfo) {
	withConfig(func(c *Config) {
		cur := c.Devices[d.IP]
		if cur == nil {
			cur = &Device{}
			if d.Serial != "" {
				for oldIP, od := range c.Devices {
					if oldIP != d.IP && od.Serial == d.Serial {
						cur = od
						delete(c.Devices, oldIP)
						if c.Selected == oldIP {
							c.Selected = d.IP
						}
						log.Printf("device %s moved %s -> %s", d.Name, oldIP, d.IP)
						break
					}
				}
			}
			c.Devices[d.IP] = cur
		}
		if d.Name != "" {
			cur.Name = d.Name
		}
		if d.Model != "" {
			cur.Model = d.Model
		}
		if d.Serial != "" {
			cur.Serial = d.Serial
		}
		if d.MemMB > 0 {
			cur.MemMB = d.MemMB
		}
		if d.ADB {
			cur.Kodi = boolPtr(d.Kodi)
			cur.KodiVer = d.KodiVer
			if !d.Kodi {
				cur.Tuned = false
			}
		}
		if c.Selected == "" {
			c.Selected = d.IP
		}
	})
}

var discoverMu sync.Mutex

// discover scans the local /24 network for Android TVs with ADB debugging (port 5555).
func discover() {
	discoverMu.Lock()
	defer discoverMu.Unlock()
	me := localIPToward("1.1.1.1")
	if me == "" {
		return
	}
	parts := strings.Split(me, ".")
	if len(parts) != 4 {
		return
	}
	prefix := strings.Join(parts[:3], ".")
	var mu sync.Mutex
	var hits []string
	sem := make(chan struct{}, 96)
	var wg sync.WaitGroup
	for i := 1; i < 255; i++ {
		ip := fmt.Sprintf("%s.%d", prefix, i)
		if ip == me {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if portOpen(ip, 5555, 800*time.Millisecond) {
				mu.Lock()
				hits = append(hits, ip)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	for _, ip := range hits {
		d := describe(ip)
		if d.ADB {
			remember(d)
		} else if device(ip).Name == "" && d.Msg != "" {
			// unauthorized/new: remember the IP so it shows up in the list
			withConfig(func(c *Config) {
				if c.Devices[ip] == nil {
					c.Devices[ip] = &Device{Name: ip}
				}
			})
		}
	}
	log.Printf("discover: %d TV(s) answering on %s.0/24", len(hits), prefix)
}

// relocate finds a TV again after its IP address changed.
func relocate(ip string) string {
	serial := device(ip).Serial
	if serial == "" {
		return ""
	}
	discover()
	var found string
	readConfig(func(c *Config) {
		for nip, d := range c.Devices {
			if d.Serial == serial && nip != ip {
				found = nip
			}
		}
	})
	return found
}

type DeviceView struct {
	IP       string `json:"ip"`
	Name     string `json:"name"`
	Model    string `json:"model"`
	Online   bool   `json:"online"`
	Tuned    bool   `json:"tuned"`
	Kodi     *bool  `json:"kodi"`
	KodiVer  string `json:"kodi_ver"`
	Selected bool   `json:"selected"`
	ADB      string `json:"adb"`
}

func deviceList() []DeviceView {
	var out []DeviceView
	readConfig(func(c *Config) {
		for ip, d := range c.Devices {
			out = append(out, DeviceView{IP: ip, Name: firstNonEmpty(d.Name, ip), Model: d.Model, Tuned: d.Tuned,
				Kodi: d.Kodi, KodiVer: d.KodiVer, Selected: ip == c.Selected})
		}
	})
	var wg sync.WaitGroup
	for i := range out {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out[i].Online = portOpen(out[i].IP, 5555, 500*time.Millisecond)
			out[i].ADB = adbStateCached(out[i].IP)
		}(i)
	}
	wg.Wait()
	sortDevices(out)
	if out == nil {
		out = []DeviceView{}
	}
	return out
}

func sortDevices(d []DeviceView) {
	for i := 1; i < len(d); i++ {
		for j := i; j > 0 && d[j].Name < d[j-1].Name; j-- {
			d[j], d[j-1] = d[j-1], d[j]
		}
	}
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

// ---------------------------------------------------------------- adb auto-install

// ensureADBInstalled downloads Google's official platform-tools if adb is missing.
func ensureADBInstalled() {
	if findTool("adb") != "" {
		return
	}
	osName := map[string]string{"darwin": "darwin", "windows": "windows", "linux": "linux"}[runtime.GOOS]
	if osName == "" {
		return
	}
	url := "https://dl.google.com/android/repository/platform-tools-latest-" + osName + ".zip"
	log.Printf("adb not found; downloading %s", url)
	client := http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		log.Printf("adb download failed: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		log.Printf("adb download failed: HTTP %d", resp.StatusCode)
		return
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 200<<20))
	if err != nil {
		log.Printf("adb download failed: %v", err)
		return
	}
	if err := unzipTo(data, appDir()); err != nil {
		log.Printf("adb unzip failed: %v", err)
		return
	}
	log.Printf("adb installed to %s", filepath.Join(appDir(), "platform-tools"))
}

func unzipTo(data []byte, dest string) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	destClean := filepath.Clean(dest) + string(os.PathSeparator)
	for _, f := range zr.File {
		target := filepath.Join(dest, filepath.FromSlash(f.Name))
		if !strings.HasPrefix(filepath.Clean(target)+string(os.PathSeparator), destClean) &&
			!strings.HasPrefix(filepath.Clean(target), destClean) {
			return fmt.Errorf("unsafe path in zip: %s", f.Name)
		}
		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(target, 0o755)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		mode := f.Mode().Perm()
		if mode == 0 {
			mode = 0o644
		}
		if f.Mode()&0o111 != 0 || strings.HasSuffix(f.Name, "adb") {
			mode |= 0o755
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, io.LimitReader(rc, 200<<20))
		rc.Close()
		out.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
