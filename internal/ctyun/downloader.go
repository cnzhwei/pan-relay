package ctyun

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// DownloadChunkSize is the Range chunk used by the multi-connection downloader.
const DownloadChunkSize = int64(8 << 20)

// DefaultDownloadConcurrency is used when the caller does not specify one.
// Measured behaviour differs by network path: over a same-ISP link a single
// connection reached ~35 MB/s while 8 parallel Range requests dropped to
// ~11 MB/s (server throttles per connection); through a cross-border proxy
// 4 connections were faster than 1. Tune with -t if throughput looks off.
const DefaultDownloadConcurrency = 4

// DownloadOptions configures a download.
type DownloadOptions struct {
	Concurrency  int
	Retries      int
	ShowProgress bool
	Progress     func(written, total int64)
}

// Download fetches a URL to outputPath, resuming in chunks when possible.
func Download(ctx context.Context, downloadURL string, customHeaders http.Header, outputPath string, opt DownloadOptions) error {
	if opt.Concurrency <= 0 {
		opt.Concurrency = DefaultDownloadConcurrency
	}
	if opt.Retries <= 0 {
		opt.Retries = 3
	}

	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:        64,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 15 * time.Second,
	}
	client := &http.Client{Transport: transport, Timeout: 0}

	size, supportsRange, err := probe(client, ctx, downloadURL, customHeaders)
	if err != nil {
		return err
	}

	if size > 0 {
		if fi, err := os.Stat(outputPath); err == nil && fi.Size() == size {
			if opt.Progress != nil {
				opt.Progress(size, size)
			}
			return nil // already complete
		}
	}

	if !supportsRange || size <= 0 || opt.Concurrency == 1 {
		return downloadSingle(ctx, client, downloadURL, customHeaders, outputPath, size, opt)
	}
	return downloadChunked(ctx, client, downloadURL, customHeaders, outputPath, size, opt)
}

func probe(client *http.Client, ctx context.Context, u string, hdr http.Header) (int64, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, false, err
	}
	for k, v := range hdr {
		for _, vv := range v {
			req.Header.Add(k, vv)
		}
	}
	req.Header.Set("Range", "bytes=0-0")

	resp, err := client.Do(req)
	if err != nil {
		return 0, false, fmt.Errorf("probe failed: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1))

	switch resp.StatusCode {
	case http.StatusPartialContent:
		var start, end, total int64
		if _, err := fmt.Sscanf(resp.Header.Get("Content-Range"), "bytes %d-%d/%d", &start, &end, &total); err == nil && total > 0 {
			return total, true, nil
		}
		return resp.ContentLength, false, nil
	case http.StatusOK:
		return resp.ContentLength, false, nil
	default:
		return 0, false, fmt.Errorf("probe returned HTTP %d", resp.StatusCode)
	}
}

func newRequest(ctx context.Context, u string, hdr http.Header) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range hdr {
		for _, vv := range v {
			req.Header.Add(k, vv)
		}
	}
	return req, nil
}

func downloadSingle(ctx context.Context, client *http.Client, u string, hdr http.Header,
	out string, size int64, opt DownloadOptions) error {

	// Resume a partial file when the server supports Range.
	var offset int64
	if fi, err := os.Stat(out); err == nil && size > 0 && fi.Size() < size {
		offset = fi.Size()
	} else if err == nil && size <= 0 {
		offset = 0
	}

	req, err := newRequest(ctx, u, hdr)
	if err != nil {
		return err
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}

	flags := os.O_CREATE | os.O_WRONLY
	if offset > 0 && resp.StatusCode == http.StatusPartialContent {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
		offset = 0
	}
	if dir := filepath.Dir(out); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(out, flags, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	var written int64 = offset
	total := size
	if total <= 0 {
		total = resp.ContentLength
	}
	buf := make([]byte, 1<<20)
	last := time.Now()
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return werr
			}
			written += int64(n)
			if opt.Progress != nil && time.Since(last) > 200*time.Millisecond {
				opt.Progress(written, total)
				last = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return rerr
		}
	}
	if opt.Progress != nil {
		opt.Progress(written, total)
	}
	return nil
}

func downloadChunked(ctx context.Context, client *http.Client, u string, hdr http.Header,
	out string, size int64, opt DownloadOptions) error {

	chunk := DownloadChunkSize
	if chunk <= 0 {
		chunk = 8 << 20
	}

	// Preallocate the file so writes can seek freely.
	dir := filepath.Dir(out)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(out, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Truncate(size); err != nil {
		return err
	}

	type span struct{ start, end int64 }
	spans := make([]span, 0, size/chunk+1)
	for start := int64(0); start < size; start += chunk {
		end := start + chunk - 1
		if end >= size {
			end = size - 1
		}
		spans = append(spans, span{start, end})
	}

	var written int64
	var wmu sync.Mutex
	jobs := make(chan span)
	errs := make(chan error, len(spans))
	var wg sync.WaitGroup

	worker := func() {
		defer wg.Done()
		buf := make([]byte, 1<<20)
		for sp := range jobs {
			if err := fetchSpan(ctx, client, u, hdr, f, sp.start, sp.end, buf, &written, &wmu, size, opt); err != nil {
				errs <- err
				return
			}
		}
	}

	n := opt.Concurrency
	for i := 0; i < n; i++ {
		wg.Add(1)
		go worker()
	}
	for _, sp := range spans {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return ctx.Err()
		case jobs <- sp:
		}
	}
	close(jobs)
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

func fetchSpan(ctx context.Context, client *http.Client, u string, hdr http.Header, f *os.File,
	start, end int64, buf []byte, written *int64, wmu *sync.Mutex, total int64, opt DownloadOptions) error {

	req, err := newRequest(ctx, u, hdr)
	if err != nil {
		return err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("range %d-%d returned HTTP %d", start, end, resp.StatusCode)
	}

	pos := start
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.WriteAt(buf[:n], pos); werr != nil {
				return werr
			}
			pos += int64(n)
			atomic.AddInt64(written, int64(n))
			if opt.Progress != nil {
				wmu.Lock()
				opt.Progress(atomic.LoadInt64(written), total)
				wmu.Unlock()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	if pos != end+1 {
		return fmt.Errorf("range %d-%d truncated at %d", start, end, pos)
	}
	return nil
}
