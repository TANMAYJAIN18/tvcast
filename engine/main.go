// TV Cast engine: streams movies from this computer to Android TV / Fire TV (Kodi)
// with zero transcoding. The original file is served over HTTP with byte ranges and
// the TV's Kodi plays it with hardware decoding, so 4K/HDR/10 GB+ files keep full quality.
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"
)

const Port = 8765

// Version is set at build time with -ldflags "-X main.Version=1.2.3".
var Version = "dev"

func main() {
	background := flag.Bool("background", false, "run the engine without opening the user interface")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("TV Cast", Version)
		return
	}

	setupLogging()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("FATAL panic: %v\n%s", r, debug.Stack())
			os.Exit(2)
		}
	}()

	// Only one engine per computer. A second launch just opens the UI.
	if engineRunning() {
		if !*background {
			openUI()
		}
		return
	}

	log.Printf("TV Cast %s starting (%s/%s)", Version, runtime.GOOS, runtime.GOARCH)
	loadConfig()
	go safeGo("library", func() { library.Scan() })
	go safeGo("adb", func() {
		ensureADBInstalled()
		adbStartServer()
		discover()
	})
	go safeGo("keepawake", keepAwakeLoop)

	ln, err := net.Listen("tcp4", fmt.Sprintf(":%d", Port))
	if err != nil {
		log.Printf("cannot listen on port %d: %v", Port, err)
		os.Exit(1)
	}
	srv := &http.Server{
		Handler:           newRouter(),
		ReadHeaderTimeout: 20 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          log.New(io.Discard, "", 0), // client disconnects mid-stream are normal
	}
	if !*background {
		go func() { time.Sleep(400 * time.Millisecond); openUI() }()
	}
	log.Printf("listening on http://127.0.0.1:%d", Port)
	if err := srv.Serve(ln); err != nil {
		log.Printf("server stopped: %v", err)
		os.Exit(1)
	}
}

// safeGo runs fn and logs (instead of crashing on) any panic.
func safeGo(name string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("panic in %s: %v\n%s", name, r, debug.Stack())
		}
	}()
	fn()
}

func engineRunning() bool {
	c := http.Client{Timeout: 800 * time.Millisecond}
	resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/api/ping", Port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return strings.Contains(string(b), "tvcast")
}

func uiURL() string { return fmt.Sprintf("http://127.0.0.1:%d/", Port) }

func setupLogging() {
	dir := appDir()
	_ = os.MkdirAll(dir, 0o755)
	p := filepath.Join(dir, "tvcast.log")
	if st, err := os.Stat(p); err == nil && st.Size() > 5<<20 {
		_ = os.Rename(p, p+".1")
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err == nil {
		log.SetOutput(io.MultiWriter(f, os.Stderr))
	}
	log.SetFlags(log.LstdFlags)
}
