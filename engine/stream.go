package main

import (
	"io"
	"net/http"
	"os"
	"strconv"
	"time"
)

// A TV that stops reading without hanging up would otherwise hold a stream open forever.
// Paused Kodi also stops reading (its buffer is full); when the stream is dropped it simply
// reconnects on resume (verified on Fire TV), so a dropped paused stream is harmless.
var stallTimeout = func() time.Duration {
	if d, err := time.ParseDuration(os.Getenv("TVCAST_STALL")); err == nil && d > 0 {
		return d // tests only
	}
	return 30 * time.Minute
}()

const maxStreams = 64 // a TV uses 1-3 at a time

var streamSlots = make(chan struct{}, maxStreams)

// progressWriter feeds the response in 4 MB sendfile chunks and pushes the write deadline
// forward after each one, so a long movie never times out but a dead reader does.
type progressWriter struct {
	http.ResponseWriter
	rc *http.ResponseController
}

func (p progressWriter) ReadFrom(r io.Reader) (int64, error) {
	src, remain := r, int64(-1)
	if lr, ok := r.(*io.LimitedReader); ok { // keep the *os.File visible to sendfile
		src, remain = lr.R, lr.N
	}
	var total int64
	for remain != 0 {
		chunk := int64(4 << 20)
		if remain > 0 && remain < chunk {
			chunk = remain
		}
		_ = p.rc.SetWriteDeadline(time.Now().Add(stallTimeout))
		n, err := io.Copy(p.ResponseWriter, io.LimitReader(src, chunk))
		total += n
		if remain > 0 {
			remain -= n
		}
		if n > 0 {
			markActive()
		}
		if err != nil {
			return total, err
		}
		if n < chunk { // end of file
			break
		}
	}
	if lr, ok := r.(*io.LimitedReader); ok {
		lr.N -= total
	}
	_ = p.rc.SetWriteDeadline(time.Time{})
	return total, nil
}

func acquireStream(w http.ResponseWriter) bool {
	select {
	case streamSlots <- struct{}{}:
		return true
	default:
		w.Header().Set("Retry-After", strconv.Itoa(5))
		w.WriteHeader(http.StatusServiceUnavailable)
		return false
	}
}

func releaseStream() { <-streamSlots }
