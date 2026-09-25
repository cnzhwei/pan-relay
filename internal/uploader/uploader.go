package uploader

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cnzhwei/pan-relay/internal/client"
	"github.com/xhofe/wopan-sdk-go"
)

const (
	DefaultPartSize = int64(8 * 1024 * 1024) // 8MB per WoPan spec
)

type UploadOptions struct {
	Concurrency  int
	Retries      int
	ShowProgress bool
	RemoteName   string
}

type PartTask struct {
	PartIndex int64
	PartSize  int64
	Offset    int64
}

// PartReaderFunc supplies an io.Reader for the specified offset and size
type PartReaderFunc func(ctx context.Context, offset int64, size int64) (io.Reader, error)

type deleteRemoteFunc func(fid string) error

func cleanupPartialUpload(fid string, deleteRemote deleteRemoteFunc) error {
	if fid == "" {
		return nil
	}
	if err := deleteRemote(fid); err != nil {
		return fmt.Errorf("failed to clean up remote partial file %s: %w", fid, err)
	}
	return nil
}

// Upload uploads a local disk file to WoPan
func Upload(ctx context.Context, c *client.Client, localPath string, targetDirID string, opt UploadOptions) (string, error) {
	if opt.Concurrency <= 0 {
		opt.Concurrency = 4
	}
	if opt.Retries <= 0 {
		opt.Retries = 3
	}

	f, err := os.Open(localPath)
	if err != nil {
		return "", fmt.Errorf("failed to open local file: %w", err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("failed to stat local file: %w", err)
	}

	fileName := filepath.Base(localPath)
	if opt.RemoteName != "" {
		fileName = opt.RemoteName
	}
	fileSize := fi.Size()

	getPartReader := func(ctx context.Context, offset int64, size int64) (io.Reader, error) {
		return io.NewSectionReader(f, offset, size), nil
	}

	return UploadStream(ctx, c, fileName, fileSize, targetDirID, getPartReader, opt)
}

// UploadStream uploads directly from a stream/memory provider to WoPan without local disk storage
func UploadStream(ctx context.Context, c *client.Client, fileName string, fileSize int64, targetDirID string, getPartReader PartReaderFunc, opt UploadOptions) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("context is nil")
	}
	if c == nil {
		return "", fmt.Errorf("WoPan client is nil")
	}
	if fileName == "" {
		return "", fmt.Errorf("file name is empty")
	}
	if fileSize <= 0 {
		return "", fmt.Errorf("file size must be positive: %d", fileSize)
	}
	if getPartReader == nil {
		return "", fmt.Errorf("part reader provider is nil")
	}
	if opt.Concurrency <= 0 {
		opt.Concurrency = 4
	}
	if opt.Retries <= 0 {
		opt.Retries = 3
	}

	if targetDirID == "" {
		targetDirID = "0"
	}

	rawClient := c.Raw()
	uploadCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	zoneURL := "https://tjupload.pan.wo.cn"
	if rawClient.ZoneURL != "" {
		zoneURL = rawClient.ZoneURL
	}
	uploadURL := zoneURL + "/openapi/client/" + wopan.KeyUpload2C

	// 1. Calculate parts
	totalPart := int64(math.Ceil(float64(fileSize) / float64(DefaultPartSize)))
	if totalPart == 0 {
		totalPart = 1
	}

	// 2. Prepare encrypted fileInfo
	batchNo := time.Now().Format("20060102150405")
	fileInfo := wopan.Json{
		"spaceType":   c.SpaceType(),
		"directoryId": targetDirID,
		"batchNo":     batchNo,
		"fileName":    fileName,
		"fileSize":    fileSize,
		"fileType":    rawClient.GetFileType(fileName),
	}
	if c.SpaceType() == wopan.SpaceTypeFamily {
		fileInfo["familyId"] = c.Config().FamilyID
	}

	fileInfoStr, err := rawClient.EncryptParam(wopan.ChannelWoHome, fileInfo)
	if err != nil {
		return "", fmt.Errorf("failed to encrypt fileInfo: %w", err)
	}

	randomSuffix := make([]byte, 3)
	_, _ = rand.Read(randomSuffix)
	uniqueID := fmt.Sprintf("%d_%s", time.Now().UnixMilli(), hex.EncodeToString(randomSuffix))

	// Progress tracking
	var completedBytes atomic.Int64
	startTime := time.Now()
	var finalFid string
	var fidMu sync.Mutex

	// Print starting banner
	fmt.Printf("Uploading %s (%.2f MB) -> WoPan Dir [%s] with %d threads...\n",
		fileName, float64(fileSize)/(1024*1024), targetDirID, opt.Concurrency)

	// Channel for parts
	tasksChan := make(chan PartTask, totalPart)
	for p := int64(1); p <= totalPart; p++ {
		offset := (p - 1) * DefaultPartSize
		partSize := DefaultPartSize
		if p == totalPart {
			partSize = fileSize - offset
		}
		tasksChan <- PartTask{
			PartIndex: p,
			PartSize:  partSize,
			Offset:    offset,
		}
	}
	close(tasksChan)

	// Progress ticker
	doneTicker := make(chan struct{})
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

	// Concurrency worker group
	var wg sync.WaitGroup
	errChan := make(chan error, opt.Concurrency)
	workerCount := opt.Concurrency
	if int64(workerCount) > totalPart {
		workerCount = int(totalPart)
	}

	for wID := 0; wID < workerCount; wID++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for task := range tasksChan {
				select {
				case <-uploadCtx.Done():
					return
				default:
				}

				// Upload part with retries
				var partErr error
				for attempt := 0; attempt <= opt.Retries; attempt++ {
					select {
					case <-uploadCtx.Done():
						return
					default:
					}

					partReader, rErr := getPartReader(uploadCtx, task.Offset, task.PartSize)
					if rErr != nil {
						partErr = fmt.Errorf("failed to obtain part reader: %w", rErr)
						time.Sleep(time.Duration(attempt+1) * time.Second)
						continue
					}

					var resp wopan.Upload2CResp
					formData := map[string]string{
						"uniqueId":    uniqueID,
						"accessToken": c.Config().AccessToken,
						"fileName":    fileName,
						"psToken":     "undefined",
						"fileSize":    strconv.FormatInt(fileSize, 10),
						"totalPart":   strconv.FormatInt(totalPart, 10),
						"channel":     wopan.ChannelWoCloud,
						"directoryId": targetDirID,
						"fileInfo":    fileInfoStr,
						"partSize":    strconv.FormatInt(task.PartSize, 10),
						"partIndex":   strconv.FormatInt(task.PartIndex, 10),
					}

					req := rawClient.NewRequest().
						SetContext(uploadCtx).
						SetResult(&resp).
						ForceContentType("application/json;charset=UTF-8").
						SetHeaders(map[string]string{
							"Origin":     "https://pan.wo.cn",
							"Referer":    "https://pan.wo.cn/",
							"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/114.0.0.0 Safari/537.36",
						}).
						SetMultipartFormData(formData).
						SetMultipartField("file", fileName, "application/octet-stream", partReader)

					res, reqErr := req.Post(uploadURL)
					if reqErr != nil {
						partErr = reqErr
						time.Sleep(time.Duration(attempt+1) * time.Second)
						continue
					}
					if res.StatusCode() != http.StatusOK {
						partErr = fmt.Errorf("part %d HTTP status %d: %s", task.PartIndex, res.StatusCode(), res.String())
						time.Sleep(time.Duration(attempt+1) * time.Second)
						continue
					}
					if resp.Code != "0000" {
						partErr = fmt.Errorf("part %d WoPan code %s: %s", task.PartIndex, resp.Code, resp.Msg)
						time.Sleep(time.Duration(attempt+1) * time.Second)
						continue
					}

					// Part succeeded
					partErr = nil
					if resp.Data.Fid != "" {
						fidMu.Lock()
						finalFid = resp.Data.Fid
						fidMu.Unlock()
					}
					completedBytes.Add(task.PartSize)
					break
				}

				if partErr != nil {
					cancel()
					select {
					case errChan <- fmt.Errorf("part %d failed after retries: %w", task.PartIndex, partErr):
					default:
					}
					return
				}
			}
		}()
	}

	wg.Wait()
	close(doneTicker)
	if err := ctx.Err(); err != nil {
		fidMu.Lock()
		partialFid := finalFid
		fidMu.Unlock()
		if partialFid != "" {
			if cleanupErr := cleanupPartialUpload(partialFid, c.Delete); cleanupErr != nil {
				return "", fmt.Errorf("%w; %v", err, cleanupErr)
			}
		}
		return "", err
	}

	select {
	case err := <-errChan:
		fidMu.Lock()
		partialFid := finalFid
		fidMu.Unlock()
		if partialFid != "" {
			if cleanupErr := cleanupPartialUpload(partialFid, c.Delete); cleanupErr != nil {
				return "", fmt.Errorf("%w; %v", err, cleanupErr)
			}
		}
		fmt.Printf("\n[!] Upload failed: %v\n", err)
		return "", err
	default:
	}

	totalTime := time.Since(startTime).Seconds()
	avgSpeed := (float64(fileSize) / (1024 * 1024)) / totalTime
	fmt.Printf("\r  Progress: 100.0%% (%6.1f / %6.1f MB) | Avg Speed: %5.2f MB/s | Total Time: %.1fs\n",
		float64(fileSize)/(1024*1024), float64(fileSize)/(1024*1024), avgSpeed, totalTime)
	fmt.Printf("[✓] Upload completed successfully! (FID: %s)\n", finalFid)

	return finalFid, nil
}
