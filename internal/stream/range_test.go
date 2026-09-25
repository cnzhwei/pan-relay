package stream

import (
	"bytes"
	"io"
	"net/http"
	"testing"
)

func makeRangeResponse(status int, contentRange string, body string) *http.Response {
	resp := &http.Response{
		StatusCode:    status,
		Header:        make(http.Header),
		Body:          io.NopCloser(bytes.NewBufferString(body)),
		ContentLength: int64(len(body)),
	}
	if contentRange != "" {
		resp.Header.Set("Content-Range", contentRange)
	}
	return resp
}

func TestReadRangeResponseAcceptsExact206(t *testing.T) {
	resp := makeRangeResponse(http.StatusPartialContent, "bytes 10-14/100", "abcde")

	reader, err := ReadRangeResponse(resp, 10, 5)
	if err != nil {
		t.Fatalf("ReadRangeResponse() error = %v", err)
	}
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("reading result: %v", err)
	}
	if string(got) != "abcde" {
		t.Fatalf("got %q, want %q", got, "abcde")
	}
}

func TestReadRangeResponseRejects200IgnoringRange(t *testing.T) {
	resp := makeRangeResponse(http.StatusOK, "", "abcde")
	if _, err := ReadRangeResponse(resp, 10, 5); err == nil {
		t.Fatal("expected 200 response to be rejected")
	}
}

func TestReadRangeResponseRejectsMismatchedContentRange(t *testing.T) {
	resp := makeRangeResponse(http.StatusPartialContent, "bytes 0-4/100", "abcde")
	if _, err := ReadRangeResponse(resp, 10, 5); err == nil {
		t.Fatal("expected mismatched Content-Range to be rejected")
	}
}

func TestReadRangeResponseRejectsShortBody(t *testing.T) {
	resp := makeRangeResponse(http.StatusPartialContent, "bytes 10-14/100", "abc")
	if _, err := ReadRangeResponse(resp, 10, 5); err == nil {
		t.Fatal("expected short body to be rejected")
	}
}

func TestReadRangeResponseRejectsTrailingContentRangeData(t *testing.T) {
	for _, value := range []string{"bytes 10-14/100junk", "bytes 10-14/100 extra"} {
		t.Run(value, func(t *testing.T) {
			resp := makeRangeResponse(http.StatusPartialContent, value, "abcde")
			if _, err := ReadRangeResponse(resp, 10, 5); err == nil {
				t.Fatalf("expected malformed Content-Range %q to be rejected", value)
			}
		})
	}
}
