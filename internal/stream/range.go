package stream

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
)

var contentRangePattern = regexp.MustCompile(`^bytes ([0-9]+)-([0-9]+)/([0-9]+)$`)

// Callers must close resp.Body after this function returns.
func ReadRangeResponse(resp *http.Response, offset, size int64) (io.Reader, error) {
	if resp == nil || resp.Body == nil {
		return nil, fmt.Errorf("nil range response")
	}
	if size <= 0 || offset < 0 {
		return nil, fmt.Errorf("invalid range offset=%d size=%d", offset, size)
	}
	if resp.StatusCode != http.StatusPartialContent {
		return nil, fmt.Errorf("range request returned HTTP %d, want %d", resp.StatusCode, http.StatusPartialContent)
	}

	contentRange := resp.Header.Get("Content-Range")
	parts := contentRangePattern.FindStringSubmatch(contentRange)
	if len(parts) != 4 {
		return nil, fmt.Errorf("invalid Content-Range %q", contentRange)
	}
	start, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid Content-Range start %q: %w", contentRange, err)
	}
	end, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid Content-Range end %q: %w", contentRange, err)
	}
	total, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid Content-Range total %q: %w", contentRange, err)
	}
	wantEnd := offset + size - 1
	if start != offset || end != wantEnd || total <= end {
		return nil, fmt.Errorf("unexpected Content-Range %q for bytes %d-%d", resp.Header.Get("Content-Range"), offset, wantEnd)
	}
	if resp.ContentLength >= 0 && resp.ContentLength != size {
		return nil, fmt.Errorf("unexpected range body length %d, want %d", resp.ContentLength, size)
	}

	buf := make([]byte, size)
	n, err := io.ReadFull(resp.Body, buf)
	if err != nil || int64(n) != size {
		return nil, fmt.Errorf("short range body: got %d/%d bytes: %w", n, size, err)
	}
	return bytesReader(buf), nil
}

// bytesReader is kept as a small indirection so the validation function only
// exposes an io.Reader and callers cannot mutate the backing slice directly.
func bytesReader(buf []byte) io.Reader {
	return &reader{buf: buf}
}

type reader struct {
	buf []byte
	off int
}

func (r *reader) Read(p []byte) (int, error) {
	if r.off == len(r.buf) {
		return 0, io.EOF
	}
	n := copy(p, r.buf[r.off:])
	r.off += n
	return n, nil
}
