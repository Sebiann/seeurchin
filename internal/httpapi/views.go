package httpapi

import (
	"context"
	"encoding/json"
	"time"

	"github.com/enderu/seeurchin/internal/poll"
	"github.com/enderu/seeurchin/internal/voting"
)

// timerView is the active round's countdown as the client needs it: an absolute
// ClosesAt to count down to (paired with the poll view's ServerNow so the
// client can correct for clock skew), a TotalSec to size the ring + ramp, and
// the paused/armed flags that drive the host controls. nil when the current
// round has no timer.
type timerView struct {
	Mode      string     `json:"mode"`                // "quick" | "scheduled"
	ClosesAt  *time.Time `json:"closes_at,omitempty"` // nil when armed or paused
	TotalSec  int        `json:"total_sec,omitempty"` // full intended length of this round
	PausedSec int        `json:"paused_sec,omitempty"`
	Armed     bool       `json:"armed,omitempty"` // quick: configured but never started
	Running   bool       `json:"running"`         // closes_at set and still in the future
}

// pollView is the client-facing representation of a poll's full state.
type pollView struct {
	Code              string               `json:"code"`
	Title             string               `json:"title"`
	Status            string               `json:"status"`
	LibraryScope      string               `json:"library_scope"`
	SubmissionRules   poll.SubmissionRules `json:"submission_rules"`
	MinToSubmit       int                  `json:"min_to_submit"`
	VotingMethod      string               `json:"voting_method"`
	VotingMethodLabel string               `json:"voting_method_label"`
	VotingConfig      json.RawMessage      `json:"voting_config"`
	AllowGuests       bool                 `json:"allow_guests"`
	ResultsLive       bool                 `json:"results_live"`
	RevealNominators  bool                 `json:"reveal_nominators"`
	RevealScope       string               `json:"reveal_scope"`
	Genres            []string             `json:"genres"`
	AllowWriteins     bool                 `json:"allow_writeins"`
	AutoRequestWinner bool                 `json:"auto_request_winner"`
	PasscodeRequired  bool                 `json:"passcode_required"`
	SeerrEnabled      bool                 `json:"seerr_enabled"`
	LoginEnabled      bool                 `json:"login_enabled"`
	ParticipantCount  int                  `json:"participant_count"`
	VoterCount        int                  `json:"voter_count"`
	Participants      []participantView    `json:"participants"`
	Nominations       []nominationView     `json:"nominations"`
	Me                *meView              `json:"me"`
	Results           *resultsView         `json:"results,omitempty"`
	ShareURL          string               `json:"share_url"`
	Timer             *timerView           `json:"timer,omitempty"`
	ServerNow         time.Time            `json:"server_now"`
}

type nominationView struct {
	ID             string `json:"id"`
	ItemID         string `json:"item_id"`
	Title          string `json:"title"`
	Year           int    `json:"year"`
	Type           string `json:"type"`
	Runtime        int    `json:"runtime_minutes"`
	Overview       string `json:"overview"`
	ImageTag       string `json:"image_tag"`
	Source         string `json:"source,omitempty"`     // "seerr" for write-ins
	PosterURL      string `json:"poster_url,omitempty"` // external poster (write-ins)
	MediaType      string `json:"media_type,omitempty"`
	NominatorCount int    `json:"nominator_count"`
	MineNominated  bool   `json:"mine_nominated"`
}

type participantView struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Linked      bool   `json:"linked"` // tied to a Jellyfin account
	IsHost      bool   `json:"is_host"`
	HasVoted    bool   `json:"has_voted"`
}

type meView struct {
	ID              string         `json:"id"`
	DisplayName     string         `json:"display_name"`
	IsHost          bool           `json:"is_host"`
	NominationCount int            `json:"nomination_count"`
	HasVoted        bool           `json:"has_voted"`
	Linked          bool           `json:"linked"`
	MySelections    map[string]int `json:"my_selections"`
}

type resultsView struct {
	Method  string               `json:"method"`
	Winners []resultEntry        `json:"winners"`
	Ranked  []resultEntry        `json:"ranked"`
	Rounds  []voting.RoundResult `json:"rounds,omitempty"`
	// TiedIDs: the co-winners a random tie-break chose between (nomination IDs).
	TiedIDs []string `json:"tied_ids,omitempty"`
}

type resultEntry struct {
	NominationID  string   `json:"nomination_id"`
	Title         string   `json:"title"`
	Score         float64  `json:"score"`
	Nominators    []string `json:"nominators,omitempty"`     // display names, when the poll reveals them
	RequestStatus string   `json:"request_status,omitempty"` // Seerr status for a winning write-in
}

// buildPollView assembles the full state for poll p as seen by participant me
// (which may be nil for a not-yet-joined visitor).
func (s *Server) buildPollView(ctx context.Context, p *poll.Poll, me *poll.Participant) (pollView, error) {
	participants, err := s.repo.ListParticipants(ctx, p.ID)
	if err != nil {
		return pollView{}, err
	}
	noms, err := s.repo.ListNominations(ctx, p.ID)
	if err != nil {
		return pollView{}, err
	}
	voterCount, err := s.repo.CountVoters(ctx, p.ID)
	if err != nil {
		return pollView{}, err
	}

	methodLabel := p.VotingMethod
	if m, ok := voting.Get(p.VotingMethod); ok {
		methodLabel = m.Label()
	}

	now := time.Now()
	view := pollView{
		Timer:             buildTimerView(p, now),
		ServerNow:         now.UTC(),
		Code:              p.Code,
		Title:             p.Title,
		Status:            string(p.Status),
		LibraryScope:      string(p.LibraryScope),
		SubmissionRules:   p.SubmissionRules,
		MinToSubmit:       p.SubmissionRules.MinToSubmit(),
		VotingMethod:      p.VotingMethod,
		VotingMethodLabel: methodLabel,
		VotingConfig:      p.VotingConfig,
		AllowGuests:       p.AllowGuests,
		ResultsLive:       p.ResultsLive,
		RevealNominators:  p.RevealNominators,
		RevealScope:       p.RevealScope,
		Genres:            genresOrEmpty(p.Genres),
		AllowWriteins:     p.AllowWriteins,
		AutoRequestWinner: p.AutoRequestWinner,
		PasscodeRequired:  p.PasscodeHash != "",
		SeerrEnabled:      s.seerrEnabled(),
		LoginEnabled:      s.cfg.EnableUserLogin,
		ParticipantCount:  len(participants),
		VoterCount:        voterCount,
		ShareURL:          s.cfg.BaseURL + "/p/" + p.Code,
		Participants:      []participantView{},
		Nominations:       []nominationView{}, // serialize empty as [] not null
	}
	// Names are only shown to people who have joined (keeps passcode polls private).
	if me != nil {
		for _, pt := range participants {
			// shortcut: one query per participant, fine for group-sized polls; batch it if polls get large.
			voted, err := s.repo.HasVoted(ctx, p.ID, pt.ID)
			if err != nil {
				return pollView{}, err
			}
			view.Participants = append(view.Participants, participantView{ID: pt.ID, DisplayName: pt.DisplayName, Linked: pt.JellyfinUserID != "", IsHost: pt.IsHost(), HasVoted: voted})
		}
	}

	for _, n := range noms {
		mine := false
		if me != nil {
			for _, nominator := range n.Nominators {
				if nominator == me.ID {
					mine = true
					break
				}
			}
		}
		view.Nominations = append(view.Nominations, nominationView{
			ID:             n.ID,
			ItemID:         n.JellyfinItemID,
			Title:          n.Snapshot.Title,
			Year:           n.Snapshot.Year,
			Type:           n.Snapshot.Type,
			Runtime:        n.Snapshot.Runtime,
			Overview:       n.Snapshot.Overview,
			ImageTag:       n.Snapshot.ImageTag,
			Source:         n.Snapshot.Source,
			PosterURL:      n.Snapshot.PosterURL,
			MediaType:      n.Snapshot.MediaType,
			NominatorCount: len(n.Nominators),
			MineNominated:  mine,
		})
	}

	if me != nil {
		count, err := s.repo.CountNominationsByParticipant(ctx, p.ID, me.ID)
		if err != nil {
			return pollView{}, err
		}
		voted, err := s.repo.HasVoted(ctx, p.ID, me.ID)
		if err != nil {
			return pollView{}, err
		}
		view.Me = &meView{
			ID:              me.ID,
			DisplayName:     me.DisplayName,
			IsHost:          me.IsHost(),
			NominationCount: count,
			HasVoted:        voted,
			Linked:          me.JellyfinUserID != "",
		}
		if p.Status == poll.StatusRound2 || p.Status == poll.StatusClosed {
			votes, err := s.repo.ListVotes(ctx, p.ID)
			if err != nil {
				return pollView{}, err
			}
			sel := map[string]int{}
			for _, v := range votes {
				if v.ParticipantID == me.ID {
					sel[v.NominationID] = v.Value
				}
			}
			view.Me.MySelections = sel
		}
	}

	if resultsVisible(p) {
		rv, err := s.computeResults(ctx, p, noms)
		if err != nil {
			return pollView{}, err
		}
		view.Results = rv
	}

	return view, nil
}

// buildTimerView computes the active round's countdown for the client, or nil
// when the current round has no timer (so the UI keeps the manual advance CTA).
// It works only from fields fixed at creation/transition plus now.
func buildTimerView(p *poll.Poll, now time.Time) *timerView {
	if p.DeadlineMode == poll.DeadlineNone {
		return nil
	}
	var closesAt *time.Time
	var durationSec int
	switch p.Status {
	case poll.StatusRound1:
		closesAt, durationSec = p.Round1ClosesAt, p.Round1DurationSec
	case poll.StatusRound2:
		closesAt, durationSec = p.Round2ClosesAt, p.Round2DurationSec
	default:
		return nil // closed: no active timer
	}

	paused := p.TimerPausedSec
	armed := p.DeadlineMode == poll.DeadlineQuick && durationSec > 0 && closesAt == nil && paused == 0
	if closesAt == nil && paused == 0 && !armed {
		return nil // this round has no timer (e.g. scheduled with an open round)
	}

	tv := &timerView{Mode: string(p.DeadlineMode), PausedSec: paused, Armed: armed}
	switch p.DeadlineMode {
	case poll.DeadlineQuick:
		tv.TotalSec = durationSec
	case poll.DeadlineScheduled:
		start := p.CreatedAt
		if p.Status == poll.StatusRound2 && p.Round1ClosesAt != nil {
			start = *p.Round1ClosesAt
		}
		if closesAt != nil {
			if total := int(closesAt.Sub(start).Seconds()); total > 0 {
				tv.TotalSec = total
			}
		}
	}
	if closesAt != nil {
		tv.ClosesAt = closesAt
		tv.Running = closesAt.After(now)
	}
	return tv
}

// genresOrEmpty guarantees a non-nil slice so it serializes as [] not null.
func genresOrEmpty(g []string) []string {
	if g == nil {
		return []string{}
	}
	return g
}

// resultsVisible reports whether tallies may be shown: always once closed, and
// during round 2 when the host enabled live results.
func resultsVisible(p *poll.Poll) bool {
	return p.Status == poll.StatusClosed || (p.Status == poll.StatusRound2 && p.ResultsLive)
}

func (s *Server) computeResults(ctx context.Context, p *poll.Poll, noms []poll.Nomination) (*resultsView, error) {
	res, _, err := s.svc.Results(ctx, p)
	if err != nil {
		return nil, err
	}
	title := make(map[string]string, len(noms))
	snapByID := make(map[string]poll.ItemSnapshot, len(noms))
	for _, n := range noms {
		title[n.ID] = n.Snapshot.Title
		snapByID[n.ID] = n.Snapshot
	}

	// Optionally reveal who nominated each title, but only once the poll is
	// closed (so it never biases live voting). "winner" scope reveals only the
	// winner(s); "all" reveals every title.
	var nominators map[string][]string
	if p.RevealNominators && p.Status == poll.StatusClosed {
		nominators, err = s.nominatorNames(ctx, p, noms)
		if err != nil {
			return nil, err
		}
	}
	winnerSet := make(map[string]bool, len(res.WinnerIDs))
	for _, id := range res.WinnerIDs {
		winnerSet[id] = true
	}

	// Initialize slices so an empty tally serializes as [] not null — the
	// frontend dereferences r.ranked / r.winners without a per-field guard.
	rv := &resultsView{
		Method:  res.Method,
		Rounds:  res.Rounds,
		TiedIDs: res.TiedIDs,
		Winners: []resultEntry{},
		Ranked:  []resultEntry{},
	}
	score := make(map[string]float64, len(res.Ranked))
	for _, r := range res.Ranked {
		score[r.NominationID] = r.Score
		e := resultEntry{NominationID: r.NominationID, Title: title[r.NominationID], Score: r.Score}
		if nominators != nil && (p.RevealScope == poll.RevealAll || winnerSet[r.NominationID]) {
			e.Nominators = nominators[r.NominationID]
		}
		rv.Ranked = append(rv.Ranked, e)
	}
	for _, id := range res.WinnerIDs {
		e := resultEntry{NominationID: id, Title: title[id], Score: score[id]}
		if nominators != nil {
			e.Nominators = nominators[id]
		}
		// Surface the Seerr request status for a winning write-in.
		if snap := snapByID[id]; snap.Source == poll.SourceSeerr {
			if req, err := s.repo.GetSeerrRequest(ctx, p.ID, id); err == nil && req != nil {
				e.RequestStatus = req.Status
			}
		}
		rv.Winners = append(rv.Winners, e)
	}
	return rv, nil
}

// nominatorNames maps each nomination ID to the display names of the
// participants who nominated it.
func (s *Server) nominatorNames(ctx context.Context, p *poll.Poll, noms []poll.Nomination) (map[string][]string, error) {
	participants, err := s.repo.ListParticipants(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	name := make(map[string]string, len(participants))
	for _, pt := range participants {
		name[pt.ID] = pt.DisplayName
	}
	out := make(map[string][]string, len(noms))
	for _, n := range noms {
		names := make([]string, 0, len(n.Nominators))
		for _, id := range n.Nominators {
			if nm := name[id]; nm != "" {
				names = append(names, nm)
			}
		}
		out[n.ID] = names
	}
	return out, nil
}
