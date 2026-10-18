package ctyun

import (
	"context"
	"fmt"
	"io"
	"sync"
)

// PartReaderFunc fetches a byte range from an upstream source (used by the
// zero-disk stream relay). It mirrors uploader.PartReaderFunc.
type PartReaderFunc func(ctx context.Context, offset int64, size int64) (io.Reader, error)

// remoteReaderAt adapts a PartReaderFunc into an io.ReaderAt so the upload path
// can read parts concurrently without touching disk.
//
// It caches a few PartSize-aligned windows: during an upload the hashing pass
// walks sequentially and the push phase reads each part exactly once, so a
// small window cache keeps the number of upstream requests close to the number
// of parts.
type remoteReaderAt struct {
	ctx   context.Context
	total int64
	fetch PartReaderFunc
	win   int64

	mu    sync.Mutex
	cache map[int64][]byte
	order []int64
	max   int
}

const streamWindowCache = 4

// NewRemoteReaderAt builds an io.ReaderAt over a remote range reader.
func NewRemoteReaderAt(ctx context.Context, total int64, fetch PartReaderFunc) io.ReaderAt {
	w := PartSize(total)
	if w <= 0 {
		w = 10 << 20
	}
	return &remoteReaderAt{
		ctx:   ctx,
		total: total,
		fetch: fetch,
		win:   w,
		cache: make(map[int64][]byte),
		max:   streamWindowCache,
	}
}

func (r *remoteReaderAt) loadWindow(start int64) ([]byte, error) {
	if buf, ok := r.cache[start]; ok {
		return buf, nil
	}

	size := r.win
	if start+size > r.total {
		size = r.total - start
	}
	if size <= 0 {
		return nil, nil
	}

	rd, err := r.fetch(r.ctx, start, size)
	if err != nil {
		return nil, fmt.Errorf("stream fetch %d+%d: %w", start, size, err)
	}
	buf := make([]byte, size)
	n, err := io.ReadFull(rd, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, fmt.Errorf("stream read %d+%d: %w", start, size, err)
	}
	buf = buf[:n]

	// Evict the oldest window once the cache is full.
	for len(r.order) >= r.max && len(r.order) > 0 {
		old := r.order[0]
		r.order = r.order[1:]
		delete(r.cache, old)
	}
	r.cache[start] = buf
	r.order = append(r.order, start)
	return buf, nil
}

// ReadAt implements io.ReaderAt.
func (r *remoteReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("negative offset %d", off)
	}
	if off >= r.total {
		return 0, io.EOF
	}

	start := (off / r.win) * r.win

	r.mu.Lock()
	buf, err := r.loadWindow(start)
	r.mu.Unlock()
	if err != nil {
		return 0, err
	}

	rel := off - start
	if rel >= int64(len(buf)) {
		// The requested range spills past this window; fall back to a precise
		// fetch for the remainder.
		need := int64(len(p))
		if off+need > r.total {
			need = r.total - off
		}
		rd, ferr := r.fetch(r.ctx, off, need)
		if ferr != nil {
			return 0, ferr
		}
		n, rerr := io.ReadFull(rd, p[:need])
		if rerr == io.ErrUnexpectedEOF || rerr == io.EOF {
			rerr = nil
		}
		return n, rerr
	}

	n := copy(p, buf[rel:])
	if n < len(p) && off+int64(n) < r.total {
		// Rare: buffer shorter than requested. Read the tail directly.
		need := int64(len(p) - n)
		if off+int64(n)+need > r.total {
			need = r.total - (off + int64(n))
		}
		rd, ferr := r.fetch(r.ctx, off+int64(n), need)
		if ferr == nil {
			m, _ := io.ReadFull(rd, p[n:n+int(need)])
			n += m
		}
	}
	if off+int64(n) >= r.total {
		return n, io.EOF
	}
	return n, nil
}

// UploadStream pushes a remote byte range into parentID without staging it on
// disk, hashing part-by-part as it goes.
func (c *Client) UploadStream(ctx context.Context, parentID, name string, size int64,
	fetch PartReaderFunc, opt UploadOptions) (string, error) {

	if fetch == nil {
		return "", fmt.Errorf("part reader is nil")
	}
	return c.uploadReaderAt(ctx, parentID, name, NewRemoteReaderAt(ctx, size, fetch), size, opt)
}
