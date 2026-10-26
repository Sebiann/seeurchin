package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/enderu/seeurchin/internal/config"
)

func TestAdminDisabledReturns404(t *testing.T) {
	// Admin requires login enabled AND an allowlist/flag; with neither it's off.
	ts := newAuthServer(t, config.Config{}, "http://unused")
	c := newClient(t)
	for _, path := range []string{"/api/admin/session", "/api/admin/polls"} {
		if code := do(t, c, http.MethodGet, ts.URL+path, nil, nil); code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, code)
		}
	}
}

func TestAdminAuthorization(t *testing.T) {
	jf := newJellyfinMock(t,
		jellyMockUser{id: "u1", name: "alice", pw: "pw"},             // ordinary user
		jellyMockUser{id: "u2", name: "boss", pw: "pw"},              // allowlisted admin
		jellyMockUser{id: "u3", name: "root", pw: "pw", admin: true}, // jellyfin admin
	)
	ts := newAuthServer(t, config.Config{
		EnableUserLogin:          true,
		AdminUsers:               []string{"boss"},
		AdminAllowJellyfinAdmins: true,
	}, jf.URL)

	login := func(c *http.Client, name string) {
		if code := do(t, c, http.MethodPost, ts.URL+"/api/user/login", map[string]string{"username": name, "password": "pw"}, nil); code != http.StatusOK {
			t.Fatalf("login %s = %d", name, code)
		}
	}

	// Not signed in → 401.
	if code := do(t, newClient(t), http.MethodGet, ts.URL+"/api/admin/polls", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("admin polls anon = %d, want 401", code)
	}

	// Ordinary user → authenticated but not authorized (403).
	alice := newClient(t)
	login(alice, "alice")
	var sess struct{ Authenticated, Authorized bool }
	do(t, alice, http.MethodGet, ts.URL+"/api/admin/session", nil, &sess)
	if !sess.Authenticated || sess.Authorized {
		t.Fatalf("alice session = %+v, want auth & !authz", sess)
	}
	if code := do(t, alice, http.MethodGet, ts.URL+"/api/admin/polls", nil, nil); code != http.StatusForbidden {
		t.Fatalf("alice admin polls = %d, want 403", code)
	}

	// Allowlisted user and Jellyfin admin both get in.
	for _, name := range []string{"boss", "root"} {
		c := newClient(t)
		login(c, name)
		do(t, c, http.MethodGet, ts.URL+"/api/admin/session", nil, &sess)
		if !sess.Authorized {
			t.Fatalf("%s should be authorized: %+v", name, sess)
		}
		if code := do(t, c, http.MethodGet, ts.URL+"/api/admin/polls", nil, nil); code != http.StatusOK {
			t.Fatalf("%s admin polls = %d, want 200", name, code)
		}
	}
}

func TestAdminDashboardFlow(t *testing.T) {
	jf := newJellyfinMock(t,
		jellyMockUser{id: "u1", name: "alice", pw: "pw"},
		jellyMockUser{id: "u2", name: "boss", pw: "pw"},
	)
	ts := newAuthServer(t, config.Config{
		EnableUserLogin: true,
		AdminUsers:      []string{"boss"},
	}, jf.URL)

	host := newClient(t)
	admin := newClient(t)
	if code := do(t, host, http.MethodPost, ts.URL+"/api/user/login", map[string]string{"username": "alice", "password": "pw"}, nil); code != http.StatusOK {
		t.Fatalf("host login = %d", code)
	}
	if code := do(t, admin, http.MethodPost, ts.URL+"/api/user/login", map[string]string{"username": "boss", "password": "pw"}, nil); code != http.StatusOK {
		t.Fatalf("admin login = %d", code)
	}

	// Build a closed poll with a winner via the normal participant API.
	var created pollView
	if code := do(t, host, http.MethodPost, ts.URL+"/api/polls", map[string]any{
		"title": "Movie Night", "library_scope": "movie",
		"voting_method": "approval",
		"voting_config": json.RawMessage(`{"votes_per_user":3,"max_votes_per_option":1,"allow_self_vote":true}`),
		"allow_guests":  true,
	}, &created); code != http.StatusCreated {
		t.Fatalf("create = %d", code)
	}
	pc := created.Code
	for _, id := range []string{"m1", "m2"} {
		do(t, host, http.MethodPost, ts.URL+"/api/polls/"+pc+"/nominations", map[string]string{"item_id": id}, nil)
	}
	var r2 pollView
	do(t, host, http.MethodPost, ts.URL+"/api/polls/"+pc+"/advance", nil, &r2)
	m1 := nomIDByItem(r2, "m1")
	do(t, host, http.MethodPost, ts.URL+"/api/polls/"+pc+"/votes", map[string]any{"selections": map[string]int{m1: 1}}, nil)
	do(t, host, http.MethodPost, ts.URL+"/api/polls/"+pc+"/advance", nil, nil) // -> closed

	// History shows the closed poll with winner + counts.
	var list []adminPollView
	if code := do(t, admin, http.MethodGet, ts.URL+"/api/admin/polls", nil, &list); code != http.StatusOK {
		t.Fatalf("polls = %d", code)
	}
	var row *adminPollView
	for i := range list {
		if list[i].Code == pc {
			row = &list[i]
		}
	}
	if row == nil {
		t.Fatal("closed poll missing from history")
	}
	if row.Status != "closed" || row.WinnerTitle != "Dune" {
		t.Fatalf("row = %+v, want closed/Dune", *row)
	}
	if row.ParticipantCount != 1 || row.NominationCount != 2 || row.VoterCount != 1 {
		t.Fatalf("counts = %+v, want 1/2/1", *row)
	}

	// Ballots expose each participant's individual choices to the admin.
	var ballots []adminBallotView
	if code := do(t, admin, http.MethodGet, ts.URL+"/api/admin/polls/"+pc+"/ballots", nil, &ballots); code != http.StatusOK {
		t.Fatalf("ballots = %d", code)
	}
	if len(ballots) != 1 || !ballots[0].HasVoted || !ballots[0].IsHost || len(ballots[0].Selections) != 1 {
		t.Fatalf("ballots = %+v, want one host ballot with one selection", ballots)
	}
	if sel := ballots[0].Selections[0]; sel.NominationID != m1 || sel.Title != "Dune" || sel.Value != 1 {
		t.Fatalf("selection = %+v, want m1/Dune/1", sel)
	}
	// ...but not to a signed-in non-admin.
	if code := do(t, host, http.MethodGet, ts.URL+"/api/admin/polls/"+pc+"/ballots", nil, nil); code != http.StatusForbidden {
		t.Fatalf("non-admin ballots = %d, want 403", code)
	}

	// Force-advance a fresh round-1 poll without being its host.
	var p2 pollView
	do(t, host, http.MethodPost, ts.URL+"/api/polls", map[string]any{
		"title": "Other", "library_scope": "movie", "voting_method": "approval", "allow_guests": true,
	}, &p2)
	for _, id := range []string{"m1", "m2"} {
		do(t, host, http.MethodPost, ts.URL+"/api/polls/"+p2.Code+"/nominations", map[string]string{"item_id": id}, nil)
	}
	var adv adminPollView
	if code := do(t, admin, http.MethodPost, ts.URL+"/api/admin/polls/"+p2.Code+"/advance", nil, &adv); code != http.StatusOK {
		t.Fatalf("admin advance = %d", code)
	}
	if adv.Status != "round2" {
		t.Fatalf("advanced status = %q, want round2", adv.Status)
	}

	// Delete it; it's then gone.
	if code := do(t, admin, http.MethodDelete, ts.URL+"/api/admin/polls/"+p2.Code, nil, nil); code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204", code)
	}
	if code := do(t, admin, http.MethodGet, ts.URL+"/api/admin/polls/"+p2.Code, nil, nil); code != http.StatusNotFound {
		t.Fatalf("get deleted = %d, want 404", code)
	}

	// Logout drops admin access.
	do(t, admin, http.MethodPost, ts.URL+"/api/user/logout", nil, nil)
	if code := do(t, admin, http.MethodGet, ts.URL+"/api/admin/polls", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("polls post-logout = %d, want 401", code)
	}
}
