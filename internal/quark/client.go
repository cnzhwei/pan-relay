package quark

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-resty/resty/v2"
)

const (
	DefaultUA      = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) quark-cloud-drive/2.5.20 Chrome/100.0.4896.160 Electron/18.3.5.4-b478491100 Safari/537.36 Channel/pckk_other_ch"
	DefaultReferer = "https://pan.quark.cn"
	DefaultOrigin  = "https://pan.quark.cn"
	DefaultAPI     = "https://drive.quark.cn/1/clouddrive"
)

type Config struct {
	Cookie   string `json:"cookie"`
	filePath string `json:"-"`
}

func LoadConfig(customPath string) (*Config, error) {
	var targetPath string
	candidates := []string{}

	if customPath != "" {
		candidates = append(candidates, customPath)
	}

	candidates = append(candidates,
		"./quark_config.json",
		"./config/quark.json",
	)

	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, ".config", "wopan-cli", "quark.json"),
			filepath.Join(home, ".quark_config.json"),
		)
	}

	for _, p := range candidates {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			targetPath = p
			break
		}
	}

	cfg := &Config{}
	if targetPath != "" {
		data, err := os.ReadFile(targetPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read quark config file %s: %w", targetPath, err)
		}
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("failed to parse quark config JSON in %s: %w", targetPath, err)
		}
		cfg.filePath = targetPath
	}

	if env := os.Getenv("QUARK_COOKIE"); env != "" {
		cfg.Cookie = env
	}

	if strings.TrimSpace(cfg.Cookie) == "" {
		return nil, fmt.Errorf("no Quark cookie found; provide quark_config.json or set QUARK_COOKIE environment variable")
	}

	return cfg, nil
}

func (c *Config) Save() error {
	if c.filePath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		dir := filepath.Join(home, ".config", "wopan-cli")
		_ = os.MkdirAll(dir, 0700)
		c.filePath = filepath.Join(dir, "quark.json")
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmpFile := c.filePath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmpFile, c.filePath)
}

func (c *Config) FilePath() string {
	return c.filePath
}

type Client struct {
	cfg    *Config
	client *resty.Client
	mu     sync.Mutex
}

func NewClient(cfg *Config) *Client {
	r := resty.New()
	r.SetTimeout(20 * time.Second)
	return &Client{
		cfg:    cfg,
		client: r,
	}
}

type FileItem struct {
	FID        string    `json:"fid"`
	Name       string    `json:"name"`
	Size       int64     `json:"size"`
	IsFolder   bool      `json:"is_folder"`
	Category   int       `json:"category"`
	CreateTime time.Time `json:"create_time"`
	UpdateTime time.Time `json:"update_time"`
}

type SortResp struct {
	Status   int    `json:"status"`
	Code     int    `json:"code"`
	Message  string `json:"message"`
	Metadata struct {
		Total int `json:"_total"`
		Page  int `json:"_page"`
		Size  int `json:"_size"`
	} `json:"metadata"`
	Data struct {
		List []struct {
			Fid        string `json:"fid"`
			FileName   string `json:"file_name"`
			Size       int64  `json:"size"`
			FileType   int    `json:"file_type"`
			Category   int    `json:"category"`
			CreatedAt  int64  `json:"created_at"`
			UpdatedAt  int64  `json:"updated_at"`
		} `json:"list"`
	} `json:"data"`
}

type DownResp struct {
	Status  int    `json:"status"`
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    []struct {
		Fid         string `json:"fid"`
		FileName    string `json:"file_name"`
		Size        int64  `json:"size"`
		DownloadURL string `json:"download_url"`
	} `json:"data"`
}

func (c *Client) request(pathname string, method string, query map[string]string, body interface{}, result interface{}) error {
	c.mu.Lock()
	cookieVal := c.cfg.Cookie
	c.mu.Unlock()

	url := DefaultAPI + pathname
	req := c.client.R()
	req.SetHeaders(map[string]string{
		"User-Agent":   DefaultUA,
		"Cookie":       cookieVal,
		"Referer":      DefaultReferer,
		"Origin":       DefaultOrigin,
		"Accept":       "application/json, text/plain, */*",
		"Content-Type": "application/json;charset=UTF-8",
	})
	req.SetQueryParams(map[string]string{
		"pr": "ucpro",
		"fr": "pc",
	})
	if len(query) > 0 {
		req.SetQueryParams(query)
	}
	if body != nil {
		req.SetBody(body)
	}
	if result != nil {
		req.SetResult(result)
	}

	res, err := req.Execute(method, url)
	if err != nil {
		return err
	}

	// Rotate cookies if server returned updated __puus / __pus
	hasCookieUpdate := false
	c.mu.Lock()
	for _, ck := range res.Cookies() {
		if ck.Name == "__puus" || ck.Name == "__pus" {
			c.cfg.Cookie = updateCookieStr(c.cfg.Cookie, ck.Name, ck.Value)
			hasCookieUpdate = true
		}
	}
	if hasCookieUpdate {
		_ = c.cfg.Save()
	}
	c.mu.Unlock()

	if res.StatusCode() >= 400 {
		return fmt.Errorf("quark API returned HTTP %d: %s", res.StatusCode(), res.String())
	}

	return nil
}

func updateCookieStr(oldCookie, key, newVal string) string {
	parts := strings.Split(oldCookie, ";")
	found := false
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if strings.HasPrefix(p, key+"=") {
			parts[i] = key + "=" + newVal
			found = true
			break
		}
	}
	if !found {
		parts = append(parts, key+"="+newVal)
	}
	return strings.Join(parts, "; ")
}

func (c *Client) ListDir(parentFID string) ([]FileItem, error) {
	if parentFID == "" {
		parentFID = "0"
	}

	var allFiles []FileItem
	page := 1
	size := 100

	for {
		query := map[string]string{
			"pdir_fid":             parentFID,
			"_size":                strconv.Itoa(size),
			"_page":                strconv.Itoa(page),
			"_fetch_total":         "1",
			"fetch_all_file":       "1",
			"fetch_risk_file_name": "1",
			"_sort":                "file_type:asc,updated_at:desc",
		}

		var resp SortResp
		err := c.request("/file/sort", http.MethodGet, query, nil, &resp)
		if err != nil {
			return nil, err
		}

		if resp.Code != 0 {
			return nil, fmt.Errorf("quark error %d: %s", resp.Code, resp.Message)
		}

		for _, item := range resp.Data.List {
			name := html.UnescapeString(item.FileName)
			isDir := (item.FileType == 0)
			allFiles = append(allFiles, FileItem{
				FID:        item.Fid,
				Name:       name,
				Size:       item.Size,
				IsFolder:   isDir,
				Category:   item.Category,
				CreateTime: time.UnixMilli(item.CreatedAt),
				UpdateTime: time.UnixMilli(item.UpdatedAt),
			})
		}

		if page*size >= resp.Metadata.Total || len(resp.Data.List) == 0 {
			break
		}
		page++
	}

	return allFiles, nil
}

func (c *Client) ResolvePath(pathStr string) (string, error) {
	pathStr = strings.TrimSpace(pathStr)
	if pathStr == "" || pathStr == "/" || pathStr == "." {
		return "0", nil
	}

	if !strings.Contains(pathStr, "/") {
		return pathStr, nil
	}

	parts := strings.Split(strings.Trim(pathStr, "/"), "/")
	currentFID := "0"

	for _, part := range parts {
		if part == "" {
			continue
		}
		items, err := c.ListDir(currentFID)
		if err != nil {
			return "", err
		}

		found := false
		for _, item := range items {
			if item.IsFolder && item.Name == part {
				currentFID = item.FID
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("path component %q not found in directory FID %s", part, currentFID)
		}
	}

	return currentFID, nil
}

func (c *Client) GetDownloadLink(fileID string) (string, http.Header, error) {
	body := map[string]interface{}{
		"fids": []string{fileID},
	}
	var resp DownResp
	err := c.request("/file/download", http.MethodPost, nil, body, &resp)
	if err != nil {
		return "", nil, err
	}

	if resp.Code != 0 {
		return "", nil, fmt.Errorf("quark download error %d: %s", resp.Code, resp.Message)
	}

	if len(resp.Data) == 0 || resp.Data[0].DownloadURL == "" {
		return "", nil, errors.New("no download URL returned from Quark API")
	}

	header := make(http.Header)
	header.Set("User-Agent", DefaultUA)
	header.Set("Referer", DefaultReferer)
	header.Set("Cookie", c.cfg.Cookie)

	return resp.Data[0].DownloadURL, header, nil
}

func (c *Client) Config() *Config {
	return c.cfg
}
