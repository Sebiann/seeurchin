// Package jellyfin is a minimal client for the Jellyfin REST API, scoped to
// what seeurchin needs: reading the movie/series library and proxying poster
// images.
//
// It authenticates with the modern Authorization header
// (`Authorization: MediaBrowser Token="...", Client="...", ...`). Jellyfin is
// removing the legacy schemes (X-Emby-Token, the api_key query parameter, etc.)
// in 10.13, so this client deliberately avoids them.
package jellyfin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	clientName    = "seeurchin"
	clientVersion = "0.1.0"
	deviceName    = "seeurchin-server"
	deviceID      = "seeurchin-server"
)

// Client talks to a single Jellyfin server using an API key for library reads.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// New returns a client for the given Jellyfin base URL and API key.
func New(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

// authHeader builds the modern Jellyfin Authorization header value. token is
// the access token or API key; it is omitted for unauthenticated calls.
func authHeader(token string) string {
	parts := []string{
		fmt.Sprintf("Client=%q", clientName),
		fmt.Sprintf("Device=%q", deviceName),
		fmt.Sprintf("DeviceId=%q", deviceID),
		fmt.Sprintf("Version=%q", clientVersion),
	}
	if token != "" {
		parts = append([]string{fmt.Sprintf("Token=%q", token)}, parts...)
	}
	return "MediaBrowser " + strings.Join(parts, ", ")
}

// AuthResult is the identity returned by a successful username/password login.
type AuthResult struct {
	UserID   string // Jellyfin user id (stable)
	Username string // display name as Jellyfin knows it
	IsAdmin  bool   // the account's Policy.IsAdministrator flag
}

// AuthenticateByName logs in a Jellyfin user with a username and password via
// POST /Users/AuthenticateByName, using the modern Authorization header with no
// token. It returns the resolved identity (id, name, admin flag). An error means
// the credentials were rejected or the server was unreachable; the access token
// Jellyfin returns is intentionally discarded — seeurchin only needs identity.
func (c *Client) AuthenticateByName(ctx context.Context, username, password string) (*AuthResult, error) {
	body, err := json.Marshal(map[string]string{"Username": username, "Pw": password})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/Users/AuthenticateByName", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	// No token: this call establishes one. Reuse the standard header builder.
	req.Header.Set("Authorization", authHeader(""))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Jellyfin returns 401 for bad credentials; surface a clean message.
		return nil, fmt.Errorf("jellyfin login: %s", resp.Status)
	}
	var out struct {
		User struct {
			ID     string `json:"Id"`
			Name   string `json:"Name"`
			Policy struct {
				IsAdministrator bool `json:"IsAdministrator"`
			} `json:"Policy"`
		} `json:"User"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.User.ID == "" {
		return nil, fmt.Errorf("jellyfin login: empty user in response")
	}
	return &AuthResult{
		UserID:   out.User.ID,
		Username: out.User.Name,
		IsAdmin:  out.User.Policy.IsAdministrator,
	}, nil
}

// User is a Jellyfin account, as offered to a poll host for linking.
type User struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ListUsers returns every enabled Jellyfin account (hidden ones included) via
// GET /Users with the server API key.
func (c *Client) ListUsers(ctx context.Context) ([]User, error) {
	var raw []struct {
		ID     string `json:"Id"`
		Name   string `json:"Name"`
		Policy struct {
			IsDisabled bool `json:"IsDisabled"`
		} `json:"Policy"`
	}
	if err := c.getJSON(ctx, "/Users", nil, &raw); err != nil {
		return nil, err
	}
	out := make([]User, 0, len(raw))
	for _, u := range raw {
		if !u.Policy.IsDisabled {
			out = append(out, User{ID: u.ID, Name: u.Name})
		}
	}
	return out, nil
}

// Item is a movie or series from the Jellyfin library.
type Item struct {
	ID              string            `json:"Id"`
	Name            string            `json:"Name"`
	Type            string            `json:"Type"` // "Movie" | "Series"
	ProductionYear  int               `json:"ProductionYear"`
	Overview        string            `json:"Overview"`
	RunTimeTicks    int64             `json:"RunTimeTicks"`
	CommunityRating float64           `json:"CommunityRating"`
	Genres          []string          `json:"Genres"`
	ImageTags       map[string]string `json:"ImageTags"`
	ProviderIds     map[string]string `json:"ProviderIds"` // e.g. {"Tmdb":"1234","Imdb":"tt.."}
}

// TMDBID returns the item's TMDB id as a string ("" if absent). Jellyfin keys
// provider ids by name; TMDB is "Tmdb".
func (it Item) TMDBID() string { return it.ProviderIds["Tmdb"] }

// RuntimeMinutes converts Jellyfin's 100ns ticks to whole minutes.
func (it Item) RuntimeMinutes() int {
	if it.RunTimeTicks <= 0 {
		return 0
	}
	return int(it.RunTimeTicks / 10_000_000 / 60)
}

// PrimaryImageTag returns the cache-busting tag for the primary image, if any.
func (it Item) PrimaryImageTag() string { return it.ImageTags["Primary"] }

// SearchParams controls a library query.
type SearchParams struct {
	Query      string   // free-text search; empty lists everything
	Types      []string // defaults to Movie + Series
	Genres     []string // restrict to these genres (OR); empty = any genre
	Limit      int
	StartIndex int
}

// Search returns library items matching the parameters along with the total
// record count for pagination.
func (c *Client) Search(ctx context.Context, p SearchParams) ([]Item, int, error) {
	types := p.Types
	if len(types) == 0 {
		types = []string{"Movie", "Series"}
	}
	q := url.Values{}
	q.Set("Recursive", "true")
	q.Set("IncludeItemTypes", strings.Join(types, ","))
	q.Set("Fields", "Overview,ProductionYear,RunTimeTicks,CommunityRating,Genres")
	q.Set("SortBy", "SortName")
	q.Set("SortOrder", "Ascending")
	q.Set("EnableImageTypes", "Primary")
	q.Set("ImageTypeLimit", "1")
	if p.Query != "" {
		q.Set("SearchTerm", p.Query)
	}
	if len(p.Genres) > 0 {
		// Jellyfin treats the pipe-delimited Genres param as an OR filter.
		q.Set("Genres", strings.Join(p.Genres, "|"))
	}
	if p.Limit > 0 {
		q.Set("Limit", strconv.Itoa(p.Limit))
	}
	if p.StartIndex > 0 {
		q.Set("StartIndex", strconv.Itoa(p.StartIndex))
	}

	var out struct {
		Items            []Item `json:"Items"`
		TotalRecordCount int    `json:"TotalRecordCount"`
	}
	if err := c.getJSON(ctx, "/Items", q, &out); err != nil {
		return nil, 0, err
	}
	return out.Items, out.TotalRecordCount, nil
}

// ListGenres returns the distinct genre names present in the library for the
// given item types (defaulting to Movie + Series), sorted by name.
func (c *Client) ListGenres(ctx context.Context, types []string) ([]string, error) {
	if len(types) == 0 {
		types = []string{"Movie", "Series"}
	}
	q := url.Values{}
	q.Set("IncludeItemTypes", strings.Join(types, ","))
	q.Set("SortBy", "SortName")
	q.Set("SortOrder", "Ascending")
	var out struct {
		Items []struct {
			Name string `json:"Name"`
		} `json:"Items"`
	}
	if err := c.getJSON(ctx, "/Genres", q, &out); err != nil {
		return nil, err
	}
	genres := make([]string, 0, len(out.Items))
	for _, g := range out.Items {
		if g.Name != "" {
			genres = append(genres, g.Name)
		}
	}
	return genres, nil
}

// GetItem fetches a single item by ID. It returns (nil, nil) if not found.
func (c *Client) GetItem(ctx context.Context, id string) (*Item, error) {
	q := url.Values{}
	q.Set("Ids", id)
	q.Set("Recursive", "true")
	q.Set("Fields", "Overview,ProductionYear,RunTimeTicks,CommunityRating,Genres")
	q.Set("EnableImageTypes", "Primary")
	var out struct {
		Items []Item `json:"Items"`
	}
	if err := c.getJSON(ctx, "/Items", q, &out); err != nil {
		return nil, err
	}
	if len(out.Items) == 0 {
		return nil, nil
	}
	return &out.Items[0], nil
}

// FindByTMDB looks for a library item matching a TMDB id, so write-ins for
// titles already in the library (even ones hidden by a poll's genre filter)
// aren't re-requested. Jellyfin can't filter by provider id server-side, so it
// searches by title and matches on the Tmdb provider id, falling back to an
// exact title+year match only for items that carry no TMDB id at all. itemType
// is the Jellyfin type ("Movie" | "Series"). Returns (nil, nil) on no match.
func (c *Client) FindByTMDB(ctx context.Context, tmdbID int, title, itemType string, year int) (*Item, error) {
	if tmdbID <= 0 || strings.TrimSpace(title) == "" {
		return nil, nil
	}
	q := url.Values{}
	q.Set("Recursive", "true")
	q.Set("IncludeItemTypes", itemType)
	q.Set("SearchTerm", title)
	q.Set("Fields", "ProviderIds,ProductionYear")
	q.Set("Limit", "25")
	var out struct {
		Items []Item `json:"Items"`
	}
	if err := c.getJSON(ctx, "/Items", q, &out); err != nil {
		return nil, err
	}
	want := strconv.Itoa(tmdbID)
	wantTitle := strings.ToLower(strings.TrimSpace(title))
	var fallback *Item
	for i := range out.Items {
		it := &out.Items[i]
		if it.TMDBID() == want {
			return it, nil
		}
		// Title+year fallback only when the library item has no TMDB id, so an
		// id mismatch reflects genuinely different media rather than absent
		// metadata. Require an exact normalized title and matching year.
		if fallback == nil && it.TMDBID() == "" &&
			strings.ToLower(strings.TrimSpace(it.Name)) == wantTitle &&
			(year == 0 || it.ProductionYear == year) {
			fallback = it
		}
	}
	return fallback, nil
}

// FetchImage retrieves an item image (e.g. imageType "Primary"). The caller is
// responsible for closing and streaming the returned response body.
func (c *Client) FetchImage(ctx context.Context, itemID, imageType string, query url.Values) (*http.Response, error) {
	u := fmt.Sprintf("%s/Items/%s/Images/%s", c.baseURL, url.PathEscape(itemID), url.PathEscape(imageType))
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", authHeader(c.apiKey))
	return c.http.Do(req)
}

// Ping verifies connectivity to the server's public info endpoint.
func (c *Client) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/System/Info/Public", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("jellyfin ping: %s", resp.Status)
	}
	return nil
}

func (c *Client) get(ctx context.Context, path string, q url.Values) (*http.Response, error) {
	u := c.baseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", authHeader(c.apiKey))
	req.Header.Set("Accept", "application/json")
	return c.http.Do(req)
}

func (c *Client) getJSON(ctx context.Context, path string, q url.Values, out any) error {
	resp, err := c.get(ctx, path, q)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		return fmt.Errorf("jellyfin GET %s: %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
