package main

// Native ADB-over-TCP client. Talks the ADB wire protocol directly to the TV (port 5555),
// so no adb binary is needed. Used on Android (phones have no adb tool) and on desktop
// when TVCAST_NATIVE_ADB=1. Every operation uses its own short-lived connection, which
// keeps the client stateless and immune to stale/broken sessions.

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	aCNXN = 0x4e584e43
	aAUTH = 0x48545541
	aOPEN = 0x4e45504f
	aOKAY = 0x59414b4f
	aCLSE = 0x45534c43
	aWRTE = 0x45545257
	aSTLS = 0x534c5453

	adbProtoVersion = 0x01000001
	adbMaxPayload   = 256 * 1024

	authToken     = 1
	authSignature = 2
	authPublicKey = 3
)

var useNativeADB = isAndroidBuild || os.Getenv("TVCAST_NATIVE_ADB") == "1"

var errUnauthorized = errors.New("unauthorized")

// ---------------------------------------------------------------- wire format

func adbWrite(w io.Writer, cmd, a0, a1 uint32, data []byte) error {
	buf := make([]byte, 24+len(data))
	le := binary.LittleEndian
	le.PutUint32(buf[0:], cmd)
	le.PutUint32(buf[4:], a0)
	le.PutUint32(buf[8:], a1)
	le.PutUint32(buf[12:], uint32(len(data)))
	var sum uint32
	for _, b := range data {
		sum += uint32(b)
	}
	le.PutUint32(buf[16:], sum)
	le.PutUint32(buf[20:], cmd^0xffffffff)
	copy(buf[24:], data)
	_, err := w.Write(buf)
	return err
}

type adbMsg struct {
	cmd, arg0, arg1 uint32
	data            []byte
}

func adbRead(r io.Reader) (adbMsg, error) {
	var h [24]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return adbMsg{}, err
	}
	le := binary.LittleEndian
	m := adbMsg{cmd: le.Uint32(h[0:]), arg0: le.Uint32(h[4:]), arg1: le.Uint32(h[8:])}
	if le.Uint32(h[20:]) != m.cmd^0xffffffff {
		return m, errors.New("adb: corrupt message")
	}
	n := le.Uint32(h[12:])
	if n > 1<<20 {
		return m, errors.New("adb: message too large")
	}
	m.data = make([]byte, n)
	_, err := io.ReadFull(r, m.data)
	return m, err
}

// ---------------------------------------------------------------- RSA key

var (
	keyOnce sync.Once
	privKey *rsa.PrivateKey
	pubKey  []byte
)

func adbKeys() (*rsa.PrivateKey, []byte) {
	keyOnce.Do(func() {
		path := os.Getenv("TVCAST_ADB_KEY")
		if path == "" {
			path = filepath.Join(appDir(), "adbkey")
		}
		if b, err := os.ReadFile(path); err == nil {
			if blk, _ := pem.Decode(b); blk != nil {
				if k, err := x509.ParsePKCS1PrivateKey(blk.Bytes); err == nil {
					privKey = k
				} else if k8, err := x509.ParsePKCS8PrivateKey(blk.Bytes); err == nil {
					privKey, _ = k8.(*rsa.PrivateKey)
				}
			}
		}
		if privKey == nil {
			k, err := rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				log.Printf("adb key generation failed: %v", err)
				return
			}
			privKey = k
			_ = os.MkdirAll(filepath.Dir(path), 0o700)
			_ = os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY",
				Bytes: x509.MarshalPKCS1PrivateKey(k)}), 0o600)
		}
		pubKey = encodeADBPublicKey(&privKey.PublicKey)
	})
	return privKey, pubKey
}

// encodeADBPublicKey produces Android's RSAPublicKey struct (base64) + " name\0".
func encodeADBPublicKey(k *rsa.PublicKey) []byte {
	const words = 64 // 2048-bit
	le := binary.LittleEndian
	var buf bytes.Buffer
	_ = binary.Write(&buf, le, uint32(words))
	r32 := new(big.Int).Lsh(big.NewInt(1), 32)
	n0 := new(big.Int).Mod(k.N, r32)
	inv := new(big.Int).ModInverse(n0, r32)
	n0inv := new(big.Int).Mod(new(big.Int).Sub(r32, inv), r32)
	_ = binary.Write(&buf, le, uint32(n0inv.Uint64()))
	writeLE := func(x *big.Int) {
		b := x.FillBytes(make([]byte, words*4)) // big-endian
		for i := len(b) - 1; i >= 0; i-- {
			buf.WriteByte(b[i])
		}
	}
	writeLE(k.N)
	rr := new(big.Int).Exp(big.NewInt(2), big.NewInt(words*32*2), k.N)
	writeLE(rr)
	_ = binary.Write(&buf, le, uint32(k.E))
	host, _ := os.Hostname()
	if host == "" || host == "localhost" {
		host = "phone"
	}
	return []byte(base64.StdEncoding.EncodeToString(buf.Bytes()) + " TVCast@" + host + "\x00")
}

// ---------------------------------------------------------------- device state

var (
	nstateMu sync.Mutex
	nstate   = map[string]string{}   // ip -> "device" | "unauthorized" | "offline"
	pending  = map[string]net.Conn{} // ip -> connection waiting for the user to accept on the TV
)

func setNState(ip, s string) {
	nstateMu.Lock()
	if s == "" {
		delete(nstate, ip)
	} else {
		nstate[ip] = s
	}
	nstateMu.Unlock()
}

// ---------------------------------------------------------------- connection

type adbClient struct {
	conn    net.Conn
	maxData int
}

// adbDial connects and authenticates. If the TV doesn't know our key yet and
// authWait > 0, it offers the key (TV shows "Allow USB debugging?") and waits.
func adbDial(ip string, authWait time.Duration) (*adbClient, error) {
	key, pub := adbKeys()
	if key == nil {
		return nil, errors.New("no adb key")
	}
	c, err := net.DialTimeout("tcp4", net.JoinHostPort(ip, "5555"), 5*time.Second)
	if err != nil {
		setNState(ip, "")
		return nil, err
	}
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
		_ = tc.SetKeepAlive(true)
	}
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if err := adbWrite(c, aCNXN, adbProtoVersion, adbMaxPayload, []byte("host::\x00")); err != nil {
		c.Close()
		return nil, err
	}
	sentSig, sentKey := false, false
	for {
		m, err := adbRead(c)
		if err != nil {
			c.Close()
			if sentKey {
				return nil, errUnauthorized
			}
			return nil, err
		}
		switch m.cmd {
		case aCNXN:
			_ = c.SetDeadline(time.Time{})
			setNState(ip, "device")
			md := int(m.arg1)
			if md <= 0 || md > adbMaxPayload {
				md = 4096
			}
			return &adbClient{conn: c, maxData: md}, nil
		case aSTLS:
			c.Close()
			return nil, errors.New("this TV needs Wireless-debugging pairing; turn on classic ADB/USB debugging instead")
		case aAUTH:
			if m.arg0 != authToken {
				continue
			}
			if !sentSig {
				sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA1, m.data)
				if err != nil {
					c.Close()
					return nil, err
				}
				sentSig = true
				if err := adbWrite(c, aAUTH, authSignature, 0, sig); err != nil {
					c.Close()
					return nil, err
				}
				continue
			}
			if !sentKey && authWait > 0 {
				sentKey = true
				setNState(ip, "unauthorized")
				nstateMu.Lock()
				pending[ip] = c
				nstateMu.Unlock()
				_ = c.SetDeadline(time.Now().Add(authWait))
				if err := adbWrite(c, aAUTH, authPublicKey, 0, pub); err != nil {
					c.Close()
					return nil, err
				}
				continue
			}
			c.Close()
			setNState(ip, "unauthorized")
			return nil, errUnauthorized
		}
	}
}

func (a *adbClient) Close() { _ = a.conn.Close() }

type adbStream struct {
	a             *adbClient
	wmu           sync.Mutex
	local, remote uint32
	data          chan []byte
	acks          chan struct{}
	done          chan struct{}
	rbuf          []byte
	closeOnce     sync.Once
}

func (a *adbClient) open(service string) (*adbStream, error) {
	const local = 1
	_ = a.conn.SetDeadline(time.Now().Add(15 * time.Second))
	if err := adbWrite(a.conn, aOPEN, local, 0, []byte(service+"\x00")); err != nil {
		return nil, err
	}
	for {
		m, err := adbRead(a.conn)
		if err != nil {
			return nil, err
		}
		switch m.cmd {
		case aOKAY:
			_ = a.conn.SetDeadline(time.Time{})
			s := &adbStream{a: a, local: local, remote: m.arg0, data: make(chan []byte, 32),
				acks: make(chan struct{}, 1), done: make(chan struct{})}
			go s.reader()
			return s, nil
		case aCLSE:
			return nil, fmt.Errorf("TV refused %q", strings.SplitN(service, ":", 2)[0])
		}
	}
}

func (s *adbStream) send(cmd uint32, data []byte) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return adbWrite(s.a.conn, cmd, s.local, s.remote, data)
}

func (s *adbStream) reader() {
	defer s.closeOnce.Do(func() { close(s.done) })
	for {
		m, err := adbRead(s.a.conn)
		if err != nil {
			return
		}
		switch m.cmd {
		case aWRTE:
			select {
			case s.data <- m.data:
			case <-time.After(5 * time.Minute):
				return
			}
			if s.send(aOKAY, nil) != nil {
				return
			}
		case aOKAY:
			select {
			case s.acks <- struct{}{}:
			default:
			}
		case aCLSE:
			_ = s.send(aCLSE, nil)
			return
		}
	}
}

func (s *adbStream) Read(p []byte) (int, error) {
	for len(s.rbuf) == 0 {
		select {
		case b := <-s.data:
			s.rbuf = b
		case <-s.done:
			select {
			case b := <-s.data:
				s.rbuf = b
			default:
				return 0, io.EOF
			}
		}
	}
	n := copy(p, s.rbuf)
	s.rbuf = s.rbuf[n:]
	return n, nil
}

func (s *adbStream) Write(p []byte) (int, error) {
	total := 0
	for len(p) > 0 {
		n := len(p)
		if n > s.a.maxData {
			n = s.a.maxData
		}
		if err := s.send(aWRTE, p[:n]); err != nil {
			return total, err
		}
		select {
		case <-s.acks:
		case <-s.done:
			return total, io.ErrClosedPipe
		case <-time.After(60 * time.Second):
			return total, errors.New("adb: write timed out")
		}
		total += n
		p = p[n:]
	}
	return total, nil
}

func (s *adbStream) Close() error {
	_ = s.send(aCLSE, nil)
	s.a.Close()
	return nil
}

// ---------------------------------------------------------------- operations

func nativeShell(ip, cmd string, timeout time.Duration) ProcResult {
	a, err := adbDial(ip, 0)
	if err != nil {
		return ProcResult{-1, "error: " + err.Error()}
	}
	defer a.Close()
	s, err := a.open("shell:" + cmd + " ; echo __TVCAST_RC=$?")
	if err != nil {
		return ProcResult{-1, "error: " + err.Error()}
	}
	_ = a.conn.SetReadDeadline(time.Now().Add(timeout))
	out, _ := io.ReadAll(io.LimitReader(s, 16<<20))
	text := strings.ReplaceAll(string(out), "\r\n", "\n")
	code := -2 // no exit marker = timed out / cut off
	if i := strings.LastIndex(text, "__TVCAST_RC="); i >= 0 {
		rc := strings.TrimSpace(text[i+len("__TVCAST_RC="):])
		if n, err := strconv.Atoi(rc); err == nil {
			code = n
		}
		text = text[:i]
	}
	return ProcResult{code, strings.TrimSpace(text)}
}

func syncHeader(id string, n int) []byte {
	b := make([]byte, 8)
	copy(b, id)
	binary.LittleEndian.PutUint32(b[4:], uint32(n))
	return b
}

func nativePull(ip, remote, local string) error {
	a, err := adbDial(ip, 0)
	if err != nil {
		return err
	}
	defer a.Close()
	s, err := a.open("sync:")
	if err != nil {
		return err
	}
	_ = a.conn.SetDeadline(time.Now().Add(2 * time.Minute))
	if _, err := s.Write(append(syncHeader("RECV", len(remote)), remote...)); err != nil {
		return err
	}
	var out bytes.Buffer
	h := make([]byte, 8)
	for {
		if _, err := io.ReadFull(s, h); err != nil {
			return err
		}
		n := int(binary.LittleEndian.Uint32(h[4:]))
		switch string(h[:4]) {
		case "DATA":
			if n > 1<<20 {
				return errors.New("adb: bad sync chunk")
			}
			if _, err := io.CopyN(&out, s, int64(n)); err != nil {
				return err
			}
			if out.Len() > 256<<20 {
				return errors.New("adb: file too large")
			}
		case "DONE":
			return os.WriteFile(local, out.Bytes(), 0o600)
		case "FAIL":
			msg := make([]byte, n)
			_, _ = io.ReadFull(s, msg)
			return errors.New(string(msg))
		default:
			return errors.New("adb: unexpected sync reply")
		}
	}
}

func nativePush(ip, local, remote string) error {
	f, err := os.Open(local)
	if err != nil {
		return err
	}
	defer f.Close()
	a, err := adbDial(ip, 0)
	if err != nil {
		return err
	}
	defer a.Close()
	s, err := a.open("sync:")
	if err != nil {
		return err
	}
	_ = a.conn.SetDeadline(time.Now().Add(20 * time.Minute))
	spec := remote + ",33188" // 0100644
	if _, err := s.Write(append(syncHeader("SEND", len(spec)), spec...)); err != nil {
		return err
	}
	buf := make([]byte, 64*1024)
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			if _, err := s.Write(append(syncHeader("DATA", n), buf[:n]...)); err != nil {
				return err
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	if _, err := s.Write(syncHeader("DONE", int(time.Now().Unix()))); err != nil {
		return err
	}
	h := make([]byte, 8)
	if _, err := io.ReadFull(s, h); err != nil {
		return err
	}
	if string(h[:4]) == "OKAY" {
		return nil
	}
	n := int(binary.LittleEndian.Uint32(h[4:]))
	if n > 4096 {
		n = 4096
	}
	msg := make([]byte, n)
	_, _ = io.ReadFull(s, msg)
	return errors.New("push failed: " + string(msg))
}

// ---------------------------------------------------------------- port forwarding

var (
	fwdMu sync.Mutex
	fwds  = map[int]net.Listener{}
)

func nativeForward(ip, remoteSpec string) (int, error) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	fwdMu.Lock()
	fwds[port] = ln
	fwdMu.Unlock()
	go func() {
		for {
			lc, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer lc.Close()
				a, err := adbDial(ip, 0)
				if err != nil {
					return
				}
				s, err := a.open(remoteSpec)
				if err != nil {
					a.Close()
					return
				}
				defer s.Close()
				go func() { _, _ = io.Copy(s, lc); _ = s.send(aCLSE, nil) }()
				_, _ = io.Copy(lc, s)
			}()
		}
	}()
	return port, nil
}

func nativeForwardRemove(port int) {
	fwdMu.Lock()
	if ln := fwds[port]; ln != nil {
		_ = ln.Close()
		delete(fwds, port)
	}
	fwdMu.Unlock()
}

// ---------------------------------------------------------------- adb command emulation

func ipOfSerial(s string) string { return strings.TrimSuffix(s, ":5555") }

// adbNative accepts the same arguments as the adb tool for the commands TV Cast uses.
func adbNative(args []string, serial string, timeout time.Duration) ProcResult {
	if len(args) == 0 {
		return ProcResult{-1, "no command"}
	}
	ip := ipOfSerial(serial)
	switch args[0] {
	case "start-server", "kill-server":
		return ProcResult{0, ""}
	case "devices":
		var b strings.Builder
		b.WriteString("List of devices attached\n")
		nstateMu.Lock()
		for k, v := range nstate {
			fmt.Fprintf(&b, "%s:5555\t%s\n", k, v)
		}
		nstateMu.Unlock()
		return ProcResult{0, strings.TrimSpace(b.String())}
	case "disconnect":
		if len(args) > 1 {
			setNState(ipOfSerial(args[1]), "")
		}
		return ProcResult{0, "disconnected"}
	case "connect":
		if len(args) < 2 {
			return ProcResult{-1, "missing address"}
		}
		ip = ipOfSerial(args[1])
		return nativeConnect(ip)
	case "shell":
		if len(args) < 2 {
			return ProcResult{-1, "missing command"}
		}
		return nativeShell(ip, strings.Join(args[1:], " "), timeout)
	case "pull":
		if len(args) < 3 {
			return ProcResult{-1, "usage"}
		}
		if err := nativePull(ip, args[1], args[2]); err != nil {
			return ProcResult{1, err.Error()}
		}
		return ProcResult{0, "pulled"}
	case "push":
		if len(args) < 3 {
			return ProcResult{-1, "usage"}
		}
		if err := nativePush(ip, args[1], args[2]); err != nil {
			return ProcResult{1, err.Error()}
		}
		return ProcResult{0, "pushed"}
	case "forward":
		if len(args) >= 3 && args[1] == "--remove" {
			p, _ := strconv.Atoi(strings.TrimPrefix(args[2], "tcp:"))
			nativeForwardRemove(p)
			return ProcResult{0, ""}
		}
		if len(args) < 3 {
			return ProcResult{-1, "usage"}
		}
		port, err := nativeForward(ip, args[2])
		if err != nil {
			return ProcResult{1, err.Error()}
		}
		return ProcResult{0, strconv.Itoa(port)}
	case "install":
		apk := args[len(args)-1]
		tmp := "/data/local/tmp/tvcast-install.apk"
		if err := nativePush(ip, apk, tmp); err != nil {
			return ProcResult{1, err.Error()}
		}
		r := nativeShell(ip, "pm install -r "+tmp+" ; rm -f "+tmp, timeout)
		if !strings.Contains(r.Out, "Success") {
			r.Code = 1
		}
		return r
	}
	return ProcResult{-1, "unsupported adb command: " + args[0]}
}

// nativeConnect authenticates; if the TV must approve this phone, it shows the prompt
// and we keep waiting in the background (up to 2 minutes) for the user to press Allow.
func nativeConnect(ip string) ProcResult {
	a, err := adbDial(ip, 0)
	if err == nil {
		a.Close()
		return ProcResult{0, "connected to " + ip + ":5555"}
	}
	if !errors.Is(err, errUnauthorized) {
		return ProcResult{1, "failed to connect to " + ip + ":5555: " + err.Error()}
	}
	// Drop any older waiting attempt (its prompt may have been missed while the TV slept)
	// and offer the key again, so the TV shows a fresh "Allow USB debugging?" prompt.
	nstateMu.Lock()
	if old := pending[ip]; old != nil {
		_ = old.Close()
		delete(pending, ip)
	}
	nstateMu.Unlock()
	go func() {
		a, err := adbDial(ip, 2*time.Minute)
		nstateMu.Lock()
		if c := pending[ip]; c != nil && a != nil && c == a.conn {
			delete(pending, ip)
		}
		nstateMu.Unlock()
		if err == nil {
			a.Close()
			log.Printf("TV %s approved this device", ip)
		}
	}()
	time.Sleep(1500 * time.Millisecond)
	nstateMu.Lock()
	st := nstate[ip]
	nstateMu.Unlock()
	if st == "device" {
		return ProcResult{0, "connected to " + ip + ":5555"}
	}
	setNState(ip, "unauthorized")
	return ProcResult{1, "failed to authenticate to " + ip + ":5555"}
}
