package client

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cnzhwei/pan-relay/internal/config"
	"github.com/go-resty/resty/v2"
	"github.com/xhofe/wopan-sdk-go"
)

type Client struct {
	cfg      *config.Config
	w        *wopan.WoClient
	mu       sync.Mutex
	spaceTyp string
}

func New(cfg *config.Config) (*Client, error) {
	w := wopan.DefaultWithRefreshToken(cfg.RefreshToken)
	if cfg.AccessToken != "" {
		w.SetAccessToken(cfg.AccessToken)
	}
	if cfg.ZoneURL != "" {
		w.ZoneURL = cfg.ZoneURL
	}

	spaceType := wopan.SpaceTypePersonal
	if cfg.FamilyID != "" {
		spaceType = wopan.SpaceTypeFamily
	}

	c := &Client{
		cfg:      cfg,
		w:        w,
		spaceTyp: spaceType,
	}

	// Register token refresh callback
	w.OnRefreshToken(func(accessToken, refreshToken string) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.cfg.AccessToken = accessToken
		c.cfg.RefreshToken = refreshToken
		_ = c.cfg.Save()
	})

	return c, nil
}

func (c *Client) EnsureValidToken() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Probe user info
	_, err := c.w.AppQueryUser()
	if err == nil {
		return nil
	}

	// Token expired or invalid, trigger AppRefreshToken
	ref, err := c.w.AppRefreshToken()
	if err != nil {
		return fmt.Errorf("token refresh failed (please check refresh_token): %w", err)
	}

	c.w.SetAccessToken(ref.AccessToken)
	c.w.SetRefreshToken(ref.RefreshToken)
	c.cfg.AccessToken = ref.AccessToken
	c.cfg.RefreshToken = ref.RefreshToken
	_ = c.cfg.Save()

	return nil
}

func (c *Client) ForceRefreshToken() (*wopan.AppRefreshTokenData, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	ref, err := c.w.AppRefreshToken()
	if err != nil {
		return nil, fmt.Errorf("failed to refresh token: %w", err)
	}

	c.w.SetAccessToken(ref.AccessToken)
	c.w.SetRefreshToken(ref.RefreshToken)
	c.cfg.AccessToken = ref.AccessToken
	c.cfg.RefreshToken = ref.RefreshToken
	_ = c.cfg.Save()

	return ref, nil
}

func (c *Client) Raw() *wopan.WoClient {
	return c.w
}

func (c *Client) Config() *config.Config {
	return c.cfg
}

func (c *Client) SpaceType() string {
	return c.spaceTyp
}

type FileItem struct {
	ID         string    `json:"id"`
	FID        string    `json:"fid"`
	Name       string    `json:"name"`
	Size       int64     `json:"size"`
	IsFolder   bool      `json:"is_folder"`
	CreateTime time.Time `json:"create_time"`
	Type       int       `json:"type"`
}

func (c *Client) ListDir(parentID string) ([]FileItem, error) {
	if parentID == "" {
		parentID = "0"
	}

	var allItems []FileItem
	pageNum := 0
	pageSize := 100

	for {
		data, err := c.w.QueryAllFiles(c.spaceTyp, parentID, pageNum, pageSize, wopan.SortNameAsc, c.cfg.FamilyID)
		if err != nil {
			// If unauthorized, refresh and retry once
			if strings.Contains(err.Error(), "token") || strings.Contains(err.Error(), "0002") {
				if rerr := c.EnsureValidToken(); rerr == nil {
					data, err = c.w.QueryAllFiles(c.spaceTyp, parentID, pageNum, pageSize, wopan.SortNameAsc, c.cfg.FamilyID)
				}
			}
			if err != nil {
				return nil, fmt.Errorf("failed to list files in directory %s: %w", parentID, err)
			}
		}

		if data == nil || len(data.Files) == 0 {
			break
		}

		for _, f := range data.Files {
			t, _ := time.Parse("20060102150405", f.CreateTime)
			allItems = append(allItems, FileItem{
				ID:         f.Id,
				FID:        f.Fid,
				Name:       f.Name,
				Size:       f.Size,
				IsFolder:   f.Type == 0,
				CreateTime: t,
				Type:       f.Type,
			})
		}

		if len(data.Files) < pageSize {
			break
		}
		pageNum++
	}

	return allItems, nil
}

// ResolvePath resolves a path like "/emby/movies" to its target directory ID.
func (c *Client) ResolvePath(pathStr string) (string, error) {
	pathStr = strings.TrimSpace(pathStr)
	if pathStr == "" || pathStr == "/" || pathStr == "." {
		return "0", nil
	}

	// If it already looks like a 32-hex ID or number, try it
	if !strings.Contains(pathStr, "/") {
		return pathStr, nil
	}

	parts := strings.Split(strings.Trim(pathStr, "/"), "/")
	currentID := "0"

	for _, part := range parts {
		if part == "" {
			continue
		}
		items, err := c.ListDir(currentID)
		if err != nil {
			return "", err
		}

		found := false
		for _, item := range items {
			if item.IsFolder && item.Name == part {
				currentID = item.ID
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("path component %q not found in directory %s", part, currentID)
		}
	}

	return currentID, nil
}

func (c *Client) Mkdir(parentID, name string) (string, error) {
	if parentID == "" {
		parentID = "0"
	}

	var resp struct {
		DirectoryID string `json:"directoryId"`
	}

	param := wopan.Json{
		"spaceType":         c.spaceTyp,
		"parentDirectoryId": parentID,
		"directoryName":     name,
		"clientId":          wopan.DefaultClientID,
	}
	if c.spaceTyp == wopan.SpaceTypeFamily {
		param["familyId"] = c.cfg.FamilyID
	}

	_, err := c.w.RequestWoHome(wopan.KeyCreateDirectory, param, wopan.JsonSecret, &resp)
	if err != nil {
		return "", fmt.Errorf("failed to create directory %q: %w", name, err)
	}

	return resp.DirectoryID, nil
}

func (c *Client) Delete(id string) error {
	err := c.w.DeleteFile(c.spaceTyp, []string{id}, []string{id})
	if err != nil {
		return fmt.Errorf("failed to delete item %s: %w", id, err)
	}
	return nil
}

func (c *Client) GetDownloadURL(fid string) (string, error) {
	res, err := c.w.GetDownloadUrlV2([]string{fid}, func(req *resty.Request) {
		req.SetContext(context.Background())
	})
	if err != nil {
		return "", fmt.Errorf("failed to get download url for fid %s: %w", fid, err)
	}
	if len(res.List) == 0 || res.List[0].DownloadUrl == "" {
		return "", fmt.Errorf("no download url returned for fid %s", fid)
	}
	return res.List[0].DownloadUrl, nil
}
