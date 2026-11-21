package main

import (
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var lastMedia atomic.Int64 // unix seconds of the last movie byte request

func markActive() { lastMedia.Store(time.Now().Unix()) }

var (
	awakeMu sync.Mutex
	awake   bool
)

func isAwake() bool {
	awakeMu.Lock()
	defer awakeMu.Unlock()
	return awake
}

// keepAwakeLoop keeps the computer from sleeping while a TV is playing (or paused on)
// one of our movies, and lets it sleep normally otherwise. It also runs light health
// checks: re-finding TVs whose address changed and restarting a stuck adb.
func keepAwakeLoop() {
	tick := 0
	for {
		active := time.Now().Unix()-lastMedia.Load() < 120 || anyTVPlayingOurs()
		awakeMu.Lock()
		changed := active != awake
		awake = active
		awakeMu.Unlock()
		safeGo("setAwake", func() { setAwake(active) }) // platform-specific; refreshed every tick
		if changed {
			if active {
				log.Printf("streaming: keeping this computer awake")
			} else {
				log.Printf("idle: normal sleep allowed")
			}
		}
		tick++
		if tick%15 == 0 { // ~5 min
			safeGo("health", healthCheck)
		}
		time.Sleep(20 * time.Second)
	}
}

func anyTVPlayingOurs() bool {
	marker := "/m/" + token() + "/"
	var ips []string
	readConfig(func(c *Config) {
		for ip, d := range c.Devices {
			if d.KodiPass != "" {
				ips = append(ips, ip)
			}
		}
	})
	for _, ip := range ips {
		if !portOpen(ip, 8080, 600*time.Millisecond) {
			continue
		}
		pid, ok := activePlayer(ip)
		if !ok {
			continue
		}
		res, err := rpc(ip, "Player.GetItem", map[string]any{"playerid": pid, "properties": []string{"file"}})
		if err != nil {
			continue
		}
		m, _ := res.(map[string]any)
		item, _ := m["item"].(map[string]any)
		if strings.Contains(str(item["file"]), marker) {
			return true
		}
	}
	return false
}

func healthCheck() {
	// adb server wedged? ("adb devices" should answer in well under 10 s)
	if !adbAvailable() {
		ensureADBInstalled()
		adbStartServer()
	} else if !useNativeADB {
		if r := adb([]string{"devices"}, "", 10*time.Second); r.Code == -2 {
			log.Printf("adb not responding; restarting it")
			adb([]string{"kill-server"}, "", 10*time.Second)
			adbStartServer()
		}
	}
	// a known TV vanished from its address (new IP from the router)? search again.
	missing := false
	readConfig(func(c *Config) {
		for ip, d := range c.Devices {
			if d.Serial != "" && !portOpen(ip, 5555, 700*time.Millisecond) {
				missing = true
			}
		}
	})
	if missing {
		discover()
	}
}
