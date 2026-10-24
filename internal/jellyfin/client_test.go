package jellyfin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuthHeaderModernFormat(t *testing.T) {
	h := authHeader("secret-token")
	if !strings.HasPrefix(h, "MediaBrowser ") {
		t.Fatalf("header must use the MediaBrowser scheme, got %q", h)
	}
	for _, want := range []string{
		`Token="secret-token"`,
		`Client="seeurchin"`,
		`DeviceId="seeurchin-server"`,
		`Version="0.1.0"`,
	} {
		if !strings.Contains(h, want) {
			t.Errorf("header %q missing %q", h, want)
		}
	}
	// Guard against accidentally reintroducing legacy schemes.
	for _, legacy := range []string{"X-Emby", "api_key", "X-MediaBrowser"} {
		if strings.Contains(h, legacy) {
			t.Errorf("header must not contain legacy token %q: %q", legacy, h)
		}
	}
}

func TestAuthHeaderOmitsEmptyToken(t *testing.T) {
	h := authHeader("")
	if strings.Contains(h, "Token=") {
		t.Errorf("empty token should be omitted, got %q", h)
	}
}

func TestAuthenticateByName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/Users/AuthenticateByName" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if h := r.Header.Get("Authorization"); !strings.HasPrefix(h, "MediaBrowser ") || strings.Contains(h, "Token=") {
			t.Errorf("auth header = %q, want MediaBrowser without Token", h)
		}
		var body struct{ Username, Pw string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Username != "alice" || body.Pw != "hunter2" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"AccessToken": "tok",
			"User": map[string]any{
				"Id":     "user-123",
				"Name":   "Alice",
				"Policy": map[string]any{"IsAdministrator": true},
			},
		})
	}))
	defer srv.Close()
	c := New(srv.URL, "k")

	res, err := c.AuthenticateByName(context.Background(), "alice", "hunter2")
	if err != nil {
		t.Fatalf("AuthenticateByName: %v", err)
	}
	if res.UserID != "user-123" || res.Username != "Alice" || !res.IsAdmin {
		t.Fatalf("result = %+v, want id=user-123 name=Alice admin=true", res)
	}

	if _, err := c.AuthenticateByName(context.Background(), "alice", "wrong"); err == nil {
		t.Fatal("expected error on bad credentials")
	}
}

func TestFindByTMDB(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("Fields"); !strings.Contains(got, "ProviderIds") {
			t.Errorf("Fields = %q, want ProviderIds requested", got)
		}
		if got := r.URL.Query().Get("IncludeItemTypes"); got != "Movie" {
			t.Errorf("IncludeItemTypes = %q, want Movie", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Items":[
			{"Id":"a","Name":"Super 8","Type":"Movie","ProductionYear":2011,"ProviderIds":{"Tmdb":"37686"}},
			{"Id":"b","Name":"Super 8 (other)","Type":"Movie","ProductionYear":1936}
		]}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "k")

	// Matches by TMDB provider id regardless of title noise.
	got, err := c.FindByTMDB(context.Background(), 37686, "Super 8", "Movie", 2011)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != "a" {
		t.Fatalf("expected TMDB-id match on item a, got %+v", got)
	}

	// A different TMDB id with no exact title+year twin returns no match (the
	// 1936 item differs in title and year, so the fallback must not fire).
	got, err = c.FindByTMDB(context.Background(), 99999, "Super 8", "Movie", 2011)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected no match for unknown id, got %+v", got)
	}
}

func TestFindByTMDBTitleYearFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// No TMDB provider id on the library item, so the title+year fallback
		// is the only available signal.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Items":[{"Id":"x","Name":"Dune","Type":"Movie","ProductionYear":2021}]}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "k")

	got, err := c.FindByTMDB(context.Background(), 438631, "dune", "Movie", 2021)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != "x" {
		t.Fatalf("expected title+year fallback match, got %+v", got)
	}
	// Year mismatch must not match via the fallback.
	if got, _ := c.FindByTMDB(context.Background(), 438631, "dune", "Movie", 1984); got != nil {
		t.Fatalf("year mismatch should not match, got %+v", got)
	}
}

func TestSearchSendsModernHeaderAndParses(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/Items" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.URL.Query().Get("SearchTerm") != "matrix" {
			t.Errorf("SearchTerm = %q, want matrix", r.URL.Query().Get("SearchTerm"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Items":[{"Id":"abc","Name":"The Matrix","Type":"Movie","ProductionYear":1999,"RunTimeTicks":81600000000,"ImageTags":{"Primary":"tag1"}}],"TotalRecordCount":1}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "secret-token")
	items, total, err := c.Search(context.Background(), SearchParams{Query: "matrix", Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !strings.HasPrefix(gotAuth, `MediaBrowser `) || !strings.Contains(gotAuth, `Token="secret-token"`) {
		t.Fatalf("server saw non-modern auth header: %q", gotAuth)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("got %d items (total %d), want 1", len(items), total)
	}
	it := items[0]
	if it.Name != "The Matrix" || it.PrimaryImageTag() != "tag1" {
		t.Fatalf("unexpected item %+v", it)
	}
	if it.RuntimeMinutes() != 136 {
		t.Errorf("runtime = %d min, want 136", it.RuntimeMinutes())
	}
}

func TestListUsersSkipsDisabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Users" {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"Id": "u1", "Name": "Alice", "Policy": map[string]any{"IsDisabled": false}},
			{"Id": "u2", "Name": "Old", "Policy": map[string]any{"IsDisabled": true}},
		})
	}))
	defer srv.Close()
	users, err := New(srv.URL, "k").ListUsers(context.Background())
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 1 || users[0].ID != "u1" || users[0].Name != "Alice" {
		t.Fatalf("users = %+v, want only Alice", users)
	}
}
