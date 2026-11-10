package fsx

import (
	"bytes"
	"errors"
	"io"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestWriteFileReplacesAndCleansUp(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "a.json")
	if err := WriteFile(p, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(p, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "two" {
		t.Fatalf("content %q", got)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 && os.PathSeparator == '/' {
		t.Fatalf("mode %v", fi.Mode())
	}
	ents, _ := os.ReadDir(filepath.Dir(p))
	if len(ents) != 1 {
		t.Fatalf("leftover temp files: %v", ents)
	}
}

// Concurrent writers must never interleave or leave a partial file: every
// read sees one complete version.
func TestWriteFileConcurrent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x")
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			data := bytes.Repeat([]byte(fmt.Sprintf("%02d", i)), 4096)
			for j := 0; j < 10; j++ {
				if err := WriteFile(p, data, 0o600); err != nil {
					t.Error(err)
					return
				}
				got, err := os.ReadFile(p)
				if err != nil {
					t.Error(err)
					return
				}
				if len(got) != 8192 || !bytes.Equal(got, bytes.Repeat(got[:2], 4096)) {
					t.Errorf("torn file (%d bytes)", len(got))
					return
				}
			}
		}(i)
	}
	wg.Wait()
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("leftover temp files: %d entries", len(ents))
	}
}

func TestWriteFailingFillLeavesOriginal(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x")
	_ = WriteFile(p, []byte("keep"), 0o600)
	err := Write(p, 0o600, func(w io.Writer) error {
		_, _ = w.Write([]byte("partial"))
		return errors.New("boom")
	})
	if err == nil {
		t.Fatal("want error")
	}
	got, _ := os.ReadFile(p)
	ents, _ := os.ReadDir(dir)
	if string(got) != "keep" || len(ents) != 1 {
		t.Fatalf("content %q, %d entries", got, len(ents))
	}
}
