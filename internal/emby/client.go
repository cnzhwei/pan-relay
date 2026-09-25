package emby

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/cnzhwei/pan-relay/internal/stream"
)

const (
	DefaultClient  = "Hills Lite"
	DefaultDevice  = "Windows"
	DefaultVersion = "1.3.0"
)

type Config struct {
	BaseURL     string `json:"base_url"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	Client      string `json:"client,omitempty"`
	Device      string `json:"device,omitempty"`
	Version     string `json:"version,omitempty"`
	UserAgent   string `json:"user_agent,omitempty"`
	DeviceID    string `json:"device_id,omitempty"`
	VerifyTLS   *bool  `json:"verify_tls,omitempty"`
	HTTPTimeout int    `json:"http_timeout_seconds,omitempty"`
}

func LoadConfig(customPath string) (Config, error) {
	var cfg Config
	candidates := []string{}
	if customPath != "" {
		candidates = append(candidates, customPath)
	}
	candidates = append(candidates, "./emby_config.json")
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".config", "pan-relay", "emby.json"))
	}
	for _, candidate := range candidates {
		data, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			return Config{}, fmt.Errorf("failed to parse Emby config %s: %w", candidate, err)
		}
		break
	}
	if value := os.Getenv("EMBY_URL"); value != "" {
		cfg.BaseURL = value
	}
	if value := os.Getenv("EMBY_USER"); value != "" {
		cfg.Username = value
	}
	if value := os.Getenv("EMBY_PASS"); value != "" {
		cfg.Password = value
	}
	if value := os.Getenv("EMBY_CLIENT"); value != "" {
		cfg.Client = value
	}
	if value := os.Getenv("EMBY_DEVICE"); value != "" {
		cfg.Device = value
	}
	if value := os.Getenv("EMBY_CLIENT_VER"); value != "" {
		cfg.Version = value
	}
	if value := os.Getenv("EMBY_USER_AGENT"); value != "" {
		cfg.UserAgent = value
	}
	if cfg.BaseURL == "" || cfg.Username == "" || cfg.Password == "" {
		return Config{}, fmt.Errorf("Emby config requires base_url, username, and password")
	}
	return cfg, nil
}

type ConfigOverrides struct {
	UserAgent string
	Client    string
	Device    string
	Version   string
}

func (c Config) normalized(overrides ConfigOverrides) Config {
	if overrides.UserAgent != "" {
		c.UserAgent = overrides.UserAgent
	}
	if overrides.Client != "" {
		c.Client = overrides.Client
	}
	if overrides.Device != "" {
		c.Device = overrides.Device
	}
	if overrides.Version != "" {
		c.Version = overrides.Version
	}
	if c.Client == "" {
		c.Client = DefaultClient
	}
	if c.Device == "" {
		c.Device = DefaultDevice
	}
	if c.Version == "" {
		c.Version = DefaultVersion
	}
	if c.UserAgent == "" {
		c.UserAgent = c.Client + "/" + c.Version
	}
	if c.DeviceID == "" {
		c.DeviceID = "pan-relay-emby"
	}
	if c.HTTPTimeout <= 0 {
		c.HTTPTimeout = 60
	}
	return c
}

type Client struct {
	cfg         Config
	httpClient  *http.Client
	accessToken string
	userID      string
}

func NewClient(cfg Config, overrides ConfigOverrides) (*Client, error) {
	cfg = cfg.normalized(overrides)
	if _, err := url.ParseRequestURI(cfg.BaseURL); err != nil {
		return nil, fmt.Errorf("invalid Emby base_url: %w", err)
	}
	return &Client{cfg: cfg, httpClient: &http.Client{Timeout: time.Duration(cfg.HTTPTimeout) * time.Second}}, nil
}

func (c *Client) UserAgent() string  { return c.cfg.UserAgent }
func (c *Client) ClientName() string { return c.cfg.Client }

func (c *Client) authHeader(withToken bool) string {
	parts := []string{
		`MediaBrowser Client="` + c.cfg.Client + `"`,
		`Device="` + c.cfg.Device + `"`,
		`DeviceId="` + c.cfg.DeviceID + `"`,
		`Version="` + c.cfg.Version + `"`,
	}
	if withToken && c.accessToken != "" {
		parts = append(parts, `Token="`+c.accessToken+`"`)
	}
	return strings.Join(parts, ", ")
}

func (c *Client) endpoint(p string) (string, error) {
	base, err := url.Parse(c.cfg.BaseURL)
	if err != nil {
		return "", err
	}
	rel, err := url.Parse(p)
	if err != nil {
		return "", err
	}
	base.Path = path.Join(base.Path, strings.TrimPrefix(rel.Path, "/"))
	base.RawQuery = rel.RawQuery
	return base.String(), nil
}

func (c *Client) request(ctx context.Context, method, endpointPath string, body io.Reader, contentType string, auth bool, headers http.Header) (*http.Response, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is nil")
	}
	endpoint, err := c.endpoint(endpointPath)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.cfg.UserAgent)
	req.Header.Set("X-Emby-Authorization", c.authHeader(auth))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for key, values := range headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	return c.httpClient.Do(req)
}

func ensureJSON(resp *http.Response, what string) ([]byte, error) {
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s response: %w", what, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Emby %s returned HTTP %d: %s", what, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}

func (c *Client) Login(ctx context.Context) error {
	payload, _ := json.Marshal(map[string]string{"Username": c.cfg.Username, "Pw": c.cfg.Password})
	resp, err := c.request(ctx, http.MethodPost, "/Users/AuthenticateByName", strings.NewReader(string(payload)), "application/json", false, nil)
	if err != nil {
		return fmt.Errorf("Emby login request: %w", err)
	}
	data, err := ensureJSON(resp, "login")
	if err != nil {
		return err
	}
	var result struct {
		AccessToken string `json:"AccessToken"`
		User        struct {
			ID string `json:"Id"`
		} `json:"User"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("parse Emby login response: %w", err)
	}
	if result.AccessToken == "" || result.User.ID == "" {
		return fmt.Errorf("Emby login response missing access token or user id")
	}
	c.accessToken, c.userID = result.AccessToken, result.User.ID
	return nil
}

type Item struct {
	ID           string            `json:"Id"`
	Name         string            `json:"Name"`
	Type         string            `json:"Type"`
	Size         int64             `json:"Size"`
	Container    string            `json:"Container"`
	ProviderIDs  map[string]string `json:"ProviderIds"`
	MediaSources []MediaSource     `json:"MediaSources"`
}

type MediaSource struct {
	ID        string `json:"Id"`
	Size      int64  `json:"Size"`
	Container string `json:"Container"`
	Name      string `json:"Name"`
}

type MediaInfo struct {
	Item      Item
	SourceID  string
	Size      int64
	Container string
}

var imdbQuery = regexp.MustCompile(`(?i)^(?:imdb[: ]*)?(tt[0-9]{6,10})$`)
var tmdbQuery = regexp.MustCompile(`(?i)^tmdb[: ]*([0-9]+)$`)

func (c *Client) Resolve(ctx context.Context, query string) ([]Item, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("Emby query is empty")
	}
	if match := imdbQuery.FindStringSubmatch(query); match != nil {
		return c.search(ctx, "", "imdb."+strings.ToLower(match[1]))
	}
	if match := tmdbQuery.FindStringSubmatch(query); match != nil {
		return c.search(ctx, "", "tmdb."+match[1])
	}
	if strings.HasPrefix(strings.ToLower(query), "item:") {
		item, err := c.item(ctx, strings.TrimSpace(query[5:]))
		if err != nil {
			return nil, err
		}
		return []Item{item}, nil
	}
	return c.search(ctx, query, "")
}

func (c *Client) search(ctx context.Context, term, provider string) ([]Item, error) {
	params := url.Values{"IncludeItemTypes": {"Movie,Episode,Video"}, "Recursive": {"true"}, "Limit": {"20"}}
	if term != "" {
		params.Set("SearchTerm", term)
	}
	if provider != "" {
		params.Set("AnyProviderIdEquals", provider)
	}
	endpoint := "/Users/" + c.userID + "/Items?" + params.Encode()
	resp, err := c.request(ctx, http.MethodGet, endpoint, nil, "", true, nil)
	if err != nil {
		return nil, err
	}
	data, err := ensureJSON(resp, "item search")
	if err != nil {
		return nil, err
	}
	var result struct {
		Items []Item `json:"Items"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("parse Emby items response: %w", err)
	}
	return result.Items, nil
}

func (c *Client) item(ctx context.Context, id string) (Item, error) {
	resp, err := c.request(ctx, http.MethodGet, "/Users/"+c.userID+"/Items/"+url.PathEscape(id), nil, "", true, nil)
	if err != nil {
		return Item{}, err
	}
	data, err := ensureJSON(resp, "item")
	if err != nil {
		return Item{}, err
	}
	var result Item
	if err := json.Unmarshal(data, &result); err != nil {
		return Item{}, err
	}
	return result, nil
}

func (c *Client) GetMediaInfo(ctx context.Context, itemID string) (MediaInfo, error) {
	body, _ := json.Marshal(map[string]any{"UserId": c.userID, "DeviceProfile": map[string]any{
		"Name": c.cfg.Client, "MaxStreamingBitrate": 120000000, "MaxStaticBitrate": 120000000,
		"DirectPlayProfiles": []map[string]string{{"Type": "Video"}, {"Type": "Audio"}}, "TranscodingProfiles": []any{},
	}})
	resp, err := c.request(ctx, http.MethodPost, "/Items/"+url.PathEscape(itemID)+"/PlaybackInfo?UserId="+url.QueryEscape(c.userID), strings.NewReader(string(body)), "application/json", true, nil)
	if err != nil {
		return MediaInfo{}, err
	}
	data, err := ensureJSON(resp, "playback info")
	if err != nil {
		return MediaInfo{}, err
	}
	var result struct {
		MediaSources []MediaSource `json:"MediaSources"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return MediaInfo{}, err
	}
	if len(result.MediaSources) == 0 || result.MediaSources[0].Size <= 0 {
		return MediaInfo{}, fmt.Errorf("Emby item %s has no usable media source", itemID)
	}
	source := result.MediaSources[0]
	return MediaInfo{SourceID: source.ID, Size: source.Size, Container: source.Container}, nil
}

func (c *Client) PartReader(ctx context.Context, itemID, sourceID string, offset, size int64) (io.Reader, error) {
	if size <= 0 || offset < 0 {
		return nil, fmt.Errorf("invalid Emby range offset=%d size=%d", offset, size)
	}
	endpoint := "/Videos/" + url.PathEscape(itemID) + "/stream?Static=true&MediaSourceId=" + url.QueryEscape(sourceID) + "&DeviceId=" + url.QueryEscape(c.cfg.DeviceID)
	resp, err := c.request(ctx, http.MethodGet, endpoint, nil, "", true, http.Header{"Range": []string{fmt.Sprintf("bytes=%d-%d", offset, offset+size-1)}})
	if err != nil {
		return nil, err
	}
	reader, err := stream.ReadRangeResponse(resp, offset, size)
	resp.Body.Close()
	return reader, err
}

func (c *Client) FileName(item Item, info MediaInfo) string {
	name := strings.TrimSpace(item.Name)
	if name == "" {
		name = "emby-" + item.ID
	}
	ext := strings.TrimPrefix(strings.ToLower(info.Container), ".")
	if ext == "" {
		ext = "mkv"
	}
	return name + "." + ext
}
