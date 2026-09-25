package uploader

import (
	"context"
	"errors"
	"io"
	"testing"
)

func TestUploadStreamRejectsInvalidInputBeforeClientUse(t *testing.T) {
	cases := []struct {
		name     string
		fileSize int64
		provider PartReaderFunc
	}{
		{name: "negative size", fileSize: -1, provider: func(context.Context, int64, int64) (io.Reader, error) { return nil, nil }},
		{name: "nil provider", fileSize: 1, provider: nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := UploadStream(context.Background(), nil, "file.bin", tc.fileSize, "0", tc.provider, UploadOptions{}); err == nil {
				t.Fatal("expected invalid input to be rejected")
			}
		})
	}
}

func TestUploadStreamRejectsNilContext(t *testing.T) {
	if _, err := UploadStream(nil, nil, "file.bin", 1, "0", func(context.Context, int64, int64) (io.Reader, error) {
		return nil, nil
	}, UploadOptions{}); err == nil {
		t.Fatal("expected nil context to be rejected")
	}
}

func TestCleanupPartialUploadDeletesReturnedFID(t *testing.T) {
	var deleted string
	err := cleanupPartialUpload("partial-fid", func(fid string) error {
		deleted = fid
		return nil
	})
	if err != nil {
		t.Fatalf("cleanupPartialUpload() error = %v", err)
	}
	if deleted != "partial-fid" {
		t.Fatalf("deleted fid = %q, want %q", deleted, "partial-fid")
	}
}

func TestCleanupPartialUploadReportsDeleteFailure(t *testing.T) {
	wantErr := io.ErrClosedPipe
	err := cleanupPartialUpload("partial-fid", func(string) error { return wantErr })
	if err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("cleanupPartialUpload() error = %v, want wrapped %v", err, wantErr)
	}
}
