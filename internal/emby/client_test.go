package emby

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
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

func TestNewClientRejectsNilContextAtRequestBoundary(t *testing.T) {
	c, err := NewClient(Config{BaseURL: "http://127.0.0.1", UserAgent: "UA"}, ConfigOverrides{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if _, err := c.request(nil, http.MethodGet, "/", nil, "", false, nil); err == nil || !strings.Contains(err.Error(), "context is nil") {
		t.Fatalf("request(nil) error = %v", err)
	}
}
