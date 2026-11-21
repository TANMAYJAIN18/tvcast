package main

import (
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
	"time"
)

//go:embed web/index.html
var indexHTML []byte

var ipRe = regexp.MustCompile(`^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}$`)

func newRouter() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil && rec != http.ErrAbortHandler {
				log.Printf("panic serving %s: %v\n%s", r.URL.Path, rec, debug.Stack())
				http.Error(w, "internal error", 500)
			}
		}()
		if strings.HasPrefix(r.URL.Path, "/m/") {
			serveMedia(w, r)
			return
		}
		// The control UI/API is only for this computer: loopback peer + loopback Host
		// (blocks other devices and DNS-rebinding attacks).
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			http.Error(w, "forbidden", 403)
			return
		}
		if h := r.Host; h != fmt.Sprintf("127.0.0.1:%d", Port) && h != fmt.Sprintf("localhost:%d", Port) {
			http.Error(w, "forbidden", 403)
			return
		}
		api(w, r)
	})
}

// ---------------------------------------------------------------- media

func serveMedia(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", 405)
		return
	}
	// /m/<token>/<id>/<filename>
	parts := strings.Split(strings.TrimPrefix(r.URL.EscapedPath(), "/m/"), "/")
	if len(parts) != 3 || subtle.ConstantTimeCompare([]byte(parts[0]), []byte(token())) != 1 {
		w.WriteHeader(404) // probes for folders, subtitles, artwork: answer fast and empty
		return
	}
	path, ok := library.Path(parts[1])
	name, _ := url.PathUnescape(parts[2])
	if !ok || name != filepath.Base(path) {
		w.WriteHeader(404)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		w.WriteHeader(404)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		w.WriteHeader(404)
		return
	}
	markActive()
	if ct := videoExt[strings.ToLower(filepath.Ext(name))]; ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	if tc, ok := w.(interface{ SetWriteBuffer(int) error }); ok {
		_ = tc.SetWriteBuffer(4 << 20)
	}
	// ServeContent handles Range, HEAD, If-Range, 416 etc. and uses sendfile.
	http.ServeContent(w, r, name, st.ModTime(), f)
}

// ---------------------------------------------------------------- API

var mutating = map[string]bool{
	"/api/adddevice": true, "/api/select": true, "/api/tune": true, "/api/play": true, "/api/control": true,
	"/api/addfolder": true, "/api/removefolder": true, "/api/openfile": true, "/api/installkodi": true,
	"/api/lidguard": true, "/api/forget": true, "/api/reconnect": true, "/api/scan": true, "/api/rescan": true,
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	b, err := json.Marshal(v)
	if err != nil {
		b = []byte(`{"ok":false,"msg":"encode error"}`)
	}
	_, _ = w.Write(b)
}

func result(ok bool, msg string) map[string]any { return map[string]any{"ok": ok, "msg": msg} }

func api(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	q := map[string]string{}
	for k, v := range r.URL.Query() {
		if len(v) > 0 {
			q[k] = v[0]
		}
	}
	if mutating[p] {
		if r.Method != http.MethodPost {
			http.Error(w, "use POST", 405)
			return
		}
		// only our own page may trigger actions (blocks cross-site requests from web pages)
		if o := r.Header.Get("Origin"); o != "" && o != fmt.Sprintf("http://127.0.0.1:%d", Port) && o != fmt.Sprintf("http://localhost:%d", Port) {
			http.Error(w, "forbidden", 403)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err == nil {
			for k, v := range body {
				q[k] = fmt.Sprint(v)
			}
		}
	}
	ip := q["ip"]
	if ip == "" {
		ip = selectedIP()
	}
	if ip != "" && !ipRe.MatchString(ip) {
		writeJSON(w, result(false, "Invalid IP address"))
		return
	}

	switch p {
	case "/", "/index.html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:")
		_, _ = w.Write(indexHTML)
	case "/api/ping":
		writeJSON(w, map[string]any{"ok": true, "app": "tvcast", "version": Version})
	case "/api/info":
		var folders []string
		readConfig(func(c *Config) { folders = append(folders, c.Folders...) })
		writeJSON(w, map[string]any{"version": Version, "os": runtime.GOOS, "lidGuard": lidGuardInstalled(),
			"awake": isAwake(), "adb": findTool("adb") != "", "ffprobe": findTool("ffprobe") != "", "folders": folders,
			"videos": library.Count()})
	case "/api/library":
		writeJSON(w, map[string]any{"items": library.Items()})
	case "/api/rescan":
		library.Scan()
		writeJSON(w, map[string]any{"items": library.Items()})
	case "/api/probe":
		writeJSON(w, library.Probe(q["id"]))
	case "/api/devices":
		writeJSON(w, deviceList())
	case "/api/scan":
		discover()
		writeJSON(w, deviceList())
	case "/api/adddevice":
		if ip == "" {
			writeJSON(w, result(false, "Enter the TV's IP address"))
			return
		}
		d := describe(ip)
		if d.ADB {
			remember(d)
			withConfig(func(c *Config) { c.Selected = ip })
			writeJSON(w, result(true, "Added "+d.Name))
		} else {
			withConfig(func(c *Config) {
				if c.Devices[ip] == nil {
					c.Devices[ip] = &Device{Name: ip}
				}
				c.Selected = ip
			})
			writeJSON(w, result(false, d.Msg))
		}
	case "/api/reconnect":
		adb([]string{"disconnect", serialOf(ip)}, "", 10*time.Second)
		d := describe(ip)
		if !d.ADB {
			if nip := relocate(ip); nip != "" {
				d = describe(nip)
				ip = nip
			}
		}
		if d.ADB {
			remember(d)
			writeJSON(w, result(true, "Connected to "+d.Name))
		} else {
			writeJSON(w, result(false, d.Msg))
		}
	case "/api/forget":
		withConfig(func(c *Config) {
			delete(c.Devices, ip)
			if c.Selected == ip {
				c.Selected = ""
				for k := range c.Devices {
					c.Selected = k
					break
				}
			}
		})
		adb([]string{"disconnect", serialOf(ip)}, "", 10*time.Second)
		writeJSON(w, result(true, "Removed"))
	case "/api/select":
		withConfig(func(c *Config) {
			if c.Devices[ip] != nil {
				c.Selected = ip
			}
		})
		writeJSON(w, result(true, ""))
	case "/api/tune":
		ok, msg := tuneKodi(ip)
		writeJSON(w, result(ok, msg))
	case "/api/installkodi":
		ok, msg := installKodi(ip)
		writeJSON(w, result(ok, msg))
	case "/api/kodilatest":
		abis := shell(ip, "getprop ro.product.cpu.abilist", 10*time.Second).Out
		dir, abi := "arm", "armeabi-v7a"
		if strings.Contains(abis, "arm64-v8a") {
			dir, abi = "arm64-v8a", "arm64-v8a"
		}
		v, _ := latestKodi(dir, abi)
		writeJSON(w, map[string]any{"latest": v, "installed": kodiInstalledVersion(ip)})
	case "/api/play":
		ok, msg := play(ip, q["id"])
		writeJSON(w, result(ok, msg))
	case "/api/control":
		if err := control(ip, q["action"], q["value"]); err != nil {
			writeJSON(w, result(false, err.Error()))
		} else {
			writeJSON(w, result(true, ""))
		}
	case "/api/status":
		writeJSON(w, status(ip))
	case "/api/addfolder":
		path := q["path"]
		if path == "" {
			var supported bool
			path, supported = pickFolder()
			if !supported {
				writeJSON(w, map[string]any{"ok": false, "needPath": true})
				return
			}
		}
		if st, err := os.Stat(path); path == "" || err != nil || !st.IsDir() {
			writeJSON(w, result(false, ""))
			return
		}
		withConfig(func(c *Config) {
			for _, f := range c.Folders {
				if f == path {
					return
				}
			}
			c.Folders = append(c.Folders, path)
		})
		library.Scan()
		writeJSON(w, result(true, "Added "+path))
	case "/api/removefolder":
		withConfig(func(c *Config) {
			var keep []string
			for _, f := range c.Folders {
				if f != q["path"] {
					keep = append(keep, f)
				}
			}
			c.Folders = keep
		})
		library.Scan()
		writeJSON(w, result(true, ""))
	case "/api/openfile":
		path := q["path"]
		if path == "" {
			var supported bool
			path, supported = pickFile()
			if !supported {
				writeJSON(w, map[string]any{"ok": false, "needPath": true})
				return
			}
		}
		if st, err := os.Stat(path); path == "" || err != nil || !st.Mode().IsRegular() {
			writeJSON(w, result(false, ""))
			return
		}
		withConfig(func(c *Config) {
			for _, f := range c.Files {
				if f == path {
					return
				}
			}
			c.Files = append(c.Files, path)
		})
		library.Scan()
		writeJSON(w, map[string]any{"ok": true, "id": fileID(path)})
	case "/api/lidguard":
		ok, msg := installLidGuard()
		writeJSON(w, result(ok, msg))
	default:
		http.NotFound(w, r)
	}
}
