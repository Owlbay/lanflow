package core

import (
	"bytes"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// logMaxSize is the size at which the core log is rotated.
const logMaxSize = 5 << 20 // 5 MB

// logTailLines is how many trailing log lines are attached to exit errors.
const logTailLines = 20

// rotatingFile appends to a file and rotates it to "<name>.1" when it grows
// past maxSize, keeping exactly one old file. It is safe for concurrent
// writes (stdout and stderr of the child share one instance).
//
// Write never fails: it is the sink of the copy goroutines exec.Cmd runs
// for the child's stdout/stderr, and an error there ends the copy, closes
// the pipe and gets Mihomo killed by SIGPIPE on its next log line. A disk
// problem (full, rotation refused) therefore drops log lines instead; the
// first such error is kept for Err and reported once to the process log.
type rotatingFile struct {
	mu      sync.Mutex
	path    string
	maxSize int64
	f       *os.File
	size    int64
	closed  bool
	err     error // first write/rotation error, reported once
}

// openRotatingFile opens (creating when needed) path for appending.
func openRotatingFile(path string, maxSize int64) (*rotatingFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	r := &rotatingFile{path: path, maxSize: maxSize}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *rotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	r.f = f
	r.size = st.Size()
	return nil
}

// Write implements io.Writer and always reports len(p), nil (see the type
// comment). A write that would push the file past maxSize triggers a
// rotation first, so a single file never exceeds maxSize unless one write
// alone is larger than that. After Close the data is discarded.
func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return len(p), nil
	}
	if r.f != nil && r.size > 0 && r.size+int64(len(p)) > r.maxSize {
		if err := r.rotateLocked(); err != nil {
			r.failLocked(err)
		}
	}
	if r.f == nil { // a failed rotation or reopen: try again now
		if err := r.open(); err != nil {
			r.failLocked(err)
			return len(p), nil
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	if err != nil {
		r.failLocked(err)
	}
	return len(p), nil
}

// failLocked records the first error and reports it once.
func (r *rotatingFile) failLocked(err error) {
	if r.err != nil {
		return
	}
	r.err = err
	log.Printf("core log %s: %v (further log lines may be dropped)", r.path, err)
}

// Err returns the first error a write or rotation hit, nil if none.
func (r *rotatingFile) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

// rotateLocked closes the file and moves it to "<name>.1". On return r.f is
// the reopened file, or nil when reopening failed.
func (r *rotatingFile) rotateLocked() error {
	cerr := r.f.Close()
	r.f = nil
	old := r.path + ".1"
	_ = os.Remove(old)
	if err := os.Rename(r.path, old); err != nil && !os.IsNotExist(err) {
		// keep appending to the oversized file rather than losing lines
		if oerr := r.open(); oerr != nil {
			return oerr
		}
		return err
	}
	if err := r.open(); err != nil {
		return err
	}
	return cerr
}

// Close closes the underlying file; later writes are discarded.
func (r *rotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}

// tailLines returns the last n non-empty lines of the file at path (at most
// the trailing 64 KB are read). Missing or unreadable files yield "".
func tailLines(path string, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	const window = 64 << 10
	start := st.Size() - window
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return ""
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	if start > 0 {
		// Drop the probably partial first line.
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		}
	}
	lines := strings.Split(strings.TrimRight(string(data), "\r\n"), "\n")
	out := make([]string, 0, n)
	for i := len(lines) - 1; i >= 0 && len(out) < n; i-- {
		l := strings.TrimRight(lines[i], "\r")
		if strings.TrimSpace(l) == "" {
			continue
		}
		out = append(out, l)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return strings.Join(out, "\n")
}
