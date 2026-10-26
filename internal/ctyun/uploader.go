package ctyun

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PartSize returns the slice size the API requires for a given file size.
// The upstream rules are:
//
//	<= 9990 MiB          -> 10 MiB slices (up to 999 parts)
//	<= 19980 MiB         -> 20 MiB slices
//	larger               -> ceil(size/1999) rounded up to 10 MiB, min 50 MiB
func PartSize(size int64) int64 {
	const d = 10 << 20
	if size > d*2*999 {
		need := (size + 1999 - 1) / 1999
		units := (need + d - 1) / d
		if units < 5 {
			units = 5
		}
		return units * d
	}
	if size > d*999 {
		return d * 2
	}
	return d
}

// UploadOptions configures an upload.
type UploadOptions struct {
	Concurrency  int
	Retries      int
	ShowProgress bool
	RemoteName   string
	// Progress receives 0..100 percentages.
	Progress func(int)
}

type initUploadResp struct {
	ResCode    any    `json:"res_code"`
	ResMessage string `json:"res_message"`

	Data struct {
		UploadType     int    `json:"uploadType"`
		UploadHost     string `json:"uploadHost"`
		UploadFileID   string `json:"uploadFileId"`
		FileDataExists int    `json:"fileDataExists"`
	} `json:"data"`
}

type partURL struct {
	RequestURL    string `json:"requestURL"`
	RequestHeader string `json:"requestHeader"`
}

type uploadURLsResp struct {
	Code       string             `json:"code"`
	UploadURLs map[string]partURL `json:"uploadURLs"`
}

type commitResp struct {
	ResCode    any    `json:"res_code"`
	ResMessage string `json:"res_message"`

	File struct {
		UserFileID apiID  `json:"userFileId"`
		FileName   string `json:"fileName"`
		FileSize   int64  `json:"fileSize"`
		FileMD5    string `json:"fileMd5"`
	} `json:"file"`
}

func uploadPrefix(base string, isFamily bool) string {
	if isFamily {
		return base + "/family"
	}
	return base + "/person"
}

// UploadLocal uploads a file from disk into parentID and returns its file id.
func (c *Client) UploadLocal(ctx context.Context, parentID, localPath string, opt UploadOptions) (string, error) {
	f, err := os.Open(localPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	if st.IsDir() {
		return "", fmt.Errorf("%s is a directory", localPath)
	}
	name := opt.RemoteName
	if name == "" {
		name = st.Name()
	}
	return c.uploadReaderAt(ctx, parentID, name, f, st.Size(), opt)
}

// Upload pushes the whole content behind ra into parentID and returns the id.
func (c *Client) Upload(ctx context.Context, parentID, name string, ra io.ReaderAt, size int64, opt UploadOptions) (string, error) {
	return c.uploadReaderAt(ctx, parentID, name, ra, size, opt)
}

func (c *Client) uploadReaderAt(ctx context.Context, parentID, name string, ra io.ReaderAt, size int64, opt UploadOptions) (string, error) {
	if size <= 0 {
		return "", fmt.Errorf("refusing to upload empty file %q", name)
	}
	if ra == nil {
		return "", fmt.Errorf("nil reader")
	}
	if opt.Concurrency <= 0 {
		opt.Concurrency = 3
	}
	if opt.Retries <= 0 {
		opt.Retries = 3
	}
	if parentID == "" {
		parentID = c.cfg.RootFolderID
	}
	if parentID == "" {
		parentID = DefaultRootID
	}

	isFamily := c.cfg.IsFamily()
	if isFamily && c.cfg.FamilyID == "" {
		return "", fmt.Errorf("family cloud upload requires family_id in the config")
	}

	sliceSize := PartSize(size)
	partCount := int((size + sliceSize - 1) / sliceSize)
	if partCount < 1 {
		partCount = 1
	}

	// --- 1. init upload session ---
	initParams := map[string]string{
		"parentFolderId": parentID,
		"fileName":       EscapeFileName(name),
		"fileSize":       strconv.FormatInt(size, 10),
		"sliceSize":      strconv.FormatInt(sliceSize, 10),
		"lazyCheck":      "1",
	}
	if isFamily {
		initParams["familyId"] = c.cfg.FamilyID
	}

	var initRes initUploadResp
	if err := c.do(ctx, http.MethodGet, uploadPrefix(c.uploadBase(), isFamily)+"/initMultiUpload",
		RequestOptions{Encrypted: initParams}, &initRes); err != nil {
		return "", fmt.Errorf("initMultiUpload: %w", err)
	}
	uploadFileID := initRes.Data.UploadFileID
	if uploadFileID == "" {
		return "", fmt.Errorf("initMultiUpload returned no uploadFileId (res_message=%s)", initRes.ResMessage)
	}

	// --- 2. hash every part, plus the whole file ---
	type partMeta struct {
		index  int
		offset int64
		length int64
		md5hex string
		md5b64 string
	}
	parts := make([]partMeta, partCount)
	wholeHash := md5.New()
	buf := make([]byte, 1<<20)

	for i := 0; i < partCount; i++ {
		offset := int64(i) * sliceSize
		length := sliceSize
		if i == partCount-1 {
			length = size - offset
		}

		h := md5.New()
		var read int64
		for read < length {
			want := int64(len(buf))
			if length-read < want {
				want = length - read
			}
			n, rerr := ra.ReadAt(buf[:want], offset+read)
			if n > 0 {
				h.Write(buf[:n])
				wholeHash.Write(buf[:n])
				read += int64(n)
			}
			if rerr != nil {
				if (rerr == io.EOF || rerr == io.ErrUnexpectedEOF) && read == length {
					break
				}
				if rerr == io.EOF && read < length {
					return "", fmt.Errorf("part %d ended early at %d/%d bytes", i+1, read, length)
				}
				return "", fmt.Errorf("read part %d: %w", i+1, rerr)
			}
		}
		if read != length {
			return "", fmt.Errorf("part %d short read: %d/%d bytes", i+1, read, length)
		}

		sum := h.Sum(nil)
		parts[i] = partMeta{
			index:  i + 1,
			offset: offset,
			length: length,
			md5hex: strings.ToUpper(hex.EncodeToString(sum)),
			md5b64: base64.StdEncoding.EncodeToString(sum),
		}
		if opt.Progress != nil {
			opt.Progress((i + 1) * 10 / partCount)
		}
	}

	// --- 3. upload parts concurrently (each worker reads its own range) ---
	// Buffered so failing workers never block on send, and a derived context so
	// a worker error unblocks the feeder instead of leaking it.
	upCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	type job struct{ meta partMeta }
	jobs := make(chan job, len(parts))
	errs := make(chan error, len(parts))
	var wg sync.WaitGroup
	var doneMu sync.Mutex
	done := 0
	var firstErr error
	var errOnce sync.Once

	fail := func(err error) {
		errOnce.Do(func() {
			firstErr = err
			cancel()
		})
	}

	worker := func() {
		defer wg.Done()
		for j := range jobs {
			if upCtx.Err() != nil {
				return
			}
			if err := c.uploadPart(upCtx, isFamily, uploadFileID, ra, j.meta, opt.Retries); err != nil {
				errs <- err
				fail(err)
				return
			}
			doneMu.Lock()
			done++
			cur := done
			doneMu.Unlock()
			if opt.Progress != nil {
				opt.Progress(10 + int(float64(cur)*85/float64(len(parts))))
			}
		}
	}

	nWorkers := opt.Concurrency
	if nWorkers > len(parts) {
		nWorkers = len(parts)
	}
	if nWorkers < 1 {
		nWorkers = 1
	}
	for i := 0; i < nWorkers; i++ {
		wg.Add(1)
		go worker()
	}

	go func() {
		defer close(jobs)
		for _, p := range parts {
			select {
			case <-upCtx.Done():
				return
			case jobs <- job{meta: p}:
			}
		}
	}()

	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			return "", e
		}
	}
	if firstErr != nil {
		return "", firstErr
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	// --- 4. commit ---
	fileMD5 := strings.ToUpper(hex.EncodeToString(wholeHash.Sum(nil)))
	sliceMD5 := fileMD5
	if len(parts) > 1 {
		joined := make([]string, len(parts))
		for i, p := range parts {
			joined[i] = p.md5hex
		}
		sum := md5.Sum([]byte(strings.Join(joined, "\n")))
		sliceMD5 = strings.ToUpper(hex.EncodeToString(sum[:]))
	}

	commitParams := map[string]string{
		"uploadFileId": uploadFileID,
		"fileMd5":      fileMD5,
		"sliceMd5":     sliceMD5,
		"lazyCheck":    "1",
		"isLog":        "0",
		"opertype":     "1",
	}
	if isFamily {
		commitParams["familyId"] = c.cfg.FamilyID
	}

	var commit commitResp
	if err := c.do(ctx, http.MethodGet, uploadPrefix(c.uploadBase(), isFamily)+"/commitMultiUploadFile",
		RequestOptions{Encrypted: commitParams}, &commit); err != nil {
		return "", fmt.Errorf("commitMultiUploadFile: %w", err)
	}
	fid := commit.File.UserFileID.String()
	if fid == "" {
		return "", fmt.Errorf("commit returned no userFileId (res_message=%s)", commit.ResMessage)
	}
	if opt.Progress != nil {
		opt.Progress(100)
	}
	return fid, nil
}

// uploadPart fetches a signed URL for one part and PUTs the bytes to it.
// Each attempt reads the range through ra, which is safe for concurrent use.
func (c *Client) uploadPart(ctx context.Context, isFamily bool, uploadFileID string,
	ra io.ReaderAt, m struct {
		index  int
		offset int64
		length int64
		md5hex string
		md5b64 string
	}, retries int) error {

	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}

		params := map[string]string{
			"uploadFileId": uploadFileID,
			"partInfo":     fmt.Sprintf("%d-%s", m.index, m.md5b64),
		}
		if isFamily {
			params["familyId"] = c.cfg.FamilyID
		}

		var urls uploadURLsResp
		if err := c.do(ctx, http.MethodGet, uploadPrefix(c.uploadBase(), isFamily)+"/getMultiUploadUrls",
			RequestOptions{Encrypted: params}, &urls); err != nil {
			lastErr = err
			continue
		}

		key := fmt.Sprintf("partNumber_%d", m.index)
		pu, ok := urls.UploadURLs[key]
		if !ok || pu.RequestURL == "" {
			lastErr = fmt.Errorf("no signed url for %s (got %d url(s))", key, len(urls.UploadURLs))
			continue
		}

		body := io.NewSectionReader(ra, m.offset, m.length)
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, pu.RequestURL, body)
		if err != nil {
			return err
		}
		req.ContentLength = m.length
		for _, kv := range strings.Split(pu.RequestHeader, "&") {
			k, v, ok := strings.Cut(kv, "=")
			if ok && k != "" {
				req.Header.Set(k, v)
			}
		}

		resp, err := c.UploadHTTPClient().Do(req)
		if err != nil {
			lastErr = fmt.Errorf("put part %d: %w", m.index, err)
			continue
		}
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			lastErr = fmt.Errorf("put part %d: HTTP %d: %s",
				m.index, resp.StatusCode, strings.TrimSpace(string(respBody)))
			continue
		}
		return nil
	}
	return lastErr
}
