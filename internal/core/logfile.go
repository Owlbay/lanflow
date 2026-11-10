package core

import (
	"bytes"
	"io"
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
type rotatingFile struct {
	mu      sync.Mutex
	path    string
	maxSize int64
	f       *os.File
	size    int64
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

// Write implements io.Writer. A write that would push the file past maxSize
// triggers a rotation first, so a single file never exceeds maxSize unless
// one write alone is larger than that.
func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return 0, os.ErrClosed
	}
	if r.size > 0 && r.size+int64(len(p)) > r.maxSize {
		if err := r.rotateLocked(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *rotatingFile) rotateLocked() error {
	if err := r.f.Close(); err != nil {
		return err
	}
	r.f = nil
	old := r.path + ".1"
	_ = os.Remove(old)
	if err := os.Rename(r.path, old); err != nil && !os.IsNotExist(err) {
		return err
	}
	return r.open()
}

// Close closes the underlying file; further writes fail.
func (r *rotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
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
