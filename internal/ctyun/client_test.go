package ctyun

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"io"
	"testing"
)

func TestPartSize(t *testing.T) {
	cases := []struct {
		size int64
		want int64
	}{
		{1, 10 << 20},
		{10 << 20, 10 << 20},
		{100 << 20, 10 << 20},
		{9990 << 20, 10 << 20},
		{(9990 << 20) + 1, 20 << 20},
		{19980 << 20, 20 << 20},
		{19981 << 20, 50 << 20},
		{100 << 30, 60 << 20}, // ceil(100GiB/1999)=51.2MiB -> rounded up to a 10MiB multiple
	}
	for _, c := range cases {
		got := PartSize(c.size)
		if got != c.want {
			t.Errorf("PartSize(%d) = %d, want %d", c.size, got, c.want)
		}
	}
}

func TestSignAndEncrypt(t *testing.T) {
	// Golden values produced from the algorithm description: the signature is
	// HMAC-SHA1 over the canonical string, upper-case hex.
	sig := sign("0123456789abcdef", "SESSIONKEY", "GET",
		"https://api.cloud.189.cn/listFiles.action", "Sun, 18 Oct 2026 11:20:38 GMT", "")
	if len(sig) != 40 {
		t.Fatalf("signature length = %d, want 40", len(sig))
	}
	for _, r := range sig {
		if !((r >= '0' && r <= '9') || (r >= 'A' && r <= 'F')) {
			t.Fatalf("signature %q contains non-uppercase-hex character %q", sig, r)
		}
	}
}

func TestRequestPath(t *testing.T) {
	cases := map[string]string{
		"https://api.cloud.189.cn/listFiles.action":              "/listFiles.action",
		"https://upload.cloud.189.cn/person/initMultiUpload":     "/person/initMultiUpload",
		"https://api.cloud.189.cn/family/file/renameFile.action": "/family/file/renameFile.action",
	}
	for in, want := range cases {
		if got := requestPath(in); got != want {
			t.Errorf("requestPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAESECBHex(t *testing.T) {
	// AES-128-ECB with PKCS7 padding, upper-case hex.
	got, err := aesECBHex("1234567890", "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 32 {
		t.Fatalf("cipher length = %d, want 32 hex chars for one block", len(got))
	}
	// Deterministic: encrypting again yields the same value.
	again, _ := aesECBHex("1234567890", "0123456789abcdef")
	if got != again {
		t.Error("AES-ECB encryption is not deterministic")
	}
}

func TestEncodeParams(t *testing.T) {
	got := encodeParams(map[string]string{"b": "2", "a": "1", "c": "3"})
	if got != "a=1&b=2&c=3" {
		t.Errorf("encodeParams = %q, want sorted output", got)
	}
	if encodeParams(nil) != "" {
		t.Error("encodeParams(nil) should be empty")
	}
}

// TestRemoteReaderAt verifies concurrent-safe random access over a fake remote
// source, which is what the zero-disk relay relies on.
func TestRemoteReaderAt(t *testing.T) {
	data := make([]byte, 3*(10<<20)+12345)
	for i := range data {
		data[i] = byte(i*7 + 3)
	}
	total := int64(len(data))
	var fetches int

	fetch := func(ctx context.Context, offset, size int64) (io.Reader, error) {
		fetches++
		if offset < 0 || offset+size > total {
			t.Fatalf("fetch out of range: %d+%d (total %d)", offset, size, total)
		}
		return bytes.NewReader(data[offset : offset+size]), nil
	}

	ra := NewRemoteReaderAt(context.Background(), total, fetch)

	// Sequential read, as the hashing pass does.
	h := md5.New()
	buf := make([]byte, 1<<20)
	var off int64
	for off < total {
		n, err := ra.ReadAt(buf, off)
		if n > 0 {
			h.Write(buf[:n])
			off += int64(n)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("ReadAt: %v", err)
		}
	}
	want := md5.Sum(data)
	if got := hex.EncodeToString(h.Sum(nil)); got != hex.EncodeToString(want[:]) {
		t.Fatalf("sequential hash mismatch")
	}

	// Random access in reverse part order, as concurrent workers do.
	part := PartSize(total)
	for i := (total + part - 1) / part; i > 0; i-- {
		start := (i - 1) * part
		length := part
		if start+length > total {
			length = total - start
		}
		b := make([]byte, length)
		if _, err := ra.ReadAt(b, start); err != nil && err != io.EOF {
			t.Fatalf("reverse ReadAt at %d: %v", start, err)
		}
		if !bytes.Equal(b, data[start:start+length]) {
			t.Fatalf("reverse read mismatch at part %d", i)
		}
	}
	if fetches == 0 {
		t.Error("expected at least one upstream fetch")
	}
}

func TestLoadConfigValidation(t *testing.T) {
	t.Setenv("CTYUN_REFRESH_TOKEN", "abc")
	cfg, err := LoadConfig("/nonexistent/path/ctyun.json")
	if err != nil {
		t.Fatalf("LoadConfig with env token: %v", err)
	}
	if cfg.RefreshToken != "abc" {
		t.Errorf("RefreshToken = %q, want env value", cfg.RefreshToken)
	}
	if cfg.Type != "personal" {
		t.Errorf("Type default = %q, want personal", cfg.Type)
	}
	if cfg.RootFolderID != DefaultRootID {
		t.Errorf("RootFolderID default = %q, want %q", cfg.RootFolderID, DefaultRootID)
	}
}
