package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/enderu/seeurchin/internal/auth"
	"github.com/enderu/seeurchin/internal/config"
	"github.com/enderu/seeurchin/internal/jellyfin"
	"github.com/enderu/seeurchin/internal/poll"
	"github.com/enderu/seeurchin/internal/store"
)

// jellyMockUser is a credential the fake Jellyfin server accepts.
type jellyMockUser struct {
	id, name, pw string
	admin        bool
}

// newJellyfinMock stands up a fake Jellyfin server handling
// POST /Users/AuthenticateByName and GET /Users for the given users.
func newJellyfinMock(t *testing.T, users ...jellyMockUser) *httptest.Server {
	t.Helper()
	m := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/Users" {
			var list []map[string]any
			for _, u := range users {
				list = append(list, map[string]any{"Id": u.id, "Name": u.name})
			}
			_ = json.NewEncoder(w).Encode(list)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/Users/AuthenticateByName" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body struct{ Username, Pw string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		for _, u := range users {
			if u.name == body.Username && u.pw == body.Pw {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"AccessToken": "tok-" + u.id,
					"User": map[string]any{
						"Id":     u.id,
						"Name":   u.name,
						"Policy": map[string]any{"IsAdministrator": u.admin},
					},
				})
				return
			}
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(m.Close)
	return m
}

// newAuthServer builds a test server with the given auth-relevant config and a
// Jellyfin client pointed at jfURL.
func newAuthServer(t *testing.T, cfg config.Config, jfURL string) *httptest.Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	resolver := fakeResolver{items: map[string]*poll.ResolvedItem{
		"m1": {ID: "m1", Title: "Dune", Type: "Movie", Year: 2021},
		"m2": {ID: "m2", Title: "Arrival", Type: "Movie", Year: 2016},
	}}
	cfg.BaseURL = "http://example.test"
	cfg.SessionSecret = []byte("test-secret-test-secret")
	svc := poll.NewService(st, resolver, 0)
	srv := NewServer(cfg, svc, st, jellyfin.New(jfURL, "k"), nil, auth.NewSessions(cfg.SessionSecret), NewHub())
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)
	return ts
}

func TestUserLoginGatesCreation(t *testing.T) {
	jf := newJellyfinMock(t, jellyMockUser{id: "u1", name: "alice", pw: "pw"})
	ts := newAuthServer(t, config.Config{EnableUserLogin: true}, jf.URL)
	c := newClient(t)

	// Session reports login is enabled and we're not authed.
	var sess struct {
		LoginEnabled  bool `json:"login_enabled"`
		Authenticated bool `json:"authenticated"`
	}
	do(t, c, http.MethodGet, ts.URL+"/api/user/session", nil, &sess)
	if !sess.LoginEnabled || sess.Authenticated {
		t.Fatalf("session = %+v, want enabled & unauth", sess)
	}

	// Creating a poll without login is rejected.
	if code := do(t, c, http.MethodPost, ts.URL+"/api/polls", map[string]any{
		"title": "x", "host_name": "Alice", "library_scope": "movie", "voting_method": "approval", "allow_guests": true,
	}, nil); code != http.StatusUnauthorized {
		t.Fatalf("create pre-login = %d, want 401", code)
	}

	// Bad credentials rejected.
	if code := do(t, c, http.MethodPost, ts.URL+"/api/user/login", map[string]string{"username": "alice", "password": "nope"}, nil); code != http.StatusUnauthorized {
		t.Fatalf("bad login = %d, want 401", code)
	}

	// Good login, then creation succeeds and host name defaults to Jellyfin name.
	if code := do(t, c, http.MethodPost, ts.URL+"/api/user/login", map[string]string{"username": "alice", "password": "pw"}, nil); code != http.StatusOK {
		t.Fatalf("login = %d, want 200", code)
	}
	var created pollView
	if code := do(t, c, http.MethodPost, ts.URL+"/api/polls", map[string]any{
		"title": "Movie Night", "library_scope": "movie", "voting_method": "approval", "allow_guests": true,
	}, &created); code != http.StatusCreated {
		t.Fatalf("create post-login = %d, want 201", code)
	}
	if created.Me == nil || created.Me.DisplayName != "alice" {
		t.Fatalf("host display name = %+v, want defaulted to 'alice'", created.Me)
	}
}

func TestUserLoginDisabledIsPassthrough(t *testing.T) {
	ts := newAuthServer(t, config.Config{EnableUserLogin: false}, "http://unused")
	c := newClient(t)
	if code := do(t, c, http.MethodPost, ts.URL+"/api/polls", map[string]any{
		"title": "x", "host_name": "Alice", "library_scope": "movie", "voting_method": "approval", "allow_guests": true,
	}, nil); code != http.StatusCreated {
		t.Fatalf("create with login disabled = %d, want 201", code)
	}
}

func TestPerPollPasscode(t *testing.T) {
	ts := newTestServer(t) // login disabled; passcode is independent
	host := newClient(t)

	var created pollView
	do(t, host, http.MethodPost, ts.URL+"/api/polls", map[string]any{
		"title": "Secret", "host_name": "Alice", "library_scope": "movie",
		"voting_method": "approval", "allow_guests": true, "passcode": "swordfish",
	}, &created)
	if !created.PasscodeRequired {
		t.Fatal("poll should report passcode_required")
	}

	// Guest cannot join without / with the wrong passcode, but can with the right one.
	guest := newClient(t)
	if code := do(t, guest, http.MethodPost, ts.URL+"/api/polls/"+created.Code+"/join", map[string]string{"display_name": "Bob"}, nil); code != http.StatusForbidden {
		t.Fatalf("join without passcode = %d, want 403", code)
	}
	if code := do(t, guest, http.MethodPost, ts.URL+"/api/polls/"+created.Code+"/join", map[string]string{"display_name": "Bob", "passcode": "wrong"}, nil); code != http.StatusForbidden {
		t.Fatalf("join wrong passcode = %d, want 403", code)
	}
	if code := do(t, guest, http.MethodPost, ts.URL+"/api/polls/"+created.Code+"/join", map[string]string{"display_name": "Bob", "passcode": "swordfish"}, nil); code != http.StatusOK {
		t.Fatalf("join correct passcode = %d, want 200", code)
	}
}

func TestJellyfinLinking(t *testing.T) {
	jf := newJellyfinMock(t,
		jellyMockUser{id: "u-alice", name: "alice", pw: "pw"},
		jellyMockUser{id: "u-bob", name: "bob", pw: "pw"},
		jellyMockUser{id: "u-carl", name: "carl", pw: "pw"},
	)
	ts := newAuthServer(t, config.Config{EnableUserLogin: true}, jf.URL)
	login := func(c *http.Client, name string) {
		t.Helper()
		if code := do(t, c, http.MethodPost, ts.URL+"/api/user/login", map[string]string{"username": name, "password": "pw"}, nil); code != http.StatusOK {
			t.Fatalf("login %s = %d", name, code)
		}
	}

	host := newClient(t)
	login(host, "alice")
	var v pollView
	do(t, host, http.MethodPost, ts.URL+"/api/polls", map[string]any{
		"title": "Movie Night", "library_scope": "movie", "voting_method": "approval", "allow_guests": true,
	}, &v)
	base := ts.URL + "/api/polls/" + v.Code

	// A guest joins without signing in, then links their account.
	phone := newClient(t)
	do(t, phone, http.MethodPost, base+"/join", map[string]string{"display_name": "Bob"}, &v)
	bobID := v.Me.ID
	if code := do(t, phone, http.MethodPost, base+"/link", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("link before sign-in = %d, want 401", code)
	}
	login(phone, "bob")
	do(t, phone, http.MethodPost, base+"/link", nil, &v)
	if !v.Me.Linked || v.Me.ID != bobID {
		t.Fatalf("after link me = %+v, want linked %s", v.Me, bobID)
	}

	// Signing in on a new device lands on the same entry with no join.
	laptop := newClient(t)
	login(laptop, "bob")
	do(t, laptop, http.MethodGet, base, nil, &v)
	if v.Me == nil || v.Me.ID != bobID {
		t.Fatalf("new device me = %+v, want %s", v.Me, bobID)
	}

	// The host links another guest by hand; only real accounts are accepted.
	carl := newClient(t)
	do(t, carl, http.MethodPost, base+"/join", map[string]string{"display_name": "Carl"}, &v)
	carlID := v.Me.ID
	if code := do(t, carl, http.MethodGet, base+"/jellyfin-users", nil, nil); code != http.StatusForbidden {
		t.Fatalf("guest listing users = %d, want 403", code)
	}
	var users []jellyfin.User
	do(t, host, http.MethodGet, base+"/jellyfin-users", nil, &users)
	if len(users) != 3 {
		t.Fatalf("users = %+v, want 3", users)
	}
	if code := do(t, host, http.MethodPost, base+"/participants/"+carlID+"/link", map[string]string{"jellyfin_user_id": "made-up"}, nil); code != http.StatusBadRequest {
		t.Fatalf("link unknown account = %d, want 400", code)
	}
	if code := do(t, host, http.MethodPost, base+"/participants/"+carlID+"/link", map[string]string{"jellyfin_user_id": "u-carl"}, &v); code != http.StatusOK {
		t.Fatalf("host link = %d", code)
	}
	carl2 := newClient(t)
	login(carl2, "carl")
	do(t, carl2, http.MethodGet, base, nil, &v)
	if v.Me == nil || v.Me.ID != carlID {
		t.Fatalf("carl on new device me = %+v, want %s", v.Me, carlID)
	}
}
