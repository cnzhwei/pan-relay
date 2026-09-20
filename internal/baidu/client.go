package baidu

import (
	"encoding/json"
	"errors"
	"fmt"
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
	DefaultWebUA       = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	DefaultCrackUA     = "netdisk;P2SP;2.2.60.26"
	DefaultAppUA       = "pan.baidu.com"
	BaiduWebBaseURL    = "https://pan.baidu.com"
	BaiduOpenAPIBaseURL = "https://pan.baidu.com/rest/2.0"
)

type Config struct {
	// Mode 1: Cookie (BDUSS)
	Cookie string `json:"cookie,omitempty"`

	// Mode 2: Official Open API
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ClientID     string `json:"client_id,omitempty"`
	ClientSecret string `json:"client_secret,omitempty"`

	filePath string `json:"-"`
}

func LoadConfig(customPath string) (*Config, error) {
	var targetPath string
	candidates := []string{}

	if customPath != "" {
		candidates = append(candidates, customPath)
	}

	candidates = append(candidates,
		"./baidu_config.json",
		"./config/baidu.json",
	)

	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, ".config", "pan-relay", "baidu.json"),
			filepath.Join(home, ".config", "wopan-cli", "baidu.json"),
			filepath.Join(home, ".baidu_config.json"),
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
			return nil, fmt.Errorf("failed to read baidu config %s: %w", targetPath, err)
		}
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("failed to parse baidu config JSON: %w", err)
		}
		cfg.filePath = targetPath
	}

	// Environment variable overrides
	if env := os.Getenv("BAIDU_COOKIE"); env != "" {
		cfg.Cookie = env
	}
	if env := os.Getenv("BAIDU_ACCESS_TOKEN"); env != "" {
		cfg.AccessToken = env
	}
	if env := os.Getenv("BAIDU_REFRESH_TOKEN"); env != "" {
		cfg.RefreshToken = env
	}

	if cfg.Cookie == "" && cfg.AccessToken == "" && cfg.RefreshToken == "" {
		return nil, fmt.Errorf("no Baidu Netdisk credentials found; provide baidu_config.json or set BAIDU_COOKIE / BAIDU_ACCESS_TOKEN")
	}

	return cfg, nil
}

func (c *Config) Save() error {
	if c.filePath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		dir := filepath.Join(home, ".config", "pan-relay")
		_ = os.MkdirAll(dir, 0700)
		c.filePath = filepath.Join(dir, "baidu.json")
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
	r.SetTimeout(25 * time.Second)
	return &Client{
		cfg:    cfg,
		client: r,
	}
}

type FileItem struct {
	FsID       uint64    `json:"fs_id"`
	Path       string    `json:"path"`
	Name       string    `json:"name"`
	Size       int64     `json:"size"`
	IsFolder   bool      `json:"is_folder"`
	Category   int       `json:"category"`
	ServerTime time.Time `json:"server_time"`
	MD5        string    `json:"md5"`
}

func (c *Client) RefreshToken() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cfg.RefreshToken == "" {
		return errors.New("no refresh_token available to refresh")
	}

	if c.cfg.ClientID == "" || c.cfg.ClientSecret == "" {
		return errors.New("clientID/clientSecret missing for token refresh")
	}

	var resp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
	}

	u := "https://openapi.baidu.com/oauth/2.0/token"
	_, err := c.client.R().
		SetResult(&resp).
		SetQueryParams(map[string]string{
			"grant_type":    "refresh_token",
			"refresh_token": c.cfg.RefreshToken,
			"client_id":     c.cfg.ClientID,
			"client_secret": c.cfg.ClientSecret,
		}).
		Get(u)

	if err != nil {
		return err
	}
	if resp.Error != "" {
		return fmt.Errorf("token refresh error: %s (%s)", resp.Error, resp.ErrorDesc)
	}
	if resp.AccessToken == "" {
		return errors.New("empty access token returned")
	}

	c.cfg.AccessToken = resp.AccessToken
	if resp.RefreshToken != "" {
		c.cfg.RefreshToken = resp.RefreshToken
	}
	_ = c.cfg.Save()

	return nil
}

func (c *Client) ListDir(dirPath string) ([]FileItem, error) {
	if dirPath == "" || !strings.HasPrefix(dirPath, "/") {
		dirPath = "/" + dirPath
	}

	// 1. Try Cookie mode if Cookie is present
	if strings.Contains(c.cfg.Cookie, "BDUSS") || len(c.cfg.Cookie) > 20 {
		return c.listDirWithCookie(dirPath)
	}

	// 2. Open API mode
	if c.cfg.AccessToken != "" {
		return c.listDirWithOpenAPI(dirPath)
	}

	return nil, errors.New("neither valid Cookie nor AccessToken configured for Baidu Netdisk")
}

func (c *Client) listDirWithCookie(dirPath string) ([]FileItem, error) {
	page := 1
	pageSize := 100
	var allItems []FileItem

	for {
		url := BaiduWebBaseURL + "/api/list"
		req := c.client.R().
			SetHeaders(map[string]string{
				"User-Agent": DefaultWebUA,
				"Cookie":     c.cfg.Cookie,
				"Referer":    "https://pan.baidu.com/disk/home",
			}).
			SetQueryParams(map[string]string{
				"dir":   dirPath,
				"num":   strconv.Itoa(pageSize),
				"page":  strconv.Itoa(page),
				"order": "time",
				"desc":  "1",
				"web":   "1",
			})

		var resp struct {
			Errno int `json:"errno"`
			List  []struct {
				FsID           uint64 `json:"fs_id"`
				ServerFilename string `json:"server_filename"`
				Path           string `json:"path"`
				Size           int64  `json:"size"`
				Isdir          int    `json:"isdir"`
				Category       int    `json:"category"`
				ServerMtime    int64  `json:"server_mtime"`
				Md5            string `json:"md5"`
			} `json:"list"`
		}

		res, err := req.SetResult(&resp).Get(url)
		if err != nil {
			return nil, err
		}

		if res.StatusCode() >= 400 {
			return nil, fmt.Errorf("baidu HTTP status %d", res.StatusCode())
		}

		if resp.Errno != 0 {
			return nil, fmt.Errorf("baidu API errno %d (invalid cookie or path)", resp.Errno)
		}

		for _, item := range resp.List {
			allItems = append(allItems, FileItem{
				FsID:       item.FsID,
				Path:       item.Path,
				Name:       item.ServerFilename,
				Size:       item.Size,
				IsFolder:   item.Isdir == 1,
				Category:   item.Category,
				ServerTime: time.Unix(item.ServerMtime, 0),
				MD5:        item.Md5,
			})
		}

		if len(resp.List) < pageSize {
			break
		}
		page++
	}

	return allItems, nil
}

func (c *Client) listDirWithOpenAPI(dirPath string) ([]FileItem, error) {
	start := 0
	limit := 1000
	var allItems []FileItem

	for {
		url := BaiduOpenAPIBaseURL + "/xpan/file"
		req := c.client.R().
			SetHeaders(map[string]string{
				"User-Agent": DefaultAppUA,
			}).
			SetQueryParams(map[string]string{
				"method":       "list",
				"dir":          dirPath,
				"web":          "web",
				"start":        strconv.Itoa(start),
				"limit":        strconv.Itoa(limit),
				"access_token": c.cfg.AccessToken,
			})

		var resp struct {
			Errno int `json:"errno"`
			List  []struct {
				FsID           uint64 `json:"fs_id"`
				ServerFilename string `json:"server_filename"`
				Path           string `json:"path"`
				Size           int64  `json:"size"`
				Isdir          int    `json:"isdir"`
				Category       int    `json:"category"`
				ServerMtime    int64  `json:"server_mtime"`
				Md5            string `json:"md5"`
			} `json:"list"`
		}

		res, err := req.SetResult(&resp).Get(url)
		if err != nil {
			return nil, err
		}

		if resp.Errno == 111 || resp.Errno == -6 { // Token expired
			if rErr := c.RefreshToken(); rErr == nil {
				return c.listDirWithOpenAPI(dirPath)
			}
		}

		if res.StatusCode() >= 400 || resp.Errno != 0 {
			return nil, fmt.Errorf("baidu openapi error %d", resp.Errno)
		}

		for _, item := range resp.List {
			allItems = append(allItems, FileItem{
				FsID:       item.FsID,
				Path:       item.Path,
				Name:       item.ServerFilename,
				Size:       item.Size,
				IsFolder:   item.Isdir == 1,
				Category:   item.Category,
				ServerTime: time.Unix(item.ServerMtime, 0),
				MD5:        item.Md5,
			})
		}

		if len(resp.List) < limit {
			break
		}
		start += limit
	}

	return allItems, nil
}

func (c *Client) GetDownloadLink(targetPath string) (string, http.Header, error) {
	if !strings.HasPrefix(targetPath, "/") {
		targetPath = "/" + targetPath
	}

	// 1. Try Cookie Crack API
	if strings.Contains(c.cfg.Cookie, "BDUSS") || len(c.cfg.Cookie) > 20 {
		url := BaiduWebBaseURL + "/api/filemetas"
		targetJSON := fmt.Sprintf("[\"%s\"]", targetPath)
		var resp struct {
			Errno int `json:"errno"`
			Info  []struct {
				FsID           uint64 `json:"fs_id"`
				Dlink          string `json:"dlink"`
				ServerFilename string `json:"server_filename"`
				Size           int64  `json:"size"`
			} `json:"info"`
		}

		req := c.client.R().
			SetHeaders(map[string]string{
				"User-Agent": DefaultCrackUA,
				"Cookie":     c.cfg.Cookie,
				"Referer":    "https://pan.baidu.com/disk/home",
			}).
			SetQueryParams(map[string]string{
				"target": targetJSON,
				"dlink":  "1",
				"web":    "5",
				"origin": "dlna",
			})

		_, err := req.SetResult(&resp).Get(url)
		if err == nil && resp.Errno == 0 && len(resp.Info) > 0 && resp.Info[0].Dlink != "" {
			h := make(http.Header)
			h.Set("User-Agent", DefaultCrackUA)
			h.Set("Cookie", c.cfg.Cookie)
			return resp.Info[0].Dlink, h, nil
		}
	}

	// 2. Try OpenAPI Mode
	if c.cfg.AccessToken != "" {
		// Needs fs_id; resolve by listing parent
		parentDir := filepath.Dir(targetPath)
		baseName := filepath.Base(targetPath)
		items, err := c.ListDir(parentDir)
		if err != nil {
			return "", nil, err
		}

		var fsID uint64
		for _, it := range items {
			if !it.IsFolder && it.Name == baseName {
				fsID = it.FsID
				break
			}
		}
		if fsID == 0 {
			return "", nil, fmt.Errorf("file %q not found in directory %s", baseName, parentDir)
		}

		url := BaiduOpenAPIBaseURL + "/xpan/multimedia"
		var resp struct {
			Errno int `json:"errno"`
			List  []struct {
				FsID  uint64 `json:"fs_id"`
				Dlink string `json:"dlink"`
			} `json:"list"`
		}

		req := c.client.R().
			SetHeaders(map[string]string{
				"User-Agent": DefaultAppUA,
			}).
			SetQueryParams(map[string]string{
				"method":       "filemetas",
				"fsids":        fmt.Sprintf("[%d]", fsID),
				"dlink":        "1",
				"access_token": c.cfg.AccessToken,
			})

		_, err = req.SetResult(&resp).Get(url)
		if err == nil && resp.Errno == 0 && len(resp.List) > 0 && resp.List[0].Dlink != "" {
			dlink := fmt.Sprintf("%s&access_token=%s", resp.List[0].Dlink, c.cfg.AccessToken)
			h := make(http.Header)
			h.Set("User-Agent", DefaultAppUA)
			return dlink, h, nil
		}
	}

	return "", nil, fmt.Errorf("failed to obtain download link for %s", targetPath)
}

func (c *Client) Mkdir(parentPath, name string) error {
	fullPath := filepath.Join(parentPath, name)
	if !strings.HasPrefix(fullPath, "/") {
		fullPath = "/" + fullPath
	}

	if c.cfg.AccessToken != "" {
		url := BaiduOpenAPIBaseURL + "/xpan/file?method=create"
		req := c.client.R().
			SetFormData(map[string]string{
				"path":         fullPath,
				"isdir":        "1",
				"size":         "0",
				"uploadid":     "N1-MTAuMS42Ljc1",
				"block_list":   "[]",
				"access_token": c.cfg.AccessToken,
			})
		var resp struct {
			Errno int `json:"errno"`
		}
		_, err := req.SetResult(&resp).Post(url)
		if err != nil {
			return err
		}
		if resp.Errno != 0 && resp.Errno != -8 { // -8 is already exists
			return fmt.Errorf("baidu mkdir errno %d", resp.Errno)
		}
		return nil
	}

	if c.cfg.Cookie != "" {
		url := BaiduWebBaseURL + "/api/create"
		req := c.client.R().
			SetHeaders(map[string]string{
				"User-Agent": DefaultWebUA,
				"Cookie":     c.cfg.Cookie,
				"Referer":    "https://pan.baidu.com/disk/home",
			}).
			SetFormData(map[string]string{
				"path":  fullPath,
				"isdir": "1",
				"size":  "0",
			})
		var resp struct {
			Errno int `json:"errno"`
		}
		_, err := req.SetResult(&resp).Post(url)
		if err != nil {
			return err
		}
		if resp.Errno != 0 && resp.Errno != -8 {
			return fmt.Errorf("baidu mkdir errno %d", resp.Errno)
		}
		return nil
	}

	return errors.New("no valid auth method for mkdir")
}

func (c *Client) Delete(targetPath string) error {
	if !strings.HasPrefix(targetPath, "/") {
		targetPath = "/" + targetPath
	}

	fileListJSON := fmt.Sprintf("[\"%s\"]", targetPath)

	if c.cfg.AccessToken != "" {
		url := BaiduOpenAPIBaseURL + "/xpan/file?method=filemanager&opera=delete"
		req := c.client.R().
			SetFormData(map[string]string{
				"filelist":     fileListJSON,
				"access_token": c.cfg.AccessToken,
			})
		var resp struct {
			Errno int `json:"errno"`
		}
		_, err := req.SetResult(&resp).Post(url)
		if err != nil {
			return err
		}
		if resp.Errno != 0 {
			return fmt.Errorf("baidu delete errno %d", resp.Errno)
		}
		return nil
	}

	if c.cfg.Cookie != "" {
		url := BaiduWebBaseURL + "/api/filemanager?opera=delete"
		req := c.client.R().
			SetHeaders(map[string]string{
				"User-Agent": DefaultWebUA,
				"Cookie":     c.cfg.Cookie,
				"Referer":    "https://pan.baidu.com/disk/home",
			}).
			SetFormData(map[string]string{
				"filelist": fileListJSON,
			})
		var resp struct {
			Errno int `json:"errno"`
		}
		_, err := req.SetResult(&resp).Post(url)
		if err != nil {
			return err
		}
		if resp.Errno != 0 {
			return fmt.Errorf("baidu delete errno %d", resp.Errno)
		}
		return nil
	}

	return errors.New("no valid auth method for delete")
}

func (c *Client) Config() *Config {
	return c.cfg
}
