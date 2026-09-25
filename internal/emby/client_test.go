package emby

import (
	"context"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestClientUsesConfiguredHTTPUserAgentAndEmbyIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != "My-UA/9.9" {
			t.Fatalf("User-Agent = %q, want %q", got, "My-UA/9.9")
		}
		if got := r.Header.Get("X-Emby-Authorization"); !strings.Contains(got, `Client="SenPlayer"`) {
			t.Fatalf("X-Emby-Authorization = %q, missing client identity", got)
		}
		if r.URL.Path != "/Users/AuthenticateByName" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"AccessToken":"token","User":{"Id":"user-1"}}`)
	}))
	defer server.Close()

	c, err := NewClient(Config{BaseURL: server.URL, Username: "u", Password: "p", Client: "SenPlayer", Device: "iPhone", Version: "5.5.0", UserAgent: "My-UA/9.9"}, ConfigOverrides{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if err := c.Login(context.Background()); err != nil {
		t.Fatalf("Login() error = %v", err)
	}
}

func TestCLIOverridesUserAgentAndAuthorizationIdentity(t *testing.T) {
	c, err := NewClient(Config{BaseURL: "http://127.0.0.1", Username: "u", Password: "p"}, ConfigOverrides{
		UserAgent: "Custom-UA/1.0",
		Client:    "SenPlayer",
		Device:    "Android",
		Version:   "5.5.0",
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if c.UserAgent() != "Custom-UA/1.0" || c.ClientName() != "SenPlayer" || c.cfg.Device != "Android" || c.cfg.Version != "5.5.0" {
		t.Fatalf("overrides not applied: %#v", c.cfg)
	}
}

func TestResolveByIMDbAndStreamReader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/Users/user-1/Items":
			io.WriteString(w, `{"Items":[{"Id":"item-1","Name":"Movie","Type":"Movie","ProviderIds":{"Imdb":"tt1234567"},"MediaSources":[{"Id":"source-1","Size":4,"Container":"mkv"}]}]}`)
		case r.URL.Path == "/Items/item-1/PlaybackInfo":
			io.WriteString(w, `{"MediaSources":[{"Id":"source-1","Size":4,"Container":"mkv"}]}`)
		case r.URL.Path == "/Videos/item-1/stream":
			if got := r.Header.Get("Range"); got != "bytes=0-3" {
				t.Fatalf("Range = %q", got)
			}
			w.Header().Set("Content-Range", "bytes 0-3/4")
			w.Header().Set("Content-Length", "4")
			w.WriteHeader(http.StatusPartialContent)
			io.WriteString(w, "data")
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	c, err := NewClient(Config{BaseURL: server.URL, UserAgent: "UA"}, ConfigOverrides{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	c.accessToken = "token"
	c.userID = "user-1"
	items, err := c.Resolve(context.Background(), "tt1234567")
	if err != nil || len(items) != 1 || items[0].ID != "item-1" {
		t.Fatalf("Resolve() = %#v, %v", items, err)
	}
	info, err := c.GetMediaInfo(context.Background(), items[0].ID)
	if err != nil || info.Size != 4 {
		t.Fatalf("GetMediaInfo() = %#v, %v", info, err)
	}
	reader, err := c.PartReader(context.Background(), items[0].ID, info.SourceID, 0, 4)
	if err != nil {
		t.Fatalf("PartReader() error = %v", err)
	}
	data, err := io.ReadAll(reader)
	if err != nil || string(data) != "data" {
		t.Fatalf("PartReader() = %q, %v", data, err)
	}
}

func TestResolveItemUsesIdsQueryAndValidatesIdentity(t *testing.T) {
	var fallbackCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/Users/user-1/Items":
			if got := r.URL.Query().Get("Ids"); got != "item-1" {
				t.Fatalf("Ids = %q, want item-1", got)
			}
			if r.URL.Query().Get("Fields") == "" {
				t.Fatal("Fields query is empty")
			}
			io.WriteString(w, `{"Items":[{"Id":"item-1","Name":"Movie","Type":"Movie","MediaSources":[{"Id":"source-1","Size":4}]}]}`)
		case "/Users/user-1/Items/item-1":
			fallbackCalled = true
			t.Fatal("unexpected fallback request")
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	c, err := NewClient(Config{BaseURL: server.URL, UserAgent: "UA"}, ConfigOverrides{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	c.userID = "user-1"
	items, err := c.Resolve(context.Background(), "item:item-1")
	if err != nil || len(items) != 1 || items[0].ID != "item-1" || fallbackCalled {
		t.Fatalf("Resolve() = %#v, %v, fallback=%v", items, err, fallbackCalled)
	}
}

func TestEndpointPreservesEscapedPathSegments(t *testing.T) {
	c, err := NewClient(Config{BaseURL: "http://example.test/base", UserAgent: "UA"}, ConfigOverrides{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	endpoint, err := c.endpoint("/Users/a%2Fb/Items/item%2Fc")
	if err != nil {
		t.Fatalf("endpoint() error = %v", err)
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	if got := parsed.EscapedPath(); got != "/base/Users/a%2Fb/Items/item%2Fc" {
		t.Fatalf("EscapedPath() = %q, want encoded path", got)
	}
}

func TestResolveItemFallbackOnlyForUnsupportedQuery(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		wantLookup bool
	}{
		{name: "bad request", status: http.StatusBadRequest, wantLookup: true},
		{name: "server error", status: http.StatusInternalServerError, wantLookup: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookupCalled := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/Users/user-1/Items":
					w.WriteHeader(tt.status)
					io.WriteString(w, `{}`)
				case "/Users/user-1/Items/item-1":
					lookupCalled = true
					io.WriteString(w, `{"Id":"item-1","Type":"Movie"}`)
				default:
					t.Fatalf("unexpected path %s", r.URL.Path)
				}
			}))
			defer server.Close()

			c, err := NewClient(Config{BaseURL: server.URL, UserAgent: "UA"}, ConfigOverrides{})
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			c.userID = "user-1"
			_, _ = c.Resolve(context.Background(), "item:item-1")
			if lookupCalled != tt.wantLookup {
				t.Fatalf("fallback called = %v, want %v", lookupCalled, tt.wantLookup)
			}
		})
	}
}

func TestResolveItemFallbackRejectsEmptyID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/Users/user-1/Items" {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{}`)
			return
		}
		io.WriteString(w, `{"Id":"","Type":"Movie"}`)
	}))
	defer server.Close()
	c, err := NewClient(Config{BaseURL: server.URL, UserAgent: "UA"}, ConfigOverrides{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	c.userID = "user-1"
	_, err = c.Resolve(context.Background(), "item:item-1")
	if err == nil || !strings.Contains(err.Error(), "missing ID") {
		t.Fatalf("Resolve() error = %v, want missing ID error", err)
	}
}

func TestResolveItemRejectsMismatchedID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"Items":[{"Id":"other-item","Type":"Movie"}]}`)
	}))
	defer server.Close()

	c, err := NewClient(Config{BaseURL: server.URL, UserAgent: "UA"}, ConfigOverrides{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	c.userID = "user-1"
	_, err = c.Resolve(context.Background(), "item:item-1")
	if err == nil || !strings.Contains(err.Error(), "returned ID") {
		t.Fatalf("Resolve() error = %v, want mismatched ID error", err)
	}
}

func TestResolveRejectsEmptyItemID(t *testing.T) {
	c, err := NewClient(Config{BaseURL: "http://127.0.0.1", UserAgent: "UA"}, ConfigOverrides{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	_, err = c.Resolve(context.Background(), "item: ")
	if err == nil || !strings.Contains(err.Error(), "item ID is empty") {
		t.Fatalf("Resolve() error = %v, want empty item ID error", err)
	}
}

func TestResolveRejectsNonPlayableItemType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"Items":[{"Id":"item-1","Type":"Person"}]}`)
	}))
	defer server.Close()

	c, err := NewClient(Config{BaseURL: server.URL, UserAgent: "UA"}, ConfigOverrides{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	c.userID = "user-1"
	_, err = c.Resolve(context.Background(), "item:item-1")
	if err == nil || !strings.Contains(err.Error(), "non-playable") {
		t.Fatalf("Resolve() error = %v, want non-playable error", err)
	}
}

func TestGetMediaInfoSelectsFirstUsableSource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"MediaSources":[{"Id":"","Size":100},{"Id":"source-2","Size":200,"Container":"mkv"}]}`)
	}))
	defer server.Close()

	c, err := NewClient(Config{BaseURL: server.URL, UserAgent: "UA"}, ConfigOverrides{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	c.userID = "user-1"
	info, err := c.GetMediaInfo(context.Background(), "item-1")
	if err != nil || info.SourceID != "source-2" || info.Size != 200 {
		t.Fatalf("GetMediaInfo() = %#v, %v", info, err)
	}
}

func TestPartReaderRejectsRangeOverflow(t *testing.T) {
	c, err := NewClient(Config{BaseURL: "http://127.0.0.1", UserAgent: "UA"}, ConfigOverrides{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	_, err = c.PartReader(context.Background(), "item-1", "source-1", math.MaxInt64, 2)
	if err == nil || !strings.Contains(err.Error(), "overflows") {
		t.Fatalf("PartReader() error = %v, want overflow error", err)
	}
}

func TestNewClientRejectsNilContextAtRequestBoundary(t *testing.T) {
	c, err := NewClient(Config{BaseURL: "http://127.0.0.1", UserAgent: "UA"}, ConfigOverrides{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if _, err := c.request(nil, http.MethodGet, "/", nil, "", false, nil); err == nil || !strings.Contains(err.Error(), "context is nil") {
		t.Fatalf("request(nil) error = %v", err)
	}
}

func TestVembyServiceUsesStandardItemDetailAndAuthPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/Users/AuthenticateByName":
			io.WriteString(w, `{"AccessToken":"token","User":{"Id":"user-1"}}`)
		case "/Users/user-1/Items/item-1":
			io.WriteString(w, `{"Id":"item-1","Type":"Movie","Name":"Movie"}`)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	c, err := NewClient(Config{BaseURL: server.URL, Username: "u", Password: "p", Service: "vemby"}, ConfigOverrides{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if err := c.Login(context.Background()); err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	items, err := c.Resolve(context.Background(), "item:item-1")
	if err != nil || len(items) != 1 || items[0].ID != "item-1" {
		t.Fatalf("Resolve() = %#v, %v", items, err)
	}
}

func TestVembyConfigDefaultsToStandardDetailLookup(t *testing.T) {
	c, err := NewClient(Config{BaseURL: "http://127.0.0.1", Service: "vemby"}, ConfigOverrides{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if c.cfg.ItemLookup != "detail" {
		t.Fatalf("ItemLookup = %q, want detail", c.cfg.ItemLookup)
	}
	if got := c.authPaths(); len(got) != 1 || got[0] != "/Users/AuthenticateByName" {
		t.Fatalf("auth paths = %#v, want standard path only", got)
	}
}
