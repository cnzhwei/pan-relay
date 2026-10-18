package ctyun

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestInterruptedDownloadIsNotMistakenForComplete is the regression test for
// the silent-corruption defect: an interrupted chunked download used to leave a
// preallocated file at the final path and size, so the next run short-circuited
// on size alone and reported success over all-zero bytes.
//
// Required behaviour after the fix:
//  1. a failed download must not create the final output path,
//  2. it must leave a distinguishable ".part" file,
//  3. a subsequent run must actually fetch the data (not trust the residue).
func TestInterruptedDownloadIsNotMistakenForComplete(t *testing.T) {
	const total = int64(8 << 20) // 8 MiB, one chunk

	var healthy bool
	var rangedFetches int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "bytes=0-0" {
			w.Header().Set("Content-Range", "bytes 0-0/8388608")
			w.Header().Set("Content-Length", "1")
			w.WriteHeader(http.StatusPartialContent)
			w.Write([]byte{0})
			return
		}
		if !healthy {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		rangedFetches++
		w.Header().Set("Content-Range", "bytes 0-8388607/8388608")
		w.WriteHeader(http.StatusPartialContent)
		// Non-zero payload so a stale sparse file is detectable.
		body := make([]byte, total)
		for i := range body {
			body[i] = 0xAB
		}
		w.Write(body)
	}))
	defer srv.Close()

	out := filepath.Join(t.TempDir(), "interrupted.bin")
	ctx := context.Background()

	// --- attempt 1: server fails ---
	err := Download(ctx, srv.URL, nil, out, DownloadOptions{Concurrency: 2, Retries: 0})
	if err == nil {
		t.Fatal("first download should have failed")
	}

	if _, statErr := os.Stat(out); statErr == nil {
		t.Fatal("REGRESSION: a failed download must not leave the final output path")
	}
	partInfo, partErr := os.Stat(out + ".part")
	if partErr != nil {
		t.Fatalf("expected a .part residue for a resumed retry: %v", partErr)
	}
	t.Logf("residual .part size = %d", partInfo.Size())

	// --- attempt 2: server healthy, must really fetch ---
	healthy = true
	if err := Download(ctx, srv.URL, nil, out, DownloadOptions{Concurrency: 2, Retries: 0}); err != nil {
		t.Fatalf("second download failed: %v", err)
	}
	if rangedFetches == 0 {
		t.Fatal("REGRESSION: second run reported success without fetching any range")
	}

	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("reading result: %v", err)
	}
	if int64(len(b)) != total {
		t.Fatalf("result size = %d, want %d", len(b), total)
	}
	for i, c := range b {
		if c != 0xAB {
			t.Fatalf("result byte %d = 0x%02X, want 0xAB (stale/sparse data)", i, c)
		}
	}
	if _, statErr := os.Stat(out + ".part"); statErr == nil {
		t.Error("the .part file should be renamed away on success")
	}
}

// TestCompletedDownloadShortCircuits verifies the size check still works for a
// genuinely finished file, so retries stay cheap.
func TestCompletedDownloadShortCircuits(t *testing.T) {
	const total = int64(1 << 20)

	var fetches int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "bytes=0-0" {
			w.Header().Set("Content-Range", "bytes 0-0/1048576")
			w.Header().Set("Content-Length", "1")
			w.WriteHeader(http.StatusPartialContent)
			w.Write([]byte{0})
			return
		}
		fetches++ // count only real payload fetches, not probes
		w.Header().Set("Content-Range", "bytes 0-1048575/1048576")
		w.WriteHeader(http.StatusPartialContent)
		w.Write(make([]byte, total))
	}))
	defer srv.Close()

	out := filepath.Join(t.TempDir(), "done.bin")
	ctx := context.Background()

	if err := Download(ctx, srv.URL, nil, out, DownloadOptions{Concurrency: 2, Retries: 0}); err != nil {
		t.Fatalf("first download: %v", err)
	}
	before := fetches

	if err := Download(ctx, srv.URL, nil, out, DownloadOptions{Concurrency: 2, Retries: 0}); err != nil {
		t.Fatalf("second download: %v", err)
	}
	if fetches != before {
		t.Errorf("a completed download should not refetch data (fetches %d -> %d)", before, fetches)
	}
}
