package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Device struct {
	Name     string `json:"name,omitempty"`
	Model    string `json:"model,omitempty"`
	Serial   string `json:"serial,omitempty"`
	MemMB    int    `json:"mem_mb,omitempty"`
	Kodi     *bool  `json:"kodi,omitempty"`
	Tuned    bool   `json:"tuned,omitempty"`
	KodiPass string `json:"kodi_pass,omitempty"`
	CacheMB  int    `json:"cache_mb,omitempty"`
	KodiVer  string `json:"kodi_ver,omitempty"`
}

type Config struct {
	Token    string             `json:"token"`
	Folders  []string           `json:"folders"`
	Files    []string           `json:"files"`
	Devices  map[string]*Device `json:"devices"`
	Selected string             `json:"selected"`
}

var (
	cfgMu sync.Mutex
	cfg   Config
)

func appDir() string {
	d, err := os.UserConfigDir()
	if err != nil || d == "" {
		d = os.TempDir()
	}
	return filepath.Join(d, "TVCast")
}

func configPath() string { return filepath.Join(appDir(), "config.json") }

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand never fails on supported OSes; fall back to time to stay alive anyway
		return base64.RawURLEncoding.EncodeToString([]byte(time.Now().String()))[:n]
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func loadConfig() {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	b, err := os.ReadFile(configPath())
	if err == nil {
		if err := json.Unmarshal(b, &cfg); err != nil {
			log.Printf("config unreadable (%v); keeping a backup and starting fresh", err)
			_ = os.WriteFile(configPath()+".bad", b, 0o600)
			cfg = Config{}
		}
	}
	if cfg.Token == "" {
		cfg.Token = randomToken(12)
	}
	if cfg.Folders == nil {
		cfg.Folders = defaultFolders()
	}
	if cfg.Files == nil {
		cfg.Files = []string{}
	}
	if cfg.Devices == nil {
		cfg.Devices = map[string]*Device{}
	}
	saveConfigLocked()
}

func saveConfigLocked() {
	_ = os.MkdirAll(appDir(), 0o755)
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		log.Printf("config encode: %v", err)
		return
	}
	tmp := configPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		log.Printf("config write: %v", err)
		return
	}
	if err := os.Rename(tmp, configPath()); err != nil {
		log.Printf("config rename: %v", err)
	}
}

// withConfig runs fn while holding the config lock and saves afterwards.
func withConfig(fn func(c *Config)) {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	fn(&cfg)
	saveConfigLocked()
}

// readConfig runs fn while holding the config lock (no save).
func readConfig(fn func(c *Config)) {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	fn(&cfg)
}

// device returns a copy of the stored device (zero value if unknown).
func device(ip string) Device {
	var d Device
	readConfig(func(c *Config) {
		if p := c.Devices[ip]; p != nil {
			d = *p
		}
	})
	return d
}

func token() string {
	var t string
	readConfig(func(c *Config) { t = c.Token })
	return t
}

func selectedIP() string {
	var s string
	readConfig(func(c *Config) { s = c.Selected })
	return s
}

func boolPtr(b bool) *bool { return &b }
