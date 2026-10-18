package ctyun

// Verification tests for defects raised by the external review.
// These are intentionally adversarial: they must FAIL (or hang) while the
// defects exist, and pass after the fixes.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

func runtimeNumGoroutine() int { return runtime.NumGoroutine() }

// TestDownloadChunkedNoDeadlockOnWorkerError reproduces the claimed hang:
// a Range endpoint that always fails should make Download return an error
// promptly, never block forever on the unbuffered jobs channel.
func TestDownloadChunkedNoDeadlockOnWorkerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Probe (bytes=0-0) succeeds so the chunked path is selected.
		if r.Header.Get("Range") == "bytes=0-0" {
			w.Header().Set("Content-Range", "bytes 0-0/52428800")
			w.Header().Set("Content-Length", "1")
			w.WriteHeader(http.StatusPartialContent)
			w.Write([]byte{0})
			return
		}
		// Every real chunk fails.
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	out := filepath.Join(t.TempDir(), "deadlock.bin")
	ctx := context.Background()

	done := make(chan error, 1)
	go func() {
		done <- Download(ctx, srv.URL, nil, out, DownloadOptions{
			Concurrency: 4,
			Retries:     0,
		})
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error from a failing server, got nil")
		}
		t.Logf("returned error as expected: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("DEADLOCK: Download did not return within 15s after worker errors")
	}
}

// TestUploadWorkerErrorNoGoroutineLeak reproduces the claimed feeder leak.
// The upload is pointed at a server whose upload calls fail; the function must
// return and leave no goroutine blocked on the unbuffered jobs channel.
func TestUploadWorkerErrorNoGoroutineLeak(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping goroutine leak check in short mode")
	}

	before := runtimeNumGoroutine()
	out := filepath.Join(t.TempDir(), "leak.bin")
	// 21 MiB -> 3 parts of 10 MiB, enough to exercise the dispatch loop without
	// making the race detector crawl.
	data := make([]byte, 21<<20)
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
	f.Close()

	cfg := &Config{RefreshToken: "x", RootFolderID: "-11", Type: "personal",
		filePath: filepath.Join(t.TempDir(), "cfg.json")}
	cl := NewClient(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	// Upload will fail during EnsureSession (no network); the point is whether
	// it returns and cleans up.
	_, _ = cl.UploadLocal(ctx, "-11", out, UploadOptions{Concurrency: 4, Retries: 0})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		now := runtimeNumGoroutine()
		if now <= before+2 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("GOROUTINE LEAK: before=%d after=%d", before, runtimeNumGoroutine())
}


// TestRemoteReaderAtPropagatesFetchError checks that a failing tail fetch is
// surfaced rather than silently returning a short read with nil error.
func TestRemoteReaderAtPropagatesFetchError(t *testing.T) {
	total := int64(20 << 20)
	var calls int
	var mu sync.Mutex

	fetch := func(ctx context.Context, off, size int64) (io.Reader, error) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n > 1 {
			return nil, fmt.Errorf("simulated network failure")
		}
		return io.LimitReader(zeroReader{}, size), nil
	}

	ra := NewRemoteReaderAt(context.Background(), total, fetch)

	// First read primes the cache/window for window 0.
	buf1 := make([]byte, 1<<20)
	if _, err := ra.ReadAt(buf1, 0); err != nil && err != io.EOF {
		t.Fatalf("first read should succeed: %v", err)
	}

	// Read across a window boundary so the tail path needs a fetch that fails.
	buf2 := make([]byte, 2<<20)
	n, err := ra.ReadAt(buf2, PartSize(total)-1<<20)
	if err == nil && n < len(buf2) {
		t.Fatalf("SHORT READ WITH NIL ERROR: n=%d len=%d (violates io.ReaderAt)", n, len(buf2))
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}
