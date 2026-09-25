package emby

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/cnzhwei/pan-relay/internal/stream"
	xproxy "golang.org/x/net/proxy"
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
	Service     string `json:"service,omitempty"`
	ItemLookup  string `json:"item_lookup,omitempty"`
	Proxy       string `json:"proxy,omitempty"`
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
	if value := os.Getenv("EMBY_SERVICE"); value != "" {
		cfg.Service = value
	}
	if value := os.Getenv("EMBY_ITEM_LOOKUP"); value != "" {
		cfg.ItemLookup = value
	}
	if value := os.Getenv("EMBY_PROXY"); value != "" {
		cfg.Proxy = value
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
	Proxy     string
}

func (c Config) normalized(overrides ConfigOverrides) (Config, error) {
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
	if overrides.Proxy != "" {
		c.Proxy = overrides.Proxy
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
	c.Service = strings.ToLower(strings.TrimSpace(c.Service))
	if c.Service == "" {
		c.Service = "guai"
	}
	if c.Service != "guai" && c.Service != "vemby" {
		return Config{}, fmt.Errorf("unsupported Emby service %q", c.Service)
	}
	c.ItemLookup = strings.ToLower(strings.TrimSpace(c.ItemLookup))
	if c.ItemLookup == "" {
		if c.Service == "vemby" {
			c.ItemLookup = "detail"
		} else {
			c.ItemLookup = "ids_query"
		}
	}
	if c.ItemLookup != "detail" && c.ItemLookup != "ids_query" {
		return Config{}, fmt.Errorf("unsupported Emby item_lookup %q", c.ItemLookup)
	}
	if c.HTTPTimeout <= 0 {
		c.HTTPTimeout = 60
	}
	return c, nil
}

type Client struct {
	cfg         Config
	httpClient  *http.Client
	accessToken string
	userID      string
}

func normalizeProxyURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, "direct") || strings.EqualFold(raw, "off") {
		return "", nil
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid proxy URL")
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return "", fmt.Errorf("unsupported proxy scheme %q", u.Scheme)
	}
	return raw, nil
}

func newHTTPTransport(proxyRaw string) (*http.Transport, error) {
	proxyURL, err := normalizeProxyURL(proxyRaw)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if proxyURL == "" {
		return transport, nil
	}
	u, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("parse proxy URL: %w", err)
	}
	if u.Scheme == "http" || u.Scheme == "https" {
		transport.Proxy = http.ProxyURL(u)
		return transport, nil
	}

	var auth *xproxy.Auth
	if password, ok := u.User.Password(); ok {
		auth = &xproxy.Auth{User: u.User.Username(), Password: password}
	} else if u.User != nil {
		auth = &xproxy.Auth{User: u.User.Username()}
	}
	dialer, err := xproxy.SOCKS5("tcp", u.Host, auth, xproxy.Direct)
	if err != nil {
		return nil, fmt.Errorf("configure SOCKS5 proxy: %w", err)
	}
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result := make(chan struct {
			conn net.Conn
			err  error
		}, 1)
		go func() {
			conn, err := dialer.Dial(network, address)
			result <- struct {
				conn net.Conn
				err  error
			}{conn: conn, err: err}
		}()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case result := <-result:
			return result.conn, result.err
		}
	}
	return transport, nil
}

func NewClient(cfg Config, overrides ConfigOverrides) (*Client, error) {
	cfg, err := cfg.normalized(overrides)
	if err != nil {
		return nil, err
	}
	if _, err := url.ParseRequestURI(cfg.BaseURL); err != nil {
		return nil, fmt.Errorf("invalid Emby base_url: %w", err)
	}
	transport, err := newHTTPTransport(cfg.Proxy)
	if err != nil {
		return nil, fmt.Errorf("invalid Emby proxy: %w", err)
	}
	return &Client{cfg: cfg, httpClient: &http.Client{Transport: transport, Timeout: time.Duration(cfg.HTTPTimeout) * time.Second}}, nil
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
	basePath := base.EscapedPath()
	relPath := rel.EscapedPath()
	base.Path = path.Join(base.Path, strings.TrimPrefix(rel.Path, "/"))
	base.RawPath = path.Join(basePath, strings.TrimPrefix(relPath, "/"))
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
		return nil, embyHTTPError{what: what, status: resp.StatusCode, body: strings.TrimSpace(string(data))}
	}
	return data, nil
}

type embyHTTPError struct {
	what   string
	status int
	body   string
}

func (e embyHTTPError) Error() string {
	return fmt.Sprintf("Emby %s returned HTTP %d: %s", e.what, e.status, e.body)
}

func isUnsupportedItemsQueryError(err error) bool {
	var httpErr embyHTTPError
	return errors.As(err, &httpErr) && (httpErr.status == http.StatusNotFound || httpErr.status == http.StatusMethodNotAllowed || httpErr.status == http.StatusBadRequest)
}

func (c *Client) authPaths() []string {
	if c.cfg.Service == "vemby" {
		return []string{"/Users/AuthenticateByName"}
	}
	return []string{"/Users/AuthenticateByName", "/emby/Users/AuthenticateByName"}
}

func (c *Client) Login(ctx context.Context) error {
	payload, _ := json.Marshal(map[string]string{"Username": c.cfg.Username, "Pw": c.cfg.Password})
	var resp *http.Response
	var err error
	for _, authPath := range c.authPaths() {
		resp, err = c.request(ctx, http.MethodPost, authPath, strings.NewReader(string(payload)), "application/json", false, nil)
		if err != nil {
			return fmt.Errorf("Emby login request: %w", err)
		}
		if resp.StatusCode != http.StatusNotFound || authPath == c.authPaths()[len(c.authPaths())-1] {
			break
		}
		_ = resp.Body.Close()
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
		itemID := strings.TrimSpace(query[5:])
		if itemID == "" {
			return nil, fmt.Errorf("Emby item ID is empty")
		}
		item, err := c.item(ctx, itemID)
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
	endpoint := "/Users/" + url.PathEscape(c.userID) + "/Items?" + params.Encode()
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
	fields := "Path,MediaSources,MediaStreams,Overview,ProductionYear,RunTimeTicks,Container,Size,CanDownload,SeriesName,SeasonName,IndexNumber,ParentIndexNumber,ProviderIds,OfficialRating,CommunityRating,DateCreated,PremiereDate,SeriesId,SeasonId,SortName,OriginalTitle"
	if c.cfg.ItemLookup == "detail" {
		resp, err := c.request(ctx, http.MethodGet, "/Users/"+url.PathEscape(c.userID)+"/Items/"+url.PathEscape(id)+"?Fields="+url.QueryEscape(fields), nil, "", true, nil)
		if err != nil {
			return Item{}, err
		}
		data, err := ensureJSON(resp, "item lookup")
		if err != nil {
			return Item{}, err
		}
		var result Item
		if err := json.Unmarshal(data, &result); err != nil {
			return Item{}, fmt.Errorf("parse Emby item lookup response: %w", err)
		}
		if result.ID == "" {
			return Item{}, fmt.Errorf("Emby item %q response has missing ID", id)
		}
		if result.ID != id {
			return Item{}, fmt.Errorf("Emby item lookup returned ID %q for requested ID %q", result.ID, id)
		}
		if !isPlayableItemType(result.Type) {
			return Item{}, fmt.Errorf("Emby item %q has non-playable type %q", id, result.Type)
		}
		return result, nil
	}

	params := url.Values{"Ids": {id}, "Fields": {fields}}
	resp, err := c.request(ctx, http.MethodGet, "/Users/"+url.PathEscape(c.userID)+"/Items?"+params.Encode(), nil, "", true, nil)
	if err == nil {
		data, lookupErr := ensureJSON(resp, "item lookup")
		if lookupErr != nil {
			if !isUnsupportedItemsQueryError(lookupErr) {
				return Item{}, lookupErr
			}
			err = lookupErr
		} else {
			var list struct {
				Items []Item `json:"Items"`
			}
			if jsonErr := json.Unmarshal(data, &list); jsonErr != nil {
				return Item{}, fmt.Errorf("parse Emby item lookup response: %w", jsonErr)
			}
			switch len(list.Items) {
			case 1:
				if list.Items[0].ID != id {
					return Item{}, fmt.Errorf("Emby item lookup returned ID %q for requested ID %q", list.Items[0].ID, id)
				}
				if !isPlayableItemType(list.Items[0].Type) {
					return Item{}, fmt.Errorf("Emby item %q has non-playable type %q", id, list.Items[0].Type)
				}
				return list.Items[0], nil
			case 0:
				return Item{}, fmt.Errorf("Emby item %q not found", id)
			default:
				return Item{}, fmt.Errorf("Emby item lookup for %q returned %d items", id, len(list.Items))
			}
		}
	}
	// 兼容明确不支持 Ids 查询的 Emby 版本；其他请求错误直接返回，避免掩盖根因。
	if !isUnsupportedItemsQueryError(err) {
		return Item{}, err
	}
	resp, err = c.request(ctx, http.MethodGet, "/Users/"+url.PathEscape(c.userID)+"/Items/"+url.PathEscape(id), nil, "", true, nil)
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
	if result.ID == "" {
		return Item{}, fmt.Errorf("Emby item %q response has missing ID", id)
	}
	if result.ID != id {
		return Item{}, fmt.Errorf("Emby item lookup returned ID %q for requested ID %q", result.ID, id)
	}
	if !isPlayableItemType(result.Type) {
		return Item{}, fmt.Errorf("Emby item %q has non-playable type %q", id, result.Type)
	}
	return result, nil
}

func isPlayableItemType(itemType string) bool {
	switch strings.ToLower(strings.TrimSpace(itemType)) {
	case "movie", "episode", "video":
		return true
	default:
		return false
	}
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
	for _, source := range result.MediaSources {
		if source.ID != "" && source.Size > 0 {
			return MediaInfo{SourceID: source.ID, Size: source.Size, Container: source.Container}, nil
		}
	}
	return MediaInfo{}, fmt.Errorf("Emby item %s has no usable media source", itemID)
}

func (c *Client) PartReader(ctx context.Context, itemID, sourceID string, offset, size int64) (io.Reader, error) {
	if size <= 0 || offset < 0 {
		return nil, fmt.Errorf("invalid Emby range offset=%d size=%d", offset, size)
	}
	if offset > math.MaxInt64-size {
		return nil, fmt.Errorf("Emby range offset=%d size=%d overflows int64", offset, size)
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
