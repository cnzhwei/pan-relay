// Package ctyun implements the Tianyi Cloud Drive (天翼云盘 / 189Cloud) driver
// for pan-relay, based on the PC-client private API used by 189CloudPC.
//
// Authentication chain:
//
//	refresh_token --(7d)--> access_token --> sessionKey/sessionSecret
//
// Every API call after login must be signed with HMAC-SHA1 over
// "SessionKey=..&Operate=..&RequestURI=..&Date=..[&params=..]" using
// sessionSecret. Sensitive parameters are AES-128-ECB encrypted with the first
// 16 bytes of sessionSecret.
//
// IMPORTANT: a refresh_token is single-active. Refreshing it invalidates every
// previously issued access_token, so never run two processes against the same
// refresh_token concurrently.
package ctyun

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-resty/resty/v2"
)

const (
	// AppID is the official PC client application id. Tokens, sessions and QR
	// logins are all scoped to it, so it must match everywhere.
	AppID      = "9317140619"
	ClientType = "10020"
	Version    = "7.2.4.0"
	ChannelID  = "web_cloud.189.cn"

	WebURL        = "https://cloud.189.cn"
	AuthURL       = "https://open.e.189.cn"
	APIURL        = "https://api.cloud.189.cn"
	UploadURL     = "https://upload.cloud.189.cn"
	DefaultRootID = "-11" // personal cloud root

	DeviceIDCookie = "DEVICEID"
)

// Config is the on-disk credential file (ctyun_config.json).
type Config struct {
	RefreshToken string `json:"refresh_token"`
	AccessToken  string `json:"access_token,omitempty"`
	FamilyID     string `json:"family_id,omitempty"`
	RootFolderID string `json:"root_folder_id,omitempty"`
	// Type selects personal (个人云) or family (家庭云) space.
	Type string `json:"type,omitempty"`

	filePath string `json:"-"`
}

// LoadConfig resolves ctyun_config.json from the usual locations.
func LoadConfig(customPath string) (*Config, error) {
	var targetPath string
	candidates := make([]string, 0, 8)

	if customPath != "" {
		candidates = append(candidates, customPath)
	}
	candidates = append(candidates, "./ctyun_config.json", "./config/ctyun.json")

	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, ".config", "pan-relay", "ctyun.json"),
			filepath.Join(home, ".config", "pan-relay", "189.json"),
			filepath.Join(home, ".ctyun_config.json"),
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
			return nil, fmt.Errorf("failed to read ctyun config %s: %w", targetPath, err)
		}
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("failed to parse ctyun config %s: %w", targetPath, err)
		}
		cfg.filePath = targetPath
	}

	if env := os.Getenv("CTYUN_REFRESH_TOKEN"); env != "" {
		cfg.RefreshToken = env
	}
	if cfg.RefreshToken == "" {
		return nil, fmt.Errorf("no Tianyi refresh_token found; provide ctyun_config.json or set CTYUN_REFRESH_TOKEN")
	}
	if cfg.Type == "" {
		cfg.Type = "personal"
	}
	if cfg.RootFolderID == "" {
		if cfg.Type == "family" {
			cfg.RootFolderID = ""
		} else {
			cfg.RootFolderID = DefaultRootID
		}
	}
	return cfg, nil
}

// Save persists the config, creating ~/.config/pan-relay/ctyun.json when needed.
func (c *Config) Save() error {
	if c.filePath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		dir := filepath.Join(home, ".config", "pan-relay")
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		c.filePath = filepath.Join(dir, "ctyun.json")
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.filePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, c.filePath)
}

// FilePath reports where the config is stored ("" when not yet saved).
func (c *Config) FilePath() string { return c.filePath }

// IsFamily reports whether this config targets the family cloud space.
func (c *Config) IsFamily() bool { return c.Type == "family" }

// FileItem is a unified directory entry.
type FileItem struct {
	ID       string
	Name     string
	Size     int64
	IsFolder bool
	Modified time.Time
	ParentID string
	MD5      string
	// Raw holds the original file id as returned by the API.
	Raw string
}

// Client is a signed Tianyi Cloud Drive client.
type Client struct {
	cfg *Config
	hc  *resty.Client

	// Endpoint overrides. Empty means use the production constants; tests set
	// them to a local server so no real traffic is generated.
	authURL   string
	apiURL    string
	uploadURL string

	mu         sync.Mutex
	accessTok  string
	expiresAt  time.Time
	sessionKey string
	sessSecret string
	famKey     string
	famSecret  string
	loginName  string
}

// NewClient builds a client. No network calls are made until first use.
func NewClient(cfg *Config) *Client {
	r := resty.New()
	r.SetTimeout(60 * time.Second)
	r.SetHeader("Accept", "application/json;charset=UTF-8")
	r.SetHeader("Referer", WebURL)
	r.SetHeader("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	return &Client{cfg: cfg, hc: r, accessTok: cfg.AccessToken,
		authURL: AuthURL, apiURL: APIURL, uploadURL: UploadURL}
}

// SetEndpoints overrides the API hosts. Intended for tests and mirrors.
//
// The endpoint fields are intentionally read without taking c.mu: callers such
// as getSessionLocked already hold it, and taking it again would self-deadlock.
// SetEndpoints must therefore be called before the client is used concurrently.
func (c *Client) SetEndpoints(auth, api, upload string) {
	if auth != "" {
		c.authURL = auth
	}
	if api != "" {
		c.apiURL = api
	}
	if upload != "" {
		c.uploadURL = upload
	}
}

func (c *Client) authBase() string {
	if c.authURL == "" {
		return AuthURL
	}
	return c.authURL
}

func (c *Client) apiBase() string {
	if c.apiURL == "" {
		return APIURL
	}
	return c.apiURL
}

func (c *Client) uploadBase() string {
	if c.uploadURL == "" {
		return UploadURL
	}
	return c.uploadURL
}

// Config returns the underlying config.
func (c *Client) Config() *Config { return c.cfg }

// LoginName returns the account name reported by the session endpoint.
func (c *Client) LoginName() string { return c.loginName }

// FamilyID returns the family cloud id, when configured.
func (c *Client) FamilyID() string { return c.cfg.FamilyID }

// RootID returns the configured root folder id.
func (c *Client) RootID() string { return c.cfg.RootFolderID }

// -----------------------------------------------------------------------------
// Authentication
// -----------------------------------------------------------------------------

type refreshResp struct {
	Result       int    `json:"result"`
	Msg          string `json:"msg"`
	ExpiresIn    int64  `json:"expiresIn"`
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
}

type sessionResp struct {
	// The API returns res_code as a number on success (0) but as a string on
	// failure ("UserInvalidOpenToken"), so it must not be typed as int.
	ResCode    any    `json:"res_code"`
	ResMessage string `json:"res_message"`

	LoginName     string `json:"loginName"`
	SessionKey    string `json:"sessionKey"`
	SessionSecret string `json:"sessionSecret"`

	FamilySessionKey    string `json:"familySessionKey"`
	FamilySessionSecret string `json:"familySessionSecret"`
}

// RefreshToken exchanges the configured refresh_token for a fresh access_token.
// The server may rotate the refresh_token; it is persisted when it changes.
func (c *Client) RefreshToken(ctx context.Context) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.refreshTokenLocked(ctx)
}

func (c *Client) refreshTokenLocked(ctx context.Context) (int64, error) {
	resp, err := c.hc.R().SetContext(ctx).
		SetFormData(map[string]string{
			"clientId":     AppID,
			"refreshToken": c.cfg.RefreshToken,
			"grantType":    "refresh_token",
			"format":       "json",
		}).
		Post(c.authBase() + "/api/oauth2/refreshToken.do")
	if err != nil {
		return 0, fmt.Errorf("refresh token request failed: %w", err)
	}

	var r refreshResp
	if err := json.Unmarshal(resp.Body(), &r); err != nil {
		return 0, fmt.Errorf("refresh token: unexpected response: %w", err)
	}
	if r.Result != 0 || r.AccessToken == "" {
		msg := r.Msg
		if msg == "" {
			msg = "unknown error"
		}
		return 0, fmt.Errorf("refresh token rejected (result=%d): %s", r.Result, msg)
	}

	c.accessTok = r.AccessToken
	c.sessionKey, c.sessSecret = "", ""
	c.famKey, c.famSecret = "", ""
	if r.ExpiresIn > 0 {
		c.expiresAt = time.Now().Add(time.Duration(r.ExpiresIn) * time.Second)
	} else {
		c.expiresAt = time.Now().Add(6 * 24 * time.Hour)
	}

	c.cfg.AccessToken = r.AccessToken
	if r.RefreshToken != "" {
		c.cfg.RefreshToken = r.RefreshToken
	}
	if err := c.cfg.Save(); err != nil {
		return r.ExpiresIn, fmt.Errorf("token refreshed but config could not be saved: %w", err)
	}
	return r.ExpiresIn, nil
}

// EnsureSession makes sure a usable sessionKey/sessionSecret pair exists.
//
// The refresh chain is single-active: refreshing twice concurrently would mint
// two access tokens and invalidate the earlier one, so the whole check-and-login
// sequence runs under c.mu with a double-check inside the lock. Network calls
// are made while holding the lock on purpose — only one goroutine may refresh.
func (c *Client) EnsureSession(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ensureSessionLocked(ctx)
}

func (c *Client) ensureSessionLocked(ctx context.Context) error {
	if c.sessionKey != "" && c.sessSecret != "" {
		return nil
	}

	if c.accessTok == "" {
		if _, err := c.refreshTokenLocked(ctx); err != nil {
			return err
		}
	}
	if err := c.getSessionLocked(ctx); err != nil {
		// An invalid access token (UserInvalidOpenToken) is recoverable: the
		// token may have been superseded, so refresh and retry once.
		if _, rerr := c.refreshTokenLocked(ctx); rerr != nil {
			return fmt.Errorf("%w (refresh retry failed: %v)", err, rerr)
		}
		if err := c.getSessionLocked(ctx); err != nil {
			return err
		}
	}
	return nil
}

// getSessionLocked exchanges the current access token for a session pair.
// The caller must hold c.mu.
func (c *Client) getSessionLocked(ctx context.Context) error {
	at := c.accessTok

	resp, err := c.hc.R().SetContext(ctx).
		SetQueryParams(map[string]string{
			"appId":       AppID,
			"clientType":  ClientType,
			"version":     Version,
			"channelId":   ChannelID,
			"returnType":  "JSON",
			"accessToken": at,
		}).
		Get(c.apiBase() + "/getSessionForPC.action")
	if err != nil {
		return fmt.Errorf("getSessionForPC request failed: %w", err)
	}

	var s sessionResp
	if err := json.Unmarshal(resp.Body(), &s); err != nil {
		return fmt.Errorf("getSessionForPC: unexpected response: %w", err)
	}
	if !isZeroResCode(s.ResCode) || s.SessionKey == "" {
		return fmt.Errorf("getSessionForPC rejected (res_code=%v): %s", s.ResCode, s.ResMessage)
	}

	// The caller (ensureSessionLocked) already holds c.mu, so assigning
	// directly is required — taking the lock here would self-deadlock.
	c.sessionKey, c.sessSecret = s.SessionKey, s.SessionSecret
	c.famKey, c.famSecret = s.FamilySessionKey, s.FamilySessionSecret
	if s.LoginName != "" {
		c.loginName = s.LoginName
	}
	return nil
}

// -----------------------------------------------------------------------------
// Request signing
// -----------------------------------------------------------------------------

func clientSuffix() map[string]string {
	return map[string]string{
		"clientType": ClientType,
		"version":    Version,
		"channelId":  ChannelID,
		"rand":       fmt.Sprintf("%d_%d", time.Now().UnixNano()%100000, time.Now().UnixNano()%10000000000),
	}
}

var urlPathPattern = regexp.MustCompile(`://[^/]+((/[^/\s?#]+)*)`)

// requestPath extracts the path component used in the HMAC signature.
// It must be the path only (no host, no query) and must return "/" for a
// bare-origin URL, otherwise the signature will not match the server's.
func requestPath(fullURL string) string {
	if u, err := url.Parse(fullURL); err == nil {
		if u.Path == "" {
			return "/"
		}
		return u.Path
	}
	// Fallback for inputs url.Parse rejects.
	m := urlPathPattern.FindStringSubmatch(fullURL)
	if len(m) < 2 || m[1] == "" {
		return "/"
	}
	return m[1]
}

func httpDate() string {
	return time.Now().UTC().Format(http.TimeFormat)
}

func sign(secret, sessionKey, operate, fullURL, date, paramsHex string) string {
	data := fmt.Sprintf("SessionKey=%s&Operate=%s&RequestURI=%s&Date=%s",
		sessionKey, operate, requestPath(fullURL), date)
	if paramsHex != "" {
		data += "&params=" + paramsHex
	}
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write([]byte(data))
	return strings.ToUpper(hex.EncodeToString(mac.Sum(nil)))
}

func pkcs7Pad(b []byte, blockSize int) []byte {
	padding := blockSize - len(b)%blockSize
	return append(b, bytes.Repeat([]byte{byte(padding)}, padding)...)
}

// aesECBHex encrypts data with AES-128-ECB and returns upper-case hex.
func aesECBHex(data, key string) (string, error) {
	block, err := aes.NewCipher([]byte(key))
	if err != nil {
		return "", err
	}
	padded := pkcs7Pad([]byte(data), block.BlockSize())
	out := make([]byte, len(padded))
	bs := block.BlockSize()
	for src, dst := padded, out; len(src) > 0; src, dst = src[bs:], dst[bs:] {
		block.Encrypt(dst[:bs], src[:bs])
	}
	return strings.ToUpper(hex.EncodeToString(out)), nil
}

// encodeParams sorts keys and produces "k1=v1&k2=v2" (values are not escaped,
// matching the reference implementation).
func encodeParams(params map[string]string) string {
	if len(params) == 0 {
		return ""
	}
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(params[k])
	}
	return b.String()
}

// RequestOptions describes a signed call.
type RequestOptions struct {
	// Plain are query parameters that are NOT part of the signature.
	Plain map[string]string
	// Encrypted are parameters that get AES-encrypted into ?params= and are
	// covered by the signature.
	Encrypted map[string]string
	// Form is posted as application/x-www-form-urlencoded.
	Form map[string]string
}

// do performs a signed API request and unmarshals the JSON body into result.
func (c *Client) do(ctx context.Context, method, fullURL string, opt RequestOptions, result interface{}) error {
	if err := c.EnsureSession(ctx); err != nil {
		return err
	}
	return c.doWithRetry(ctx, method, fullURL, opt, result, true)
}

func (c *Client) doWithRetry(ctx context.Context, method, fullURL string, opt RequestOptions, result interface{}, allowRetry bool) error {
	c.mu.Lock()
	sk, ss := c.sessionKey, c.sessSecret
	// Family cloud endpoints authenticate with their own session pair; using
	// the personal one produces a signature mismatch on every family call.
	if c.cfg.IsFamily() && c.famKey != "" && c.famSecret != "" {
		sk, ss = c.famKey, c.famSecret
	}
	c.mu.Unlock()

	if len(ss) < 16 {
		return fmt.Errorf("invalid session secret: got %d bytes, need at least 16", len(ss))
	}

	paramsHex := ""
	if len(opt.Encrypted) > 0 {
		var err error
		paramsHex, err = aesECBHex(encodeParams(opt.Encrypted), ss[:16])
		if err != nil {
			return fmt.Errorf("failed to encrypt params: %w", err)
		}
	}

	date := httpDate()
	headers := map[string]string{
		"Date":         date,
		"SessionKey":   sk,
		"Signature":    sign(ss, sk, method, fullURL, date, paramsHex),
		"X-Request-ID": fmt.Sprintf("%d-%d", time.Now().UnixNano(), os.Getpid()),
	}

	query := clientSuffix()
	query["returnType"] = "JSON"
	for k, v := range opt.Plain {
		query[k] = v
	}
	if paramsHex != "" {
		query["params"] = paramsHex
	}

	req := c.hc.R().SetContext(ctx).SetQueryParams(query).SetHeaders(headers)
	if len(opt.Form) > 0 {
		req = req.SetFormData(opt.Form)
	}

	resp, err := req.Execute(method, fullURL)
	if err != nil {
		return fmt.Errorf("%s %s failed: %w", method, requestPath(fullURL), err)
	}

	body := resp.Body()
	// Detect expired/rotated session and recover once.
	if allowRetry && (bytes.Contains(body, []byte("InvalidSessionKey")) ||
		bytes.Contains(body, []byte("userSessionBO is null")) ||
		bytes.Contains(body, []byte("UserInvalidOpenToken"))) {
		// Take the lock before clearing state so a concurrent recovery cannot
		// start a second, competing refresh.
		c.mu.Lock()
		c.sessionKey, c.sessSecret = "", ""
		rerr := c.ensureSessionLocked(ctx)
		c.mu.Unlock()
		if rerr != nil {
			return fmt.Errorf("session expired and re-login failed: %w", rerr)
		}
		return c.doWithRetry(ctx, method, fullURL, opt, result, false)
	}

	if result != nil {
		if err := json.Unmarshal(body, result); err != nil {
			return fmt.Errorf("%s %s: cannot parse response: %w (body: %s)",
				method, requestPath(fullURL), err, truncate(string(body), 200))
		}
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// -----------------------------------------------------------------------------
// Filesystem operations
// -----------------------------------------------------------------------------

type listResp struct {
	ResCode    any    `json:"res_code"`
	ResMessage string `json:"res_message"`

	FileListAO struct {
		Count      int         `json:"count"`
		FileList   []apiFile   `json:"fileList"`
		FolderList []apiFolder `json:"folderList"`
	} `json:"fileListAO"`
}

type apiFile struct {
	ID         apiID  `json:"id"`
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	MD5        string `json:"md5"`
	ParentID   apiID  `json:"parentId"`
	LastOpTime string `json:"lastOpTime"`
	CreateDate string `json:"createDate"`
}

type apiFolder struct {
	ID         apiID  `json:"id"`
	Name       string `json:"name"`
	ParentID   apiID  `json:"parentId"`
	LastOpTime string `json:"lastOpTime"`
	CreateDate string `json:"createDate"`
}

// apiID handles ids that the API returns either as numbers or strings.
type apiID string

func (a *apiID) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), "\"")
	*a = apiID(s)
	return nil
}

func (a apiID) String() string { return string(a) }

func parseAPITime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	layouts := []string{
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
		time.RFC3339,
	}
	for _, l := range layouts {
		if t, err := time.ParseInLocation(l, s, time.Local); err == nil {
			return t
		}
	}
	return time.Time{}
}

// ListDir lists one directory page by page. folderID empty means root.
func (c *Client) ListDir(ctx context.Context, folderID string) ([]FileItem, error) {
	if folderID == "" {
		folderID = c.cfg.RootFolderID
	}
	if folderID == "" {
		folderID = DefaultRootID
	}

	const pageSize = 100
	var out []FileItem
	for page := 1; ; page++ {
		var r listResp
		err := c.do(ctx, http.MethodGet, c.apiBase()+"/listFiles.action", RequestOptions{
			Plain: map[string]string{
				"folderId":   folderID,
				"fileType":   "0",
				"mediaAttr":  "0",
				"iconOption": "5",
				"pageNum":    strconv.Itoa(page),
				"pageSize":   strconv.Itoa(pageSize),
				"recursive":  "0",
				"orderBy":    "filename",
				"descending": "false",
			},
		}, &r)
		if err != nil {
			return nil, err
		}

		for _, f := range r.FileListAO.FolderList {
			out = append(out, FileItem{
				ID:       f.ID.String(),
				Name:     f.Name,
				IsFolder: true,
				Modified: parseAPITime(f.LastOpTime),
				ParentID: f.ParentID.String(),
			})
		}
		for _, f := range r.FileListAO.FileList {
			out = append(out, FileItem{
				ID:       f.ID.String(),
				Name:     f.Name,
				Size:     f.Size,
				MD5:      f.MD5,
				Modified: parseAPITime(f.LastOpTime),
				ParentID: f.ParentID.String(),
			})
		}

		if r.FileListAO.Count < pageSize {
			break
		}
		if len(r.FileListAO.FileList) == 0 && len(r.FileListAO.FolderList) == 0 {
			break
		}
	}
	return out, nil
}

// ResolveDir walks a "/a/b/c" style path and returns the folder id.
func (c *Client) ResolveDir(ctx context.Context, path string) (string, error) {
	path = strings.Trim(strings.TrimSpace(path), "/")
	if path == "" || path == "." {
		if c.cfg.RootFolderID != "" {
			return c.cfg.RootFolderID, nil
		}
		return DefaultRootID, nil
	}

	current := c.cfg.RootFolderID
	if current == "" {
		current = DefaultRootID
	}
	for _, seg := range strings.Split(path, "/") {
		if seg == "" {
			continue
		}
		items, err := c.ListDir(ctx, current)
		if err != nil {
			return "", err
		}
		found := false
		for _, it := range items {
			if it.IsFolder && it.Name == seg {
				current = it.ID
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("directory %q not found (segment %q)", path, seg)
		}
	}
	return current, nil
}

// FindFile resolves a "/path/to/file.ext" style path to a FileItem.
func (c *Client) FindFile(ctx context.Context, path string) (*FileItem, error) {
	clean := strings.TrimSpace(path)
	if clean == "" {
		return nil, fmt.Errorf("empty path")
	}
	clean = strings.TrimPrefix(clean, "/")

	name := clean
	parentID := c.cfg.RootFolderID
	if parentID == "" {
		parentID = DefaultRootID
	}
	if idx := strings.LastIndex(clean, "/"); idx >= 0 {
		dirPart := clean[:idx]
		name = clean[idx+1:]
		var err error
		parentID, err = c.ResolveDir(ctx, dirPart)
		if err != nil {
			return nil, err
		}
	}

	items, err := c.ListDir(ctx, parentID)
	if err != nil {
		return nil, err
	}
	for i := range items {
		if items[i].Name == name && !items[i].IsFolder {
			return &items[i], nil
		}
	}
	return nil, fmt.Errorf("file %q not found", path)
}

type downloadURLResp struct {
	ResCode    any    `json:"res_code"`
	ResMessage string `json:"res_message"`

	FileDownloadURL string `json:"fileDownloadUrl"`
}

// GetDownloadURL returns a directly usable CDN URL for the given file id.
func (c *Client) GetDownloadURL(ctx context.Context, fileID string) (string, error) {
	var r downloadURLResp
	err := c.do(ctx, http.MethodGet, c.apiBase()+"/getFileDownloadUrl.action", RequestOptions{
		Plain: map[string]string{
			"fileId": fileID,
			"dt":     "3",
			"flag":   "1",
		},
	}, &r)
	if err != nil {
		return "", err
	}
	raw := strings.ReplaceAll(r.FileDownloadURL, "&amp;", "&")
	raw = strings.Replace(raw, "http://", "https://", 1)
	if raw == "" {
		return "", fmt.Errorf("empty download url (res_message=%s)", r.ResMessage)
	}

	// The first URL is a redirector; resolve it to the real CDN endpoint.
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return raw, nil
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := client.Do(req)
	if err != nil {
		return raw, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusMovedPermanently ||
		resp.StatusCode == http.StatusTemporaryRedirect {
		if loc := resp.Header.Get("Location"); loc != "" {
			return loc, nil
		}
	}
	return raw, nil
}

type folderResp struct {
	ResCode    any    `json:"res_code"`
	ResMessage string `json:"res_message"`

	ID   apiID  `json:"id"`
	Name string `json:"name"`
}

// Mkdir creates a folder and returns its id.
func (c *Client) Mkdir(ctx context.Context, parentID, name string) (string, error) {
	if parentID == "" {
		parentID = c.cfg.RootFolderID
	}
	if parentID == "" {
		parentID = DefaultRootID
	}
	var r folderResp
	err := c.do(ctx, http.MethodPost, c.apiBase()+"/createFolder.action", RequestOptions{
		Plain: map[string]string{
			"folderName":     name,
			"relativePath":   "",
			"parentFolderId": parentID,
		},
	}, &r)
	if err != nil {
		return "", err
	}
	if r.ID.String() == "" {
		return "", fmt.Errorf("createFolder returned no id (res_message=%s)", r.ResMessage)
	}
	return r.ID.String(), nil
}

type batchTaskResp struct {
	ResCode    any    `json:"res_code"`
	ResMessage string `json:"res_message"`
	TaskID     string `json:"taskId"`
}

type batchTaskStateResp struct {
	ResCode    any    `json:"res_code"`
	ResMessage string `json:"res_message"`

	TaskStatus     int `json:"taskStatus"` // 1 init, 2 conflict, 3 running, 4 done
	FailedCount    int `json:"failedCount"`
	SuccessedCount int `json:"successedCount"`
	SkipCount      int `json:"skipCount"`
	Process        int `json:"process"`
}

// Remove deletes a file or folder through the batch task API.
func (c *Client) Remove(ctx context.Context, item FileItem) error {
	isFolder := 0
	if item.IsFolder {
		isFolder = 1
	}
	infos, err := json.Marshal([]map[string]interface{}{{
		"fileId":   item.ID,
		"fileName": item.Name,
		"isFolder": isFolder,
	}})
	if err != nil {
		return err
	}

	var r batchTaskResp
	err = c.do(ctx, http.MethodPost, c.apiBase()+"/batch/createBatchTask.action", RequestOptions{
		Form: map[string]string{
			"type":      "DELETE",
			"taskInfos": string(infos),
		},
	}, &r)
	if err != nil {
		return err
	}
	if r.TaskID == "" {
		return fmt.Errorf("delete task was not created (res_message=%s)", r.ResMessage)
	}

	// Poll the task until it reaches the terminal state (4 = done).
	for i := 0; i < 40; i++ {
		time.Sleep(300 * time.Millisecond)
		var st batchTaskStateResp
		err := c.do(ctx, http.MethodPost, c.apiBase()+"/batch/checkBatchTask.action", RequestOptions{
			Form: map[string]string{"type": "DELETE", "taskId": r.TaskID},
		}, &st)
		if err != nil {
			return err
		}
		if st.TaskStatus == 4 {
			if st.FailedCount > 0 {
				return fmt.Errorf("delete finished with %d failure(s) (success=%d skip=%d)",
					st.FailedCount, st.SuccessedCount, st.SkipCount)
			}
			return nil
		}
	}
	return fmt.Errorf("delete task %s did not finish in time", r.TaskID)
}

type capacityResp struct {
	ResCode    any    `json:"res_code"`
	ResMessage string `json:"res_message"`

	CloudCapacityInfo struct {
		TotalSize int64 `json:"totalSize"`
		UsedSize  int64 `json:"usedSize"`
		FreeSize  int64 `json:"freeSize"`
	} `json:"cloudCapacityInfo"`

	FamilyCapacityInfo struct {
		TotalSize int64 `json:"totalSize"`
		UsedSize  int64 `json:"usedSize"`
		FreeSize  int64 `json:"freeSize"`
	} `json:"familyCapacityInfo"`
}

// Capacity returns total/used bytes for the configured space.
func (c *Client) Capacity(ctx context.Context) (total, used int64, err error) {
	var r capacityResp
	if err := c.do(ctx, http.MethodGet, c.apiBase()+"/portal/getUserSizeInfo.action", RequestOptions{}, &r); err != nil {
		return 0, 0, err
	}
	if c.cfg.IsFamily() {
		return r.FamilyCapacityInfo.TotalSize, r.FamilyCapacityInfo.UsedSize, nil
	}
	return r.CloudCapacityInfo.TotalSize, r.CloudCapacityInfo.UsedSize, nil
}

// EscapeFileName mirrors the driver's URL escaping of upload file names.
func EscapeFileName(name string) string { return url.QueryEscape(name) }

// isZeroResCode reports whether an API res_code represents success.
// The field arrives as a JSON number (0) on success and as a string on error,
// so it is decoded into any and compared loosely.
func isZeroResCode(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case float64:
		return t == 0
	case int:
		return t == 0
	case string:
		return t == "" || t == "0"
	default:
		return false
	}
}
