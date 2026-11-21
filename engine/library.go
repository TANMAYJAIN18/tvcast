package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var videoExt = map[string]string{
	".mkv": "video/x-matroska", ".mp4": "video/mp4", ".m4v": "video/x-m4v", ".avi": "video/x-msvideo",
	".mov": "video/quicktime", ".ts": "video/mp2t", ".m2ts": "video/mp2t", ".webm": "video/webm",
	".wmv": "video/x-ms-wmv", ".mpg": "video/mpeg", ".mpeg": "video/mpeg", ".flv": "video/x-flv", ".3gp": "video/3gpp",
}

const minVideoSize = 30 << 20 // ignore clips/samples under 30 MB

type Library struct {
	mu       sync.Mutex
	scanMu   sync.Mutex
	paths    map[string]string // id -> path
	probeMu  sync.Mutex
	probes   map[string]map[string]any
	probeDir string
}

var library = &Library{paths: map[string]string{}, probes: loadProbes()}

func fileID(path string) string {
	h := sha1.Sum([]byte(path))
	return hex.EncodeToString(h[:])[:16]
}

func probePath() string { return filepath.Join(appDir(), "probe.json") }

func loadProbes() map[string]map[string]any {
	m := map[string]map[string]any{}
	if b, err := os.ReadFile(probePath()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func (l *Library) Scan() {
	l.scanMu.Lock() // one scan at a time
	defer l.scanMu.Unlock()
	found := map[string]string{}
	var folders, files []string
	readConfig(func(c *Config) {
		folders = append(folders, c.Folders...)
		files = append(files, c.Files...)
	})
	roots := append(folders, extraVolumeRoots()...)
	skip := skipDirs()
	for _, root := range roots {
		st, err := os.Stat(root)
		if err != nil || !st.IsDir() {
			continue
		}
		baseDepth := strings.Count(filepath.Clean(root), string(os.PathSeparator))
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			name := d.Name()
			if d.IsDir() {
				if p != root && (strings.HasPrefix(name, ".") || skip[p] ||
					strings.Count(p, string(os.PathSeparator))-baseDepth > 6 || strings.HasSuffix(name, ".app")) {
					return fs.SkipDir
				}
				return nil
			}
			if strings.HasPrefix(name, ".") {
				return nil
			}
			if _, ok := videoExt[strings.ToLower(filepath.Ext(name))]; !ok {
				return nil
			}
			if info, err := d.Info(); err == nil && info.Mode().IsRegular() && info.Size() >= minVideoSize {
				found[fileID(p)] = p
			}
			return nil
		})
	}
	for _, p := range files {
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			found[fileID(p)] = p
		}
	}
	l.mu.Lock()
	l.paths = found
	l.mu.Unlock()
	log.Printf("library: %d videos", len(found))
}

func (l *Library) Path(id string) (string, bool) {
	l.mu.Lock()
	p, ok := l.paths[id]
	l.mu.Unlock()
	if !ok {
		l.Scan()
		l.mu.Lock()
		p, ok = l.paths[id]
		l.mu.Unlock()
	}
	return p, ok
}

func (l *Library) Count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.paths)
}

type Item struct {
	ID    string  `json:"id"`
	Name  string  `json:"name"`
	Dir   string  `json:"dir"`
	Size  int64   `json:"size"`
	MTime float64 `json:"mtime"`
}

func (l *Library) Items() []Item {
	l.mu.Lock()
	paths := make(map[string]string, len(l.paths))
	for k, v := range l.paths {
		paths[k] = v
	}
	l.mu.Unlock()
	home, _ := os.UserHomeDir()
	items := make([]Item, 0, len(paths))
	for id, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		dir := filepath.Dir(p)
		if home != "" && strings.HasPrefix(dir, home) {
			dir = "~" + dir[len(home):]
		}
		items = append(items, Item{ID: id, Name: filepath.Base(p), Dir: dir, Size: st.Size(),
			MTime: float64(st.ModTime().UnixNano()) / 1e9})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].MTime > items[j].MTime })
	return items
}

// Probe returns codec/resolution info for a file using ffprobe (if installed). Cached.
func (l *Library) Probe(id string) map[string]any {
	p, ok := l.Path(id)
	if !ok {
		return map[string]any{}
	}
	st, err := os.Stat(p)
	if err != nil {
		return map[string]any{}
	}
	key := fmt.Sprintf("%s|%d|%d", p, st.Size(), st.ModTime().Unix())
	l.probeMu.Lock()
	if v, ok := l.probes[key]; ok {
		l.probeMu.Unlock()
		return v
	}
	l.probeMu.Unlock()

	info := map[string]any{}
	ff := findTool("ffprobe")
	if ff == "" {
		return info // optional tool; don't cache so it works once installed
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ff, "-v", "error", "-print_format", "json", "-show_format", "-show_streams", p)
	hideWindow(cmd)
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.Output()
	if err != nil {
		return info
	}
	var d struct {
		Streams []map[string]any `json:"streams"`
		Format  map[string]any   `json:"format"`
	}
	if json.Unmarshal(out, &d) != nil {
		return info
	}
	var v map[string]any
	var audios []string
	subs := 0
	for _, s := range d.Streams {
		switch str(s["codec_type"]) {
		case "video":
			if disp, _ := s["disposition"].(map[string]any); disp != nil && num(disp["attached_pic"]) == 1 {
				continue
			}
			if v == nil {
				v = s
			}
		case "audio":
			lang := "?"
			if t, _ := s["tags"].(map[string]any); t != nil && str(t["language"]) != "" {
				lang = str(t["language"])
			}
			audios = append(audios, strings.TrimSpace(fmt.Sprintf("%s %s %dch", strings.ToUpper(lang),
				strings.ToUpper(str(s["codec_name"])), int(num(s["channels"])))))
		case "subtitle":
			subs++
		}
	}
	if v == nil {
		v = map[string]any{}
	}
	w, h := num(v["width"]), num(v["height"])
	res := ""
	switch {
	case w >= 3200 || h >= 2000:
		res = "4K"
	case w >= 1800 || h >= 1000:
		res = "1080p"
	case h >= 700:
		res = "720p"
	case h > 0:
		res = fmt.Sprintf("%dp", int(h))
	}
	hdr := ""
	switch str(v["color_transfer"]) {
	case "smpte2084":
		hdr = "HDR10"
	case "arib-std-b67":
		hdr = "HLG"
	}
	if sd, err := json.Marshal(v["side_data_list"]); err == nil && strings.Contains(string(sd), "DOVI") {
		hdr = "Dolby Vision"
	}
	if tag := str(v["codec_tag_string"]); tag == "dvh1" || tag == "dvhe" {
		hdr = "Dolby Vision"
	}
	br := num(d.Format["bit_rate"])
	info = map[string]any{
		"res": res, "vcodec": strings.ToUpper(str(v["codec_name"])), "hdr": hdr,
		"bit10": strings.Contains(str(v["pix_fmt"]), "10"), "duration": num(d.Format["duration"]),
		"audio": audios, "subs": subs,
	}
	if br > 0 {
		info["mbps"] = math.Round(br/1e5) / 10
	}
	l.probeMu.Lock()
	l.probes[key] = info
	if b, err := json.Marshal(l.probes); err == nil {
		_ = os.WriteFile(probePath(), b, 0o600)
	}
	l.probeMu.Unlock()
	return info
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// num converts JSON numbers and numeric strings (ffprobe uses both) to float64; NaN/Inf become 0.
func num(v any) float64 {
	var f float64
	switch t := v.(type) {
	case float64:
		f = t
	case string:
		_, _ = fmt.Sscan(t, &f)
	case int:
		f = float64(t)
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return f
}
