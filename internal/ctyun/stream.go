package ctyun

import (
	"container/list"
	"context"
	"fmt"
	"io"
	"sync"
)

// PartReaderFunc fetches a byte range from an upstream source (used by the
// zero-disk stream relay). It mirrors uploader.PartReaderFunc.
type PartReaderFunc func(ctx context.Context, offset int64, size int64) (io.Reader, error)

// windowCacheSize is the number of PartSize-aligned windows kept in memory.
// The uploader first walks the file sequentially to hash every part and then
// re-reads each part once while pushing, so a handful of windows is enough to
// serve the common access pattern without re-fetching.
const windowCacheSize = 8

type cacheEntry struct {
	start int64
	data  []byte
}

// remoteReaderAt adapts a PartReaderFunc into a concurrent-safe io.ReaderAt.
//
// Concurrency: the lock only guards the cache map and LRU list. Network fetches
// happen outside the lock, and concurrent callers requesting the same window
// share a single in-flight fetch (per-window single-flight) instead of
// serialising all workers behind one global mutex.
type remoteReaderAt struct {
	ctx   context.Context
	total int64
	fetch PartReaderFunc
	win   int64

	mu       sync.Mutex
	lru      *list.List              // front = most recently used
	items    map[int64]*list.Element // start -> element(*cacheEntry)
	inflight map[int64]*windowFetch
}

type windowFetch struct {
	done chan struct{}
	data []byte
	err  error
}

// NewRemoteReaderAt builds an io.ReaderAt over a remote range reader.
func NewRemoteReaderAt(ctx context.Context, total int64, fetch PartReaderFunc) io.ReaderAt {
	w := PartSize(total)
	if w <= 0 {
		w = 10 << 20
	}
	return &remoteReaderAt{
		ctx:      ctx,
		total:    total,
		fetch:    fetch,
		win:      w,
		lru:      list.New(),
		items:    make(map[int64]*list.Element),
		inflight: make(map[int64]*windowFetch),
	}
}

func (r *remoteReaderAt) windowStart(off int64) int64 {
	return (off / r.win) * r.win
}

func (r *remoteReaderAt) windowSize(start int64) int64 {
	size := r.win
	if start+size > r.total {
		size = r.total - start
	}
	if size < 0 {
		return 0
	}
	return size
}

// getWindow returns the cached bytes for the window starting at start,
// fetching them if necessary. Callers never hold mu across network IO.
func (r *remoteReaderAt) getWindow(start int64) ([]byte, error) {
	r.mu.Lock()
	if el, ok := r.items[start]; ok {
		r.lru.MoveToFront(el)
		data := el.Value.(*cacheEntry).data
		r.mu.Unlock()
		return data, nil
	}
	// Join an in-flight fetch for the same window instead of starting another.
	if wf, ok := r.inflight[start]; ok {
		r.mu.Unlock()
		select {
		case <-wf.done:
			return wf.data, wf.err
		case <-r.ctx.Done():
			return nil, r.ctx.Err()
		}
	}
	wf := &windowFetch{done: make(chan struct{})}
	r.inflight[start] = wf
	r.mu.Unlock()

	data, err := r.fetchWindow(start)

	r.mu.Lock()
	wf.data, wf.err = data, err
	delete(r.inflight, start)
	if err == nil && len(data) > 0 {
		el := r.lru.PushFront(&cacheEntry{start: start, data: data})
		r.items[start] = el
		for r.lru.Len() > windowCacheSize {
			back := r.lru.Back()
			if back == nil {
				break
			}
			entry := back.Value.(*cacheEntry)
			r.lru.Remove(back)
			delete(r.items, entry.start)
		}
	}
	r.mu.Unlock()
	close(wf.done)
	return data, err
}

func (r *remoteReaderAt) fetchWindow(start int64) ([]byte, error) {
	size := r.windowSize(start)
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
	if int64(n) != size {
		return nil, fmt.Errorf("stream short window %d+%d: got %d bytes", start, size, n)
	}
	return buf, nil
}

// ReadAt implements io.ReaderAt. It returns an explicit error whenever fewer
// than len(p) bytes are produced before EOF, as the interface requires.
func (r *remoteReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("negative offset %d", off)
	}
	if len(p) == 0 {
		return 0, nil
	}
	if off >= r.total {
		return 0, io.EOF
	}

	// Clamp the request to the file end; a read that stops exactly at EOF is
	// reported with io.EOF only if it began at or beyond the boundary.
	want := int64(len(p))
	truncated := false
	if off+want > r.total {
		want = r.total - off
		truncated = true
	}

	var n int
	for int64(n) < want {
		pos := off + int64(n)
		start := r.windowStart(pos)
		data, err := r.getWindow(start)
		if err != nil {
			return n, err
		}
		if data == nil {
			break
		}
		rel := pos - start
		if rel < 0 || rel >= int64(len(data)) {
			break
		}
		copied := copy(p[n:want], data[rel:])
		n += copied
		if copied == 0 {
			break
		}
	}

	if int64(n) < want {
		return n, fmt.Errorf("short read at offset %d: %d/%d bytes", off, n, want)
	}
	if truncated {
		// Partial read that reached the end of the object.
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
