package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
)

// createQuickPoll makes a host + a quick-timer poll and returns the code and the
// create response.
func createQuickPoll(t *testing.T, ts string, host *http.Client) (string, pollView) {
	t.Helper()
	var created pollView
	code := do(t, host, http.MethodPost, ts+"/api/polls", map[string]any{
		"title":               "Quick Night",
		"host_name":           "Alice",
		"library_scope":       "movie",
		"voting_method":       "approval",
		"voting_config":       json.RawMessage(`{"votes_per_user":3,"max_votes_per_option":1,"allow_self_vote":true}`),
		"allow_guests":        true,
		"deadline_mode":       "quick",
		"round1_duration_sec": 60,
		"round2_duration_sec": 30,
	}, &created)
	if code != http.StatusCreated {
		t.Fatalf("create poll status = %d", code)
	}
	return created.Code, created
}

func TestQuickTimerArmedThenStartedAndPaused(t *testing.T) {
	ts := newTestServer(t)
	host := newClient(t)
	code, created := createQuickPoll(t, ts.URL, host)

	if created.Timer == nil {
		t.Fatal("create response should carry a timer for a quick poll")
	}
	if !created.Timer.Armed || created.Timer.Running {
		t.Fatalf("round 1 should be armed (not running): %+v", created.Timer)
	}
	if created.Timer.TotalSec != 60 {
		t.Fatalf("total_sec = %d, want 60", created.Timer.TotalSec)
	}
	if created.ServerNow.IsZero() {
		t.Fatal("server_now should be set")
	}

	// Start → running with a close time.
	var started pollView
	if c := do(t, host, http.MethodPost, ts.URL+"/api/polls/"+code+"/timer/start", nil, &started); c != http.StatusOK {
		t.Fatalf("start status = %d", c)
	}
	if started.Timer == nil || !started.Timer.Running || started.Timer.ClosesAt == nil {
		t.Fatalf("timer should be running with a close time: %+v", started.Timer)
	}

	// Pause → frozen with a remaining count, no close time.
	var paused pollView
	if c := do(t, host, http.MethodPost, ts.URL+"/api/polls/"+code+"/timer/pause", nil, &paused); c != http.StatusOK {
		t.Fatalf("pause status = %d", c)
	}
	if paused.Timer == nil || paused.Timer.Running || paused.Timer.ClosesAt != nil {
		t.Fatalf("timer should be paused: %+v", paused.Timer)
	}
	if paused.Timer.PausedSec < 50 || paused.Timer.PausedSec > 61 {
		t.Fatalf("paused_sec = %d, want ~60", paused.Timer.PausedSec)
	}

	// Extend (+30) while paused.
	var extended pollView
	if c := do(t, host, http.MethodPost, ts.URL+"/api/polls/"+code+"/timer/extend",
		map[string]any{"add_seconds": 30}, &extended); c != http.StatusOK {
		t.Fatalf("extend status = %d", c)
	}
	if extended.Timer.PausedSec < 80 || extended.Timer.PausedSec > 91 {
		t.Fatalf("paused_sec after +30 = %d, want ~90", extended.Timer.PausedSec)
	}
}

func TestTimerControlsAreHostOnly(t *testing.T) {
	ts := newTestServer(t)
	host := newClient(t)
	code, _ := createQuickPoll(t, ts.URL, host)

	guest := newClient(t)
	if c := do(t, guest, http.MethodPost, ts.URL+"/api/polls/"+code+"/join",
		map[string]any{"display_name": "Bob"}, nil); c != http.StatusOK {
		t.Fatalf("join status = %d", c)
	}
	if c := do(t, guest, http.MethodPost, ts.URL+"/api/polls/"+code+"/timer/start", nil, nil); c != http.StatusForbidden {
		t.Fatalf("guest start status = %d, want 403", c)
	}
}

func TestUntimedPollHasNoTimerView(t *testing.T) {
	ts := newTestServer(t)
	host := newClient(t)
	var created pollView
	code := do(t, host, http.MethodPost, ts.URL+"/api/polls", map[string]any{
		"title":         "Plain Night",
		"host_name":     "Alice",
		"library_scope": "movie",
		"voting_method": "approval",
		"voting_config": json.RawMessage(`{"votes_per_user":3,"max_votes_per_option":1,"allow_self_vote":true}`),
		"allow_guests":  true,
	}, &created)
	if code != http.StatusCreated {
		t.Fatalf("create status = %d", code)
	}
	if created.Timer != nil {
		t.Fatalf("untimed poll should have no timer view, got %+v", created.Timer)
	}
}

func TestUpdateSettingsHostOnly(t *testing.T) {
	ts := newTestServer(t)
	host := newClient(t)
	code, _ := createQuickPoll(t, ts.URL, host)
	url := ts.URL + "/api/polls/" + code + "/settings"
	body := map[string]any{"voting_config": json.RawMessage(`{"votes_per_user":-1,"max_vetoes":-1}`), "results_live": true}

	guest := newClient(t)
	if c := do(t, guest, http.MethodPost, ts.URL+"/api/polls/"+code+"/join", map[string]any{"display_name": "Bob"}, nil); c != http.StatusOK {
		t.Fatalf("join status = %d", c)
	}
	if c := do(t, guest, http.MethodPatch, url, body, nil); c != http.StatusForbidden {
		t.Fatalf("guest settings status = %d, want 403", c)
	}
	var got pollView
	if c := do(t, host, http.MethodPatch, url, body, &got); c != http.StatusOK {
		t.Fatalf("host settings status = %d", c)
	}
	if !got.ResultsLive || string(got.VotingConfig) != `{"votes_per_user":-1,"max_vetoes":-1}` {
		t.Fatalf("settings not applied: live=%v cfg=%s", got.ResultsLive, got.VotingConfig)
	}
}
