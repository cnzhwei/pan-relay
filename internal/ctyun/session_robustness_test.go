package ctyun

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestRequestPathRoot checks the signature path for a bare-origin URL.
// The old regex returned "" here, which would break any root-path signature.
func TestRequestPathRoot(t *testing.T) {
	cases := map[string]string{
		"https://api.cloud.189.cn/":                             "/",
		"https://api.cloud.189.cn":                              "/",
		"https://api.cloud.189.cn/listFiles.action":             "/listFiles.action",
		"https://upload.cloud.189.cn/person/initMultiUpload":    "/person/initMultiUpload",
		"https://api.cloud.189.cn/family/file/renameFile.action": "/family/file/renameFile.action",
		"https://api.cloud.189.cn/listFiles.action?a=1&b=2":     "/listFiles.action",
	}
	for in, want := range cases {
		if got := requestPath(in); got != want {
			t.Errorf("requestPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDoRejectsShortSessionSecret ensures a malformed sessionSecret produces an
// error instead of panicking on the ss[:16] slice.
func TestDoRejectsShortSessionSecret(t *testing.T) {
	cfg := &Config{RefreshToken: "x", Type: "personal", RootFolderID: "-11"}
	cl := NewClient(cfg)

	cl.mu.Lock()
	cl.sessionKey = "KEY"
	cl.sessSecret = "short" // < 16 bytes
	cl.mu.Unlock()

	err := cl.doWithRetry(context.Background(), http.MethodGet,
		APIURL+"/listFiles.action", RequestOptions{Encrypted: map[string]string{"a": "b"}},
		nil, false)
	if err == nil {
		t.Fatal("expected an error for a short session secret")
	}
	t.Logf("got expected error: %v", err)
}

// TestFamilySessionPairIsUsed verifies the family cloud signature uses the
// family session pair rather than the personal one.
func TestFamilySessionPairIsUsed(t *testing.T) {
	var gotSessionKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSessionKey = r.Header.Get("SessionKey")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"res_code":0}`))
	}))
	defer srv.Close()

	cfg := &Config{RefreshToken: "x", Type: "family", FamilyID: "123"}
	cl := NewClient(cfg)
	cl.mu.Lock()
	cl.sessionKey, cl.sessSecret = "PERSONAL_KEY", "personal_secret_16b"
	cl.famKey, cl.famSecret = "FAMILY_KEY", "family_secret_16by"
	cl.mu.Unlock()

	if err := cl.doWithRetry(context.Background(), http.MethodGet,
		srv.URL+"/listFiles.action", RequestOptions{}, nil, false); err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if gotSessionKey != "FAMILY_KEY" {
		t.Errorf("family request used SessionKey %q, want FAMILY_KEY", gotSessionKey)
	}

	// A personal config must still use the personal pair.
	cfg2 := &Config{RefreshToken: "x", Type: "personal"}
	cl2 := NewClient(cfg2)
	cl2.mu.Lock()
	cl2.sessionKey, cl2.sessSecret = "PERSONAL_KEY", "personal_secret_16b"
	cl2.famKey, cl2.famSecret = "FAMILY_KEY", "family_secret_16by"
	cl2.mu.Unlock()

	gotSessionKey = ""
	if err := cl2.doWithRetry(context.Background(), http.MethodGet,
		srv.URL+"/listFiles.action", RequestOptions{}, nil, false); err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if gotSessionKey != "PERSONAL_KEY" {
		t.Errorf("personal request used SessionKey %q, want PERSONAL_KEY", gotSessionKey)
	}
}

// TestEnsureSessionConcurrentSingleRefresh proves the refresh chain is entered
// once even when many goroutines race to establish a session. The client is
// pointed at a local server so no real API traffic is generated.
func TestEnsureSessionConcurrentSingleRefresh(t *testing.T) {
	var refreshCalls int64
	var sessionCalls int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/getSessionForPC.action":
			atomic.AddInt64(&sessionCalls, 1)
			time.Sleep(20 * time.Millisecond) // widen the race window
			w.Write([]byte(`{"res_code":0,"sessionKey":"SK","sessionSecret":"0123456789abcdef","loginName":"u"}`))
		case r.URL.Path == "/api/oauth2/refreshToken.do":
			atomic.AddInt64(&refreshCalls, 1)
			w.Write([]byte(`{"result":0,"accessToken":"AT","refreshToken":"RT","expiresIn":604799}`))
		default:
			w.Write([]byte(`{"res_code":0}`))
		}
	}))
	defer srv.Close()

	cfg := &Config{RefreshToken: "seed", Type: "personal",
		filePath: filepath.Join(t.TempDir(), "cfg.json")}
	cl := NewClient(cfg)
	cl.SetEndpoints(srv.URL, srv.URL, srv.URL)

	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, 32)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := cl.EnsureSession(context.Background()); err != nil {
				errs <- err
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("EnsureSession: %v", err)
	}

	if got := atomic.LoadInt64(&refreshCalls); got != 1 {
		t.Errorf("refresh endpoint called %d times, want exactly 1 (single-active token)", got)
	}
	if got := atomic.LoadInt64(&sessionCalls); got != 1 {
		t.Errorf("session endpoint called %d times, want exactly 1", got)
	}
}

// TestRemoteReaderAtConcurrentReads exercises the LRU cache and per-window
// single-flight under -race, verifying content correctness.
func TestRemoteReaderAtConcurrentReads(t *testing.T) {
	const total = int64(40 << 20)
	src := make([]byte, total)
	for i := range src {
		src[i] = byte(i % 251)
	}

	var fetches int64
	var mu sync.Mutex
	fetch := func(ctx context.Context, off, size int64) (io.Reader, error) {
		atomic.AddInt64(&fetches, 1)
		mu.Lock()
		defer mu.Unlock()
		if off < 0 || off+size > total {
			t.Fatalf("fetch out of range: %d+%d", off, size)
		}
		return bytes.NewReader(src[off : off+size]), nil
	}

	ra := NewRemoteReaderAt(context.Background(), total, fetch)
	part := PartSize(total)

	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			// Each worker reads a distinct part, in a rotated order, twice.
			for round := 0; round < 2; round++ {
				idx := (w + round) % int((total+part-1)/part)
				start := int64(idx) * part
				length := part
				if start+length > total {
					length = total - start
				}
				buf := make([]byte, length)
				if _, err := ra.ReadAt(buf, start); err != nil && err != io.EOF {
					t.Errorf("worker %d ReadAt: %v", w, err)
					return
				}
				if !bytes.Equal(buf, src[start:start+length]) {
					t.Errorf("worker %d content mismatch at part %d", w, idx)
					return
				}
			}
		}(w)
	}
	wg.Wait()

	t.Logf("upstream fetches = %d for %d parts", atomic.LoadInt64(&fetches), (total+part-1)/part)
	if atomic.LoadInt64(&fetches) == 0 {
		t.Error("expected upstream fetches")
	}
	_ = time.Now
}
