package httpapi

import (
	"context"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/enderu/seeurchin/internal/poll"
	"github.com/enderu/seeurchin/internal/voting"
)

// isAdminUser reports whether a logged-in Jellyfin identity is authorized for
// the admin dashboard: its username is in the configured allowlist, or it is a
// Jellyfin administrator and that path is enabled.
func (s *Server) isAdminUser(id *userIdentity) bool {
	if id == nil {
		return false
	}
	for _, u := range s.cfg.AdminUsers {
		if strings.EqualFold(strings.TrimSpace(u), id.Name) {
			return true
		}
	}
	return s.cfg.AdminAllowJellyfinAdmins && id.JFAdmin
}

// requireAdmin gates the admin data endpoints: 404 when the dashboard isn't
// configured (so its existence isn't even advertised), 401 when the caller has
// no Jellyfin session, 403 when that account isn't authorized for admin.
func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.cfg.AdminEnabled() {
			s.writeJSON(w, http.StatusNotFound, errResp{"not found"})
			return
		}
		id, ok := s.currentUser(r)
		if !ok {
			s.writeJSON(w, http.StatusUnauthorized, errResp{"sign in with Jellyfin first"})
			return
		}
		if !s.isAdminUser(id) {
			s.writeJSON(w, http.StatusForbidden, errResp{"this account is not an admin"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// handleAdminSession lets the SPA decide what to render: 404 when the dashboard
// is disabled, otherwise whether the caller is signed in and authorized. The
// actual sign-in flow is the shared Jellyfin login at /api/user/login.
func (s *Server) handleAdminSession(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.AdminEnabled() {
		s.writeJSON(w, http.StatusNotFound, errResp{"not found"})
		return
	}
	id, authed := s.currentUser(r)
	s.writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": authed,
		"authorized":    authed && s.isAdminUser(id),
	})
}

// adminPollView is one row of the admin history list.
type adminPollView struct {
	Code              string     `json:"code"`
	Title             string     `json:"title"`
	Status            string     `json:"status"`
	VotingMethod      string     `json:"voting_method"`
	VotingMethodLabel string     `json:"voting_method_label"`
	DeadlineMode      string     `json:"deadline_mode,omitempty"`
	ParticipantCount  int        `json:"participant_count"`
	NominationCount   int        `json:"nomination_count"`
	VoterCount        int        `json:"voter_count"`
	WinnerTitle       string     `json:"winner_title,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	ClosedAt          *time.Time `json:"closed_at,omitempty"`
}

func (s *Server) buildAdminPollView(ctx context.Context, sum poll.PollSummary) adminPollView {
	p := sum.Poll
	label := p.VotingMethod
	if m, ok := voting.Get(p.VotingMethod); ok {
		label = m.Label()
	}
	v := adminPollView{
		Code:              p.Code,
		Title:             p.Title,
		Status:            string(p.Status),
		VotingMethod:      p.VotingMethod,
		VotingMethodLabel: label,
		DeadlineMode:      string(p.DeadlineMode),
		ParticipantCount:  sum.Counts.Participants,
		NominationCount:   sum.Counts.Nominations,
		VoterCount:        sum.Counts.Voters,
		CreatedAt:         p.CreatedAt.UTC(),
		ClosedAt:          p.ClosedAt,
	}
	// Resolve the winning title for finished polls so the list reads as history.
	if p.Status == poll.StatusClosed {
		if res, noms, err := s.svc.Results(ctx, p); err == nil && len(res.WinnerIDs) > 0 {
			for _, n := range noms {
				if n.ID == res.WinnerIDs[0] {
					v.WinnerTitle = n.Snapshot.Title
					break
				}
			}
		}
	}
	return v
}

func (s *Server) handleAdminPolls(w http.ResponseWriter, r *http.Request) {
	summaries, err := s.svc.ListHistory(r.Context())
	if err != nil {
		s.writeErr(w, err)
		return
	}
	out := make([]adminPollView, 0, len(summaries))
	for _, sum := range summaries {
		out = append(out, s.buildAdminPollView(r.Context(), sum))
	}
	s.writeJSON(w, http.StatusOK, out)
}

// handleAdminPoll returns the full state of one poll (nominations and, when
// available, results) for the dashboard's drill-down. It reuses the standard
// poll view with no participant identity.
func (s *Server) handleAdminPoll(w http.ResponseWriter, r *http.Request) {
	p, err := s.pollFromCode(r)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	view, err := s.buildPollView(r.Context(), p, nil)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, view)
}

// adminBallotView is one participant's ballot as seen by an admin. Selections
// is empty for someone who hasn't voted (HasVoted false).
type adminBallotView struct {
	ParticipantID string               `json:"participant_id"`
	DisplayName   string               `json:"display_name"`
	IsHost        bool                 `json:"is_host"`
	HasVoted      bool                 `json:"has_voted"`
	Selections    []adminSelectionView `json:"selections"`
}

// adminSelectionView is one allocation on a ballot. Value's meaning follows the
// poll's voting method: votes (approval), rank with 1 = first (ranked), or score.
type adminSelectionView struct {
	NominationID string `json:"nomination_id"`
	Title        string `json:"title"`
	Value        int    `json:"value"`
}

// handleAdminBallots returns every participant's individual ballot for one
// poll, in join order. Ballots are otherwise private to the voter; only admins
// see them here.
func (s *Server) handleAdminBallots(w http.ResponseWriter, r *http.Request) {
	p, err := s.pollFromCode(r)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	ctx := r.Context()
	participants, err := s.repo.ListParticipants(ctx, p.ID)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	noms, err := s.repo.ListNominations(ctx, p.ID)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	votes, err := s.repo.ListVotes(ctx, p.ID)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	titles := make(map[string]string, len(noms))
	for _, n := range noms {
		titles[n.ID] = n.Snapshot.Title
	}
	byVoter := map[string][]adminSelectionView{}
	for _, v := range votes {
		byVoter[v.ParticipantID] = append(byVoter[v.ParticipantID], adminSelectionView{
			NominationID: v.NominationID,
			Title:        titles[v.NominationID],
			Value:        v.Value,
		})
	}
	// Ranks read best-first ascending; votes and scores descending.
	ascending := p.VotingMethod == "ranked"
	out := make([]adminBallotView, 0, len(participants))
	for _, pt := range participants {
		sel := byVoter[pt.ID]
		sort.SliceStable(sel, func(i, j int) bool {
			if sel[i].Value != sel[j].Value {
				return (sel[i].Value < sel[j].Value) == ascending
			}
			return sel[i].Title < sel[j].Title
		})
		if sel == nil {
			sel = []adminSelectionView{} // serialize empty as [] not null
		}
		out = append(out, adminBallotView{
			ParticipantID: pt.ID,
			DisplayName:   pt.DisplayName,
			IsHost:        pt.IsHost(),
			HasVoted:      len(sel) > 0,
			Selections:    sel,
		})
	}
	s.writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAdminDeletePoll(w http.ResponseWriter, r *http.Request) {
	p, err := s.pollFromCode(r)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	if err := s.svc.DeletePoll(r.Context(), p.ID); err != nil {
		s.writeErr(w, err)
		return
	}
	// Nudge any connected clients; their refetch will 404 on the gone poll.
	s.broadcast(p.ID, "status")
	s.writeJSON(w, http.StatusNoContent, nil)
}

func (s *Server) handleAdminAdvance(w http.ResponseWriter, r *http.Request) {
	p, err := s.pollFromCode(r)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	if _, err := s.svc.ForceAdvance(r.Context(), p); err != nil {
		s.writeErr(w, err)
		return
	}
	s.broadcast(p.ID, "status")
	if p.Status == poll.StatusClosed {
		s.autoRequestWinners(r.Context(), p)
	}
	summaries, err := s.svc.ListHistory(r.Context())
	if err != nil {
		s.writeErr(w, err)
		return
	}
	for _, sum := range summaries {
		if sum.Poll.ID == p.ID {
			s.writeJSON(w, http.StatusOK, s.buildAdminPollView(r.Context(), sum))
			return
		}
	}
	s.writeJSON(w, http.StatusOK, errResp{}) // shouldn't happen
}

// handleAdminDuplicate copies a poll in its current state under a new code and
// returns the copy's history row.
func (s *Server) handleAdminDuplicate(w http.ResponseWriter, r *http.Request) {
	p, err := s.pollFromCode(r)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	cp, err := s.svc.DuplicatePoll(r.Context(), p)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	summaries, err := s.svc.ListHistory(r.Context())
	if err != nil {
		s.writeErr(w, err)
		return
	}
	for _, sum := range summaries {
		if sum.Poll.ID == cp.ID {
			s.writeJSON(w, http.StatusCreated, s.buildAdminPollView(r.Context(), sum))
			return
		}
	}
	s.writeJSON(w, http.StatusCreated, errResp{}) // shouldn't happen
}

// RunRetentionSweeper periodically purges closed polls older than the configured
// retention window. It runs once immediately, then every interval until ctx is
// cancelled. Started from main only when retention is enabled.
func (s *Server) RunRetentionSweeper(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		n, err := s.svc.PurgeExpired(ctx, time.Now(), s.cfg.PollRetentionDays)
		if err != nil {
			log.Printf("retention sweep: %v", err)
		} else if n > 0 {
			log.Printf("retention: purged %d poll(s) older than %d days", n, s.cfg.PollRetentionDays)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
