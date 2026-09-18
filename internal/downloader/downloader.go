package downloader

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

type DownloadOptions struct {
	Concurrency int
	Retries     int
	ShowProgress bool
}

type ChunkTask struct {
	Index int
	Start int64
	End   int64
}

func Download(ctx context.Context, downloadURL string, outputPath string, opt DownloadOptions) error {
	if opt.Concurrency <= 0 {
		opt.Concurrency = 4
	}
	if opt.Retries <= 0 {
		opt.Retries = 3
	}

	// 1. Send HEAD / Range 0-0 probe to check file size and Range support
	req, err := http.NewRequestWithContext(ctx, "HEAD", downloadURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create HEAD request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	var fileSize int64 = -1
	acceptRanges := false

	if err == nil {
		defer resp.Body.Close()
		fileSize = resp.ContentLength
		if resp.Header.Get("Accept-Ranges") == "bytes" || resp.StatusCode == http.StatusOK {
			acceptRanges = true
		}
	}

	// If HEAD didn't return content length, probe with GET Range: bytes=0-0
	if fileSize <= 0 {
		rangeReq, _ := http.NewRequestWithContext(ctx, "GET", downloadURL, nil)
		rangeReq.Header.Set("Range", "bytes=0-0")
		rangeReq.Header.Set("User-Agent", "Mozilla/5.0")
		rResp, rErr := client.Do(rangeReq)
		if rErr == nil {
			defer rResp.Body.Close()
			if rResp.StatusCode == http.StatusPartialContent {
				acceptRanges = true
				// Content-Range: bytes 0-0/1234567
				cr := rResp.Header.Get("Content-Range")
				var start, end, total int64
				if _, err := fmt.Sscanf(cr, "bytes %d-%d/%d", &start, &end, &total); err == nil && total > 0 {
					fileSize = total
				}
			} else if rResp.StatusCode == http.StatusOK {
				fileSize = rResp.ContentLength
			}
		}
	}

	// Prepare local file
	_ = os.MkdirAll(filepath.Dir(outputPath), 0755)
	tmpPath := outputPath + ".download.tmp"
	out, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to create destination file: %w", err)
	}
	defer func() {
		out.Close()
		if _, err := os.Stat(outputPath); err != nil {
			// clean up partial on failure
			_ = os.Remove(tmpPath)
		}
	}()

	startTime := time.Now()

	// Single-threaded fallback if range is not supported or file size is unknown or tiny (< 8MB)
	if !acceptRanges || fileSize <= 8*1024*1024 || opt.Concurrency == 1 {
		fmt.Printf("Downloading %s directly (single-threaded)...\n", filepath.Base(outputPath))
		getReq, _ := http.NewRequestWithContext(ctx, "GET", downloadURL, nil)
		getReq.Header.Set("User-Agent", "Mozilla/5.0")
		gResp, gErr := client.Do(getReq)
		if gErr != nil {
			return gErr
		}
		defer gResp.Body.Close()

		if gResp.StatusCode != http.StatusOK && gResp.StatusCode != http.StatusPartialContent {
			return fmt.Errorf("download server returned HTTP %d", gResp.StatusCode)
		}

		if _, err := io.Copy(out, gResp.Body); err != nil {
			return fmt.Errorf("failed to write file: %w", err)
		}
		_ = out.Close()
		_ = os.Rename(tmpPath, outputPath)
		fmt.Printf("[✓] Download completed: %s\n", outputPath)
		return nil
	}

	// Pre-allocate file size
	if err := out.Truncate(fileSize); err != nil {
		return fmt.Errorf("failed to allocate file size: %w", err)
	}

	fmt.Printf("Downloading %s (%.2f MB) with %d threads...\n",
		filepath.Base(outputPath), float64(fileSize)/(1024*1024), opt.Concurrency)

	// Partition file into chunks
	partSize := fileSize / int64(opt.Concurrency)
	tasks := make([]ChunkTask, opt.Concurrency)
	for i := 0; i < opt.Concurrency; i++ {
		start := int64(i) * partSize
		end := start + partSize - 1
		if i == opt.Concurrency-1 {
			end = fileSize - 1
		}
		tasks[i] = ChunkTask{
			Index: i,
			Start: start,
			End:   end,
		}
	}

	var completedBytes atomic.Int64
	doneTicker := make(chan struct{})

	// Progress ticker
	if opt.ShowProgress {
		go func() {
			ticker := time.NewTicker(400 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-doneTicker:
					return
				case <-ticker.C:
					done := completedBytes.Load()
					pct := float64(done) / float64(fileSize) * 100
					if pct > 100 {
						pct = 100
					}
					elapsed := time.Since(startTime).Seconds()
					speedMB := (float64(done) / (1024 * 1024)) / elapsed
					fmt.Printf("\r  Progress: %5.1f%% (%6.1f / %6.1f MB) | Speed: %5.2f MB/s | Elapsed: %.0fs",
						pct, float64(done)/(1024*1024), float64(fileSize)/(1024*1024), speedMB, elapsed)
				}
			}
		}()
	}

	var wg sync.WaitGroup
	errChan := make(chan error, opt.Concurrency)

	for _, task := range tasks {
		wg.Add(1)
		go func(t ChunkTask) {
			defer wg.Done()

			var chunkErr error
			for attempt := 0; attempt <= opt.Retries; attempt++ {
				select {
				case <-ctx.Done():
					return
				default:
				}

				req, _ := http.NewRequestWithContext(ctx, "GET", downloadURL, nil)
				req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", t.Start, t.End))
				req.Header.Set("User-Agent", "Mozilla/5.0")

				resp, err := client.Do(req)
				if err != nil {
					chunkErr = err
					time.Sleep(time.Duration(attempt+1) * time.Second)
					continue
				}

				if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
					resp.Body.Close()
					chunkErr = fmt.Errorf("chunk %d HTTP %d", t.Index, resp.StatusCode)
					time.Sleep(time.Duration(attempt+1) * time.Second)
					continue
				}

				// Stream chunk into file at offset
				buf := make([]byte, 64*1024)
				currentOffset := t.Start
				var readErr error

				for {
					n, rErr := resp.Body.Read(buf)
					if n > 0 {
						if _, wErr := out.WriteAt(buf[:n], currentOffset); wErr != nil {
							readErr = wErr
							break
						}
						currentOffset += int64(n)
						completedBytes.Add(int64(n))
					}
					if rErr != nil {
						if rErr != io.EOF {
							readErr = rErr
						}
						break
					}
				}
				resp.Body.Close()

				if readErr == nil && currentOffset >= t.End+1 {
					chunkErr = nil
					break
				}
				chunkErr = readErr
				time.Sleep(time.Duration(attempt+1) * time.Second)
			}

			if chunkErr != nil {
				select {
				case errChan <- fmt.Errorf("chunk %d failed: %w", t.Index, chunkErr):
				default:
				}
			}
		}(task)
	}

	wg.Wait()
	close(doneTicker)

	select {
	case err := <-errChan:
		fmt.Printf("\n[!] Download failed: %v\n", err)
		return err
	default:
	}

	_ = out.Close()
	if err := os.Rename(tmpPath, outputPath); err != nil {
		return fmt.Errorf("failed to finalize downloaded file: %w", err)
	}

	totalTime := time.Since(startTime).Seconds()
	avgSpeed := (float64(fileSize) / (1024 * 1024)) / totalTime
	fmt.Printf("\r  Progress: 100.0%% (%6.1f / %6.1f MB) | Avg Speed: %5.2f MB/s | Total Time: %.1fs\n",
		float64(fileSize)/(1024*1024), float64(fileSize)/(1024*1024), avgSpeed, totalTime)
	fmt.Printf("[✓] Download completed successfully: %s\n", outputPath)

	return nil
}
