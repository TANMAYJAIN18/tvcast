package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	kodiVersion  = "21.2"
	kodiCodename = "Omega"
	kodiUserdata = "/sdcard/Android/data/org.xbmc.kodi/files/.kodi/userdata"
)

type kv struct {
	K string
	V any
}

// kodiSettings returns the tuned settings, in the order they must be applied.
func kodiSettings(d Device) (int, []kv) {
	// must be one of Kodi's own choices: ... 128, 192, 256, 384, 512 ...
	cache := 192
	if d.MemMB >= 3000 {
		cache = 384
	} else if d.MemMB >= 1900 {
		cache = 256
	}
	return cache, []kv{
		// remote control for TV Cast (password first: Kodi refuses the web server without one)
		{"services.webserverpassword", d.KodiPass},
		{"services.webserverusername", "kodi"},
		{"services.webserverauthentication", true},
		{"services.webserverport", 8080},
		{"services.webserver", true},
		// big read-ahead buffer = no stutter on 10 GB+ files
		{"filecache.buffermode", 1},
		{"filecache.memorysize", cache},
		{"filecache.readfactor", 1000},
		{"filecache.chunksize", 1048576},
		// smooth 24p films: switch the TV's refresh rate to match the movie
		{"videoplayer.adjustrefreshrate", 2},
		{"videoplayer.usemediacodec", true},
		{"videoplayer.usemediacodecsurface", true},
	}
}

var tuneMu sync.Mutex

// tuneKodi applies the streaming settings. Works on Android TV (edits Kodi's settings
// file) and Fire OS 8+ (which blocks that file, so we use Kodi's local control port).
func tuneKodi(ip string) (bool, string) {
	tuneMu.Lock()
	defer tuneMu.Unlock()
	if ok, msg := ensureADB(ip); !ok {
		return false, msg
	}
	withConfig(func(c *Config) {
		d := c.Devices[ip]
		if d == nil {
			d = &Device{}
			c.Devices[ip] = d
		}
		if d.KodiPass == "" {
			d.KodiPass = randomToken(10)
		}
	})
	if device(ip).MemMB == 0 {
		remember(describe(ip))
	}
	dev := device(ip)
	cache, settings := kodiSettings(dev)

	gs := kodiUserdata + "/guisettings.xml"
	if r := shell(ip, "ls "+gs, 10*time.Second); !r.OK() && !strings.Contains(r.Out, "ermission") {
		// Kodi never started: start it once so it creates its settings
		shell(ip, "monkey -p "+kodiPkg+" -c android.intent.category.LAUNCHER 1", 15*time.Second)
		for i := 0; i < 30; i++ {
			time.Sleep(2 * time.Second)
			if shell(ip, "ls "+gs, 10*time.Second).OK() {
				break
			}
		}
		time.Sleep(3 * time.Second)
	}
	shell(ip, "am force-stop "+kodiPkg, 10*time.Second)
	time.Sleep(time.Second)

	tmp, err := os.MkdirTemp("", "tvcast")
	if err != nil {
		return false, err.Error()
	}
	defer os.RemoveAll(tmp)
	local := filepath.Join(tmp, "guisettings.xml")
	if r := adb([]string{"pull", gs, local}, serialOf(ip), 30*time.Second); r.OK() {
		if err := tuneXML(ip, local, gs, settings); err != nil {
			return false, err.Error()
		}
	} else {
		if err := tuneRPC(ip, settings); err != nil {
			return false, "Could not change Kodi settings: " + err.Error()
		}
	}
	withConfig(func(c *Config) {
		if d := c.Devices[ip]; d != nil {
			d.Tuned = true
			d.CacheMB = cache
			d.Kodi = boolPtr(true)
		}
	})
	shell(ip, "monkey -p "+kodiPkg+" -c android.intent.category.LAUNCHER 1", 15*time.Second)
	return true, fmt.Sprintf("Kodi optimized: %d MB buffer, smooth 24p, remote control on", cache)
}

func tuneXML(ip, local, remote string, settings []kv) error {
	b, err := os.ReadFile(local)
	if err != nil {
		return err
	}
	backup := filepath.Join(appDir(), "guisettings-backup-"+ip+".xml")
	if _, err := os.Stat(backup); os.IsNotExist(err) {
		_ = os.WriteFile(backup, b, 0o600)
	}
	xml := string(b)
	for _, s := range settings {
		val := fmt.Sprint(s.V)
		xml = setXMLSetting(xml, s.K, val)
	}
	if err := os.WriteFile(local, []byte(xml), 0o600); err != nil {
		return err
	}
	if r := adb([]string{"push", local, remote}, serialOf(ip), 30*time.Second); !r.OK() {
		return errors.New("could not write Kodi settings: " + r.Out)
	}
	return nil
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xmlEscapeTo(&b, s)
	return b.String()
}

func xmlEscapeTo(w io.Writer, s string) error {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	_, err := io.WriteString(w, r.Replace(s))
	return err
}

func setXMLSetting(xml, id, val string) string {
	re := regexp.MustCompile(`(?s)<setting id="` + regexp.QuoteMeta(id) + `"[^>]*?(?:/>|>.*?</setting>)`)
	repl := `<setting id="` + id + `">` + xmlEscape(val) + `</setting>`
	if loc := re.FindStringIndex(xml); loc != nil {
		return xml[:loc[0]] + repl + xml[loc[1]:]
	}
	return strings.Replace(xml, "</settings>", "    "+repl+"\n</settings>", 1)
}

// tuneRPC tunnels to Kodi's JSON-RPC port (device localhost:9090) through adb.
func tuneRPC(ip string, settings []kv) error {
	shell(ip, "input keyevent 224", 5*time.Second)
	shell(ip, "monkey -p "+kodiPkg+" -c android.intent.category.LAUNCHER 1", 15*time.Second)
	r := adb([]string{"forward", "tcp:0", "tcp:9090"}, serialOf(ip), 10*time.Second)
	port, err := strconv.Atoi(strings.TrimSpace(r.Out))
	if err != nil {
		return errors.New("adb forward failed: " + r.Out)
	}
	defer adb([]string{"forward", "--remove", fmt.Sprintf("tcp:%d", port)}, serialOf(ip), 10*time.Second)
	up := false
	for i := 0; i < 45; i++ {
		if _, err := tcpRPC(port, "JSONRPC.Ping", nil, 3*time.Second); err == nil {
			up = true
			break
		}
		time.Sleep(2 * time.Second)
	}
	if !up {
		return errors.New("Kodi did not start")
	}
	var failed []string
	for _, s := range settings {
		if err := setWithConfirm(ip, port, s.K, s.V); err != nil {
			failed = append(failed, s.K+": "+err.Error())
		}
	}
	if len(failed) > 0 {
		return errors.New(strings.Join(failed, "; "))
	}
	return nil
}

// setWithConfirm: turning on Kodi's web server pops up "Warning! ... Proceed?" on the TV
// and the call waits for an answer. If no reply comes quickly, press "Yes" over adb.
func setWithConfirm(ip string, port int, k string, v any) error {
	done := make(chan error, 1)
	go func() {
		_, err := tcpRPC(port, "Settings.SetSettingValue", map[string]any{"setting": k, "value": v}, 90*time.Second)
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(4 * time.Second):
	}
	shell(ip, "input keyevent 21", 5*time.Second) // focus moves from "No" to "Yes"
	time.Sleep(500 * time.Millisecond)
	shell(ip, "input keyevent 23", 5*time.Second) // OK
	select {
	case err := <-done:
		return err
	case <-time.After(60 * time.Second):
		return errors.New("no reply from Kodi")
	}
}

func tcpRPC(port int, method string, params any, timeout time.Duration) (any, error) {
	c, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", port), 3*time.Second)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(timeout))
	if params == nil {
		params = map[string]any{}
	}
	req, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if _, err := c.Write(req); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(io.LimitReader(c, 8<<20))
	var resp struct {
		Result any `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := dec.Decode(&resp); err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, errors.New(resp.Error.Message)
	}
	return resp.Result, nil
}

// ---------------------------------------------------------------- Kodi web JSON-RPC

var kodiHTTP = &http.Client{Timeout: 5 * time.Second}

var errNoPass = errors.New("Kodi not optimized yet")

func rpc(ip, method string, params map[string]any) (any, error) {
	pass := device(ip).KodiPass
	if pass == "" {
		return nil, errNoPass
	}
	if params == nil {
		params = map[string]any{}
	}
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req, err := http.NewRequest("POST", "http://"+net.JoinHostPort(ip, "8080")+"/jsonrpc", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("kodi", pass)
	resp, err := kodiHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 {
		return nil, errors.New("Kodi password mismatch (press Optimize Kodi)")
	}
	var out struct {
		Result any `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&out); err != nil {
		return nil, err
	}
	if out.Error != nil {
		return nil, errors.New(out.Error.Message)
	}
	return out.Result, nil
}

func activePlayer(ip string) (int, bool) {
	res, err := rpc(ip, "Player.GetActivePlayers", nil)
	if err != nil {
		return 0, false
	}
	list, _ := res.([]any)
	for _, p := range list {
		m, _ := p.(map[string]any)
		if str(m["type"]) == "video" {
			return int(num(m["playerid"])), true
		}
	}
	return 0, false
}

var keycodes = map[string]string{"playpause": "85", "stop": "86", "back": "4", "left": "21", "right": "22",
	"up": "19", "down": "20", "ok": "23", "volup": "24", "voldown": "25", "home": "3"}

func control(ip, action, value string) error {
	err := controlRPC(ip, action, value)
	if err == nil {
		return nil
	}
	// fall back to remote-control key presses over adb
	key := action
	if action == "seekrel" {
		if n, _ := strconv.Atoi(value); n < 0 {
			key = "left"
		} else {
			key = "right"
		}
	}
	if code, ok := keycodes[key]; ok {
		if r := shell(ip, "input keyevent "+code, 8*time.Second); r.OK() {
			return nil
		}
	}
	return err
}

func controlRPC(ip, action, value string) error {
	switch action {
	case "volup":
		_, err := rpc(ip, "Application.SetVolume", map[string]any{"volume": "increment"})
		return err
	case "voldown":
		_, err := rpc(ip, "Application.SetVolume", map[string]any{"volume": "decrement"})
		return err
	}
	pid, ok := activePlayer(ip)
	if !ok {
		return errors.New("nothing is playing")
	}
	var err error
	switch action {
	case "playpause":
		_, err = rpc(ip, "Player.PlayPause", map[string]any{"playerid": pid})
	case "stop":
		_, err = rpc(ip, "Player.Stop", map[string]any{"playerid": pid})
	case "seekrel":
		n, _ := strconv.Atoi(value)
		_, err = rpc(ip, "Player.Seek", map[string]any{"playerid": pid, "value": map[string]any{"seconds": n}})
	case "seekpct":
		f, _ := strconv.ParseFloat(value, 64)
		_, err = rpc(ip, "Player.Seek", map[string]any{"playerid": pid, "value": map[string]any{"percentage": f}})
	case "audio":
		n, _ := strconv.Atoi(value)
		_, err = rpc(ip, "Player.SetAudioStream", map[string]any{"playerid": pid, "stream": n})
	case "sub":
		if value == "off" {
			_, err = rpc(ip, "Player.SetSubtitle", map[string]any{"playerid": pid, "subtitle": "off"})
		} else {
			n, _ := strconv.Atoi(value)
			_, err = rpc(ip, "Player.SetSubtitle", map[string]any{"playerid": pid, "subtitle": n, "enable": true})
		}
	default:
		err = errors.New("unknown action")
	}
	return err
}

func secs(v any) int {
	m, _ := v.(map[string]any)
	return int(num(m["hours"]))*3600 + int(num(m["minutes"]))*60 + int(num(m["seconds"]))
}

func status(ip string) map[string]any {
	if ip == "" || device(ip).KodiPass == "" {
		return map[string]any{"playing": false}
	}
	pid, ok := activePlayer(ip)
	if !ok {
		return map[string]any{"playing": false}
	}
	res, err := rpc(ip, "Player.GetProperties", map[string]any{"playerid": pid, "properties": []string{
		"time", "totaltime", "percentage", "speed", "audiostreams", "currentaudiostream",
		"subtitles", "currentsubtitle", "subtitleenabled"}})
	if err != nil {
		return map[string]any{"playing": false, "err": err.Error()}
	}
	p, _ := res.(map[string]any)
	title := ""
	if it, err := rpc(ip, "Player.GetItem", map[string]any{"playerid": pid, "properties": []string{"file"}}); err == nil {
		m, _ := it.(map[string]any)
		item, _ := m["item"].(map[string]any)
		f := str(item["file"])
		if i := strings.LastIndex(f, "/"); i >= 0 {
			f = f[i+1:]
		}
		title, _ = url.PathUnescape(f)
		if title == "" {
			title = str(item["label"])
		}
	}
	var audios, subs []map[string]any
	for _, a := range asList(p["audiostreams"]) {
		audios = append(audios, map[string]any{"i": int(num(a["index"])), "n": strings.TrimSpace(fmt.Sprintf("%s %s %dch",
			strings.ToUpper(str(a["language"])), str(a["name"]), int(num(a["channels"]))))})
	}
	for _, s := range asList(p["subtitles"]) {
		subs = append(subs, map[string]any{"i": int(num(s["index"])), "n": strings.TrimSpace(
			strings.ToUpper(str(s["language"])) + " " + str(s["name"]))})
	}
	cur := func(k string) any {
		m, _ := p[k].(map[string]any)
		if m == nil {
			return nil
		}
		return int(num(m["index"]))
	}
	var sub any = "off"
	if b, _ := p["subtitleenabled"].(bool); b {
		sub = cur("currentsubtitle")
	}
	if audios == nil {
		audios = []map[string]any{}
	}
	if subs == nil {
		subs = []map[string]any{}
	}
	return map[string]any{"playing": true, "paused": num(p["speed"]) == 0, "time": secs(p["time"]),
		"total": secs(p["totaltime"]), "pct": num(p["percentage"]), "title": title,
		"audios": audios, "audio": cur("currentaudiostream"), "subs": subs, "sub": sub}
}

func asList(v any) []map[string]any {
	l, _ := v.([]any)
	out := make([]map[string]any, 0, len(l))
	for _, x := range l {
		if m, ok := x.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// ---------------------------------------------------------------- play

func encodeName(name string) string {
	var b strings.Builder
	for _, c := range []byte(name) {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.IndexByte("-._~", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func play(ip, id string) (bool, string) {
	path, ok := library.Path(id)
	if !ok {
		return false, "That file is gone. Press Rescan."
	}
	if ip == "" {
		return false, "Pick a TV first"
	}
	okADB, msg := ensureADB(ip)
	if !okADB {
		if nip := relocate(ip); nip != "" {
			ip = nip
			withConfig(func(c *Config) { c.Selected = nip })
			okADB, msg = ensureADB(ip)
		}
	}
	if !okADB {
		return false, msg
	}
	d := device(ip)
	if d.Kodi == nil || !*d.Kodi {
		info := describe(ip)
		remember(info)
		if !info.Kodi {
			return false, "Kodi isn't installed on this TV yet. Press “Install Kodi”."
		}
	}
	if !device(ip).Tuned {
		if ok, m := tuneKodi(ip); !ok {
			log.Printf("auto-optimize failed: %s", m)
		}
	}
	mac := localIPToward(ip)
	if mac == "" {
		return false, "This computer isn't on the same network as the TV"
	}
	name := filepath.Base(path)
	u := fmt.Sprintf("http://%s:%d/m/%s/%s/%s", mac, Port, token(), id, encodeName(name))
	mime := videoExt[strings.ToLower(filepath.Ext(name))]
	if mime == "" {
		mime = "video/*"
	}
	shell(ip, "input keyevent 224", 5*time.Second) // wake the TV
	r := shell(ip, fmt.Sprintf("am start -a android.intent.action.VIEW -t %s -d %s -n %s/.Splash",
		shellQuote(mime), shellQuote(u), kodiPkg), 20*time.Second)
	if !r.OK() || strings.Contains(r.Out, "Error") {
		return false, "The TV refused to start Kodi: " + r.Out
	}
	markActive()
	return true, "Playing on " + firstNonEmpty(device(ip).Name, ip)
}

// ---------------------------------------------------------------- install Kodi

var installMu sync.Mutex

func installKodi(ip string) (bool, string) {
	installMu.Lock()
	defer installMu.Unlock()
	if ok, msg := ensureADB(ip); !ok {
		return false, msg
	}
	abis := shell(ip, "getprop ro.product.cpu.abilist", 10*time.Second).Out
	dir, abi := "arm", "armeabi-v7a"
	if strings.Contains(abis, "arm64-v8a") {
		dir, abi = "arm64-v8a", "arm64-v8a"
	} else if strings.Contains(abis, "x86_64") {
		dir, abi = "x86_64", "x86_64"
	}
	ver, name := latestKodi(dir, abi)
	apkURL := fmt.Sprintf("https://mirrors.kodi.tv/releases/android/%s/kodi-%s-%s-%s.apk", dir, ver, name, abi)
	client := http.Client{Timeout: 20 * time.Minute}
	sumResp, err := client.Get(apkURL + ".sha256")
	if err != nil {
		return false, "Download failed: " + err.Error()
	}
	sumBytes, _ := io.ReadAll(io.LimitReader(sumResp.Body, 4096))
	sumResp.Body.Close()
	want := strings.ToLower(strings.Fields(string(sumBytes) + " x")[0])
	if len(want) != 64 {
		return false, "Could not get Kodi's checksum"
	}
	tmp, err := os.MkdirTemp("", "tvcast-kodi")
	if err != nil {
		return false, err.Error()
	}
	defer os.RemoveAll(tmp)
	apk := filepath.Join(tmp, "kodi.apk")
	resp, err := client.Get(apkURL)
	if err != nil {
		return false, "Download failed: " + err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return false, fmt.Sprintf("Download failed: HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(apk)
	if err != nil {
		return false, err.Error()
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, 500<<20))
	f.Close()
	if err != nil {
		return false, "Download failed: " + err.Error()
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return false, "Downloaded Kodi failed its integrity check; not installed"
	}
	r := adb([]string{"install", "-r", apk}, serialOf(ip), 15*time.Minute)
	if !r.OK() || !strings.Contains(r.Out, "Success") {
		return false, "Install failed: " + r.Out
	}
	for _, p := range []string{"READ_EXTERNAL_STORAGE", "WRITE_EXTERNAL_STORAGE"} {
		shell(ip, "pm grant "+kodiPkg+" android.permission."+p, 10*time.Second)
	}
	withConfig(func(c *Config) {
		if d := c.Devices[ip]; d != nil {
			d.Kodi = boolPtr(true)
			d.Tuned = false
		}
	})
	ok, msg := tuneKodi(ip)
	if !ok {
		return true, "Kodi installed. Optimizing failed: " + msg
	}
	return true, "Kodi " + ver + " installed and optimized"
}

var kodiReleaseRe = regexp.MustCompile(`kodi-(\d+)\.(\d+)(?:\.(\d+))?-([A-Za-z]+)-([a-z0-9_-]+)\.apk"`)

// latestKodi reads Kodi's official release folder and returns the newest stable version.
func latestKodi(dir, abi string) (string, string) {
	best, bestName := kodiVersion, kodiCodename
	bestKey := [3]int{21, 2, 0}
	c := http.Client{Timeout: 20 * time.Second}
	resp, err := c.Get("https://mirrors.kodi.tv/releases/android/" + dir + "/")
	if err != nil {
		return best, bestName
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	for _, m := range kodiReleaseRe.FindAllStringSubmatch(string(body), -1) {
		if m[5] != abi {
			continue
		}
		a, _ := strconv.Atoi(m[1])
		b, _ := strconv.Atoi(m[2])
		c3, _ := strconv.Atoi(m[3])
		k := [3]int{a, b, c3}
		if k[0] > bestKey[0] || (k[0] == bestKey[0] && (k[1] > bestKey[1] || (k[1] == bestKey[1] && k[2] > bestKey[2]))) {
			bestKey = k
			best = m[1] + "." + m[2]
			if m[3] != "" {
				best += "." + m[3]
			}
			bestName = m[4]
		}
	}
	return best, bestName
}

// kodiInstalledVersion returns the Kodi version on the TV ("" if not installed).
func kodiInstalledVersion(ip string) string {
	out := shell(ip, "dumpsys package "+kodiPkg+" | grep -m1 versionName", 15*time.Second).Out
	if i := strings.Index(out, "versionName="); i >= 0 {
		v := strings.Fields(out[i+len("versionName="):] + " ")[0]
		return v
	}
	return ""
}
