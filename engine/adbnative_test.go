package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Live test against a real TV. Run with:
//
//	TVCAST_TEST_TV=192.168.1.3 TVCAST_ADB_KEY=~/.android/adbkey go test -run TestNativeADB -v
func TestNativeADB(t *testing.T) {
	ip := os.Getenv("TVCAST_TEST_TV")
	if ip == "" {
		t.Skip("set TVCAST_TEST_TV to a TV's IP to run")
	}
	if r := adbNative([]string{"connect", ip + ":5555"}, "", 20*time.Second); r.Code != 0 {
		t.Fatalf("connect: %v", r)
	}
	r := nativeShell(ip, "getprop ro.product.model", 10*time.Second)
	if r.Code != 0 || r.Out == "" {
		t.Fatalf("shell: %+v", r)
	}
	t.Logf("model=%q", r.Out)
	if r := nativeShell(ip, "ls /definitely/not/here", 10*time.Second); r.Code == 0 {
		t.Fatalf("expected non-zero exit, got %+v", r)
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "up.txt")
	payload := strings.Repeat("tvcast-native-adb ", 20000) // ~360 KB: several sync chunks
	_ = os.WriteFile(src, []byte(payload), 0o600)
	if err := nativePush(ip, src, "/data/local/tmp/tvcast-test.txt"); err != nil {
		t.Fatalf("push: %v", err)
	}
	dst := filepath.Join(dir, "down.txt")
	if err := nativePull(ip, "/data/local/tmp/tvcast-test.txt", dst); err != nil {
		t.Fatalf("pull: %v", err)
	}
	if b, _ := os.ReadFile(dst); string(b) != payload {
		t.Fatalf("round trip mismatch: %d vs %d bytes", len(b), len(payload))
	}
	nativeShell(ip, "rm -f /data/local/tmp/tvcast-test.txt", 10*time.Second)
	big := nativeShell(ip, "cat /proc/cpuinfo /proc/meminfo /proc/cpuinfo /proc/meminfo", 10*time.Second)
	if big.Code != 0 || len(big.Out) < 1000 {
		t.Fatalf("large output: %d bytes code %d", len(big.Out), big.Code)
	}
	t.Logf("push/pull %d bytes OK; large shell output %d bytes OK", len(payload), len(big.Out))
}

func TestADBPublicKeyFormat(t *testing.T) {
	k, pub := adbKeys()
	if k == nil || len(pub) < 500 || !strings.HasSuffix(string(pub), "\x00") {
		t.Fatalf("bad public key encoding (%d bytes)", len(pub))
	}
}
