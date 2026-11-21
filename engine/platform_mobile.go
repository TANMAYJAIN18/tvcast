//go:build android || tvandroid

package main

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Android: the app's Java service starts this engine, keeps the phone and Wi-Fi awake while
// /api/info reports "awake", and provides the window. adb is the built-in protocol client.
const isAndroidBuild = true

func init() {
	// Android has no /etc/resolv.conf; without this, Go's resolver can't look up
	// download hosts (Kodi updates). Use the network's DNS via public resolvers as fallback.
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: 4 * time.Second}
			var lastErr error
			for _, s := range []string{"1.1.1.1:53", "8.8.8.8:53", "9.9.9.9:53"} {
				c, err := d.DialContext(ctx, network, s)
				if err == nil {
					return c, nil
				}
				lastErr = err
			}
			return nil, lastErr
		},
	}
	if os.Getenv("SSL_CERT_DIR") == "" {
		_ = os.Setenv("SSL_CERT_DIR", "/system/etc/security/cacerts")
	}
}

func setAwake(on bool) {} // handled by the Android service (wake + Wi-Fi locks)

func lidGuardInstalled() bool { return true }

func installLidGuard() (bool, string) { return true, "Phones keep streaming with the screen off." }

const storage = "/storage/emulated/0"

func defaultFolders() []string {
	return []string{filepath.Join(storage, "Movies"), filepath.Join(storage, "Download"),
		filepath.Join(storage, "DCIM"), filepath.Join(storage, "Video"), filepath.Join(storage, "Videos")}
}

// extraVolumeRoots lists SD cards and USB drives (/storage/XXXX-XXXX).
func extraVolumeRoots() []string {
	var out []string
	ents, _ := os.ReadDir("/storage")
	for _, e := range ents {
		n := e.Name()
		if n == "emulated" || n == "self" || !e.IsDir() {
			continue
		}
		out = append(out, filepath.Join("/storage", n))
	}
	return out
}

func skipDirs() map[string]bool {
	return map[string]bool{filepath.Join(storage, "Android"): true}
}

func extraToolDirs() []string { return nil }

func hideWindow(cmd *exec.Cmd) {}

func openUI() {}

// Pickers are native on Android: the app passes the chosen path to /api/openfile.
func pickFolder() (string, bool) { return "", false }
func pickFile() (string, bool)   { return "", false }
