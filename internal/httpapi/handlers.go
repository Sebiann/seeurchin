package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/enderu/seeurchin/internal/jellyfin"
	"github.com/enderu/seeurchin/internal/poll"
	"github.com/enderu/seeurchin/internal/seerr"
	"github.com/enderu/seeurchin/internal/voting"
)

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleFeatures advertises optional capabilities so the create page can show
// the right controls before a poll exists, and so the home page knows whether
// to surface the admin link.
func (s *Server) handleFeatures(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]any{
		"seerr":      s.seerrEnabled(),
		"admin":      s.cfg.AdminEnabled(),
		"user_login": s.cfg.EnableUserLogin,
	})
}

type methodView struct {
	Key           string          `json:"key"`
	Label         string          `json:"label"`
	DefaultConfig json.RawMessage `json:"default_config"`
}

func (s *Server) handleMethods(w http.ResponseWriter, _ *http.Request) {
	var out []methodView
	for _, m := range voting.All() {
		out = append(out, methodView{Key: m.Key(), Label: m.Label(), DefaultConfig: m.DefaultConfig()})
	}
	s.writeJSON(w, http.StatusOK, out)
}

type createPollReq struct {
	Title             string               `json:"title"`
	HostName          string               `json:"host_name"`
	LibraryScope      string               `json:"library_scope"`
	SubmissionRules   poll.SubmissionRules `json:"submission_rules"`
	VotingMethod      string               `json:"voting_method"`
	VotingConfig      json.RawMessage      `json:"voting_config"`
	AllowGuests       bool                 `json:"allow_guests"`
	ResultsLive       bool                 `json:"results_live"`
	RevealNominators  bool                 `json:"reveal_nominators"`
	RevealScope       string               `json:"reveal_scope"`
	Genres            []string             `json:"genres"`
	AllowWriteins     bool                 `json:"allow_writeins"`
	AutoRequestWinner bool                 `json:"auto_request_winner"`
	Passcode          string               `json:"passcode"`
	DeadlineMode      string               `json:"deadline_mode"`
	Round1DurationSec int                  `json:"round1_duration_sec"`
	Round2DurationSec int                  `json:"round2_duration_sec"`
	Round1ClosesAt    *time.Time           `json:"round1_closes_at"`
	Round2ClosesAt    *time.Time           `json:"round2_closes_at"`
}

// handleGenres lists the library's genres for a scope ("movie" | "series" |
// "both"), so the create page can offer a genre restriction before a poll
// exists.
func (s *Server) handleGenres(w http.ResponseWriter, r *http.Request) {
	types := scopeTypes(poll.LibraryScope(strings.ToLower(r.URL.Query().Get("scope"))))
	genres, err := s.jf.ListGenres(r.Context(), types)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	if genres == nil {
		genres = []string{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"genres": genres})
}

func (s *Server) handleCreatePoll(w http.ResponseWriter, r *http.Request) {
	var req createPollReq
	if err := decodeJSON(r, &req); err != nil {
		s.writeErr(w, err)
		return
	}
	// When signed in with Jellyfin, attribute the poll to that account and
	// default the host name to the Jellyfin display name if none was supplied.
	hostName := req.HostName
	var hostJellyfinUserID string
	if id, ok := s.currentUser(r); ok {
		hostJellyfinUserID = id.UserID
		if strings.TrimSpace(hostName) == "" {
			hostName = id.Name
		}
	}
	p, host, err := s.svc.CreatePoll(r.Context(), poll.CreatePollInput{
		Title:              req.Title,
		HostName:           hostName,
		HostJellyfinUserID: hostJellyfinUserID,
		PasscodeHash:       s.passcodeHash(req.Passcode),
		LibraryScope:       poll.LibraryScope(req.LibraryScope),
		SubmissionRules:    req.SubmissionRules,
		VotingMethod:       req.VotingMethod,
		VotingConfig:       req.VotingConfig,
		AllowGuests:        req.AllowGuests,
		ResultsLive:        req.ResultsLive,
		RevealNominators:   req.RevealNominators,
		RevealScope:        req.RevealScope,
		Genres:             req.Genres,
		AllowWriteins:      req.AllowWriteins,
		AutoRequestWinner:  req.AutoRequestWinner,
		DeadlineMode:       poll.DeadlineMode(req.DeadlineMode),
		Round1DurationSec:  req.Round1DurationSec,
		Round2DurationSec:  req.Round2DurationSec,
		Round1ClosesAt:     req.Round1ClosesAt,
		Round2ClosesAt:     req.Round2ClosesAt,
	})
	if err != nil {
		s.writeErr(w, err)
		return
	}
	s.setToken(w, r, p.ID, host.SessionToken)
	view, err := s.buildPollView(r.Context(), p, host)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	s.writeJSON(w, http.StatusCreated, view)
}

func (s *Server) handleGetPoll(w http.ResponseWriter, r *http.Request) {
	p, err := s.pollFromCode(r)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	me := s.currentParticipant(r, p)
	if me == nil {
		me = s.reclaimLinked(w, r, p)
	}
	s.respondView(w, r, p, me)
}

// signedInUser returns the Jellyfin identity when login is enabled and this
// browser is signed in.
func (s *Server) signedInUser(r *http.Request) (*userIdentity, bool) {
	if !s.cfg.EnableUserLogin {
		return nil, false
	}
	return s.currentUser(r)
}

// reclaimLinked puts a signed-in user back on the poll entry linked to their
// Jellyfin account (e.g. on a new device), or returns nil if there is none.
func (s *Server) reclaimLinked(w http.ResponseWriter, r *http.Request, p *poll.Poll) *poll.Participant {
	id, ok := s.signedInUser(r)
	if !ok {
		return nil
	}
	me, err := s.repo.GetParticipantByJellyfinUser(r.Context(), p.ID, id.UserID)
	if err != nil {
		return nil
	}
	s.setToken(w, r, p.ID, me.SessionToken)
	return me
}

type joinReq struct {
	DisplayName string `json:"display_name"`
	Passcode    string `json:"passcode"`
}

func (s *Server) handleJoin(w http.ResponseWriter, r *http.Request) {
	p, err := s.pollFromCode(r)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	if me := s.currentParticipant(r, p); me != nil {
		s.respondView(w, r, p, me) // already joined; idempotent
		return
	}
	if me := s.reclaimLinked(w, r, p); me != nil {
		s.respondView(w, r, p, me)
		return
	}
	var req joinReq
	if err := decodeJSON(r, &req); err != nil {
		s.writeErr(w, err)
		return
	}
	// Optional per-poll guest passcode: a soft gate the host sets at creation to
	// keep random link-finders out. It never touches the NAS.
	if p.PasscodeHash != "" && !s.passcodeMatches(p.PasscodeHash, req.Passcode) {
		s.writeJSON(w, http.StatusForbidden, errResp{"incorrect passcode"})
		return
	}
	user, signedIn := s.signedInUser(r)
	if signedIn && strings.TrimSpace(req.DisplayName) == "" {
		req.DisplayName = user.Name
	}
	me, err := s.svc.JoinAsGuest(r.Context(), p, req.DisplayName)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	if signedIn {
		if me, err = s.svc.LinkJellyfin(r.Context(), p, me, user.UserID, false); err != nil {
			s.writeErr(w, err)
			return
		}
	}
	s.setToken(w, r, p.ID, me.SessionToken)
	s.respondView(w, r, p, me)
}

type libraryItem struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Year      int    `json:"year"`
	Type      string `json:"type"`
	Runtime   int    `json:"runtime_minutes"`
	ImageTag  string `json:"image_tag"`
	Nominated bool   `json:"nominated"`
}

func (s *Server) handleLibrary(w http.ResponseWriter, r *http.Request) {
	p, err := s.pollFromCode(r)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	if _, ok := s.requireParticipant(w, r, p); !ok {
		return
	}

	types := scopeTypes(p.LibraryScope)
	switch strings.ToLower(r.URL.Query().Get("type")) {
	case "movie":
		types = []string{"Movie"}
	case "series":
		types = []string{"Series"}
	}
	// Load the whole library by default — Jellyfin returns all matching items
	// when no Limit is sent, and the frontend lazy-loads posters as you scroll.
	// An explicit ?limit= still caps it if ever needed.
	limit := 0
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		limit = v
	}
	start, _ := strconv.Atoi(r.URL.Query().Get("start"))

	// An optional ?genre= narrows the browse within the poll's own genre
	// restriction (if any). A poll that restricts to p.Genres still only lets
	// you pick one of those; an unrestricted poll can filter to any genre.
	genres := p.Genres
	if g := strings.TrimSpace(r.URL.Query().Get("genre")); g != "" {
		if len(p.Genres) == 0 || containsFold(p.Genres, g) {
			genres = []string{g}
		}
	}

	items, total, err := s.jf.Search(r.Context(), jellyfin.SearchParams{
		Query:      strings.TrimSpace(r.URL.Query().Get("q")),
		Types:      types,
		Genres:     genres,
		Limit:      limit,
		StartIndex: start,
	})
	if err != nil {
		s.writeErr(w, err)
		return
	}

	nominated := map[string]bool{}
	if noms, err := s.repo.ListNominations(r.Context(), p.ID); err == nil {
		for _, n := range noms {
			nominated[n.JellyfinItemID] = true
		}
	}

	// Collapse duplicate library entries for the same title — e.g. the same
	// media surfaced under two Jellyfin libraries shows up as distinct items
	// with different IDs. Key on type+title+year; prefer the copy that has a
	// poster, and keep the nominated flag sticky across the merge.
	out := make([]libraryItem, 0, len(items))
	seen := make(map[string]int, len(items))
	for _, it := range items {
		li := libraryItem{
			ID:        it.ID,
			Title:     it.Name,
			Year:      it.ProductionYear,
			Type:      it.Type,
			Runtime:   it.RuntimeMinutes(),
			ImageTag:  it.PrimaryImageTag(),
			Nominated: nominated[it.ID],
		}
		key := it.Type + "\x00" + strings.ToLower(strings.TrimSpace(it.Name)) + "\x00" + strconv.Itoa(it.ProductionYear)
		if idx, ok := seen[key]; ok {
			if li.Nominated {
				out[idx].Nominated = true
			}
			if out[idx].ImageTag == "" && li.ImageTag != "" {
				wasNominated := out[idx].Nominated
				out[idx] = li
				out[idx].Nominated = wasNominated
			}
			continue
		}
		seen[key] = len(out)
		out = append(out, li)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"items": out, "total": total})
}

// handleSearchExternal backs the "request something new" tab: a live,
// debounced search of Seerr (TMDB) for titles that may not be in the library.
func (s *Server) handleSearchExternal(w http.ResponseWriter, r *http.Request) {
	p, err := s.pollFromCode(r)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	if _, ok := s.requireParticipant(w, r, p); !ok {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if !s.seerrEnabled() || !p.AllowWriteins || q == "" {
		s.writeJSON(w, http.StatusOK, map[string]any{"results": []any{}})
		return
	}
	results, err := s.seerr.Search(r.Context(), q)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	// Keep the request tab consistent with the library tab: drop hits outside the
	// poll's scope and, when the poll restricts genres, outside those genres.
	// Jellyfin genre names map only approximately to TMDB ids (see
	// seerr.GenreIDSet), so unrecognized names yield an empty set and impose no
	// restriction rather than hiding everything.
	allowedGenres := seerr.GenreIDSet(p.Genres)
	out := results[:0] // reuse backing array
	for _, res := range results {
		if !scopeAllowsMedia(p.LibraryScope, res.MediaType) {
			continue
		}
		if !seerr.MatchesGenres(res.GenreIDs, allowedGenres) {
			continue
		}
		out = append(out, res)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"results": out})
}

// scopeAllowsMedia maps a Seerr media type ("movie"|"tv") to the poll's scope.
func scopeAllowsMedia(scope poll.LibraryScope, mediaType string) bool {
	switch scope {
	case poll.ScopeMovies:
		return mediaType == "movie"
	case poll.ScopeSeries:
		return mediaType == "tv"
	default:
		return mediaType == "movie" || mediaType == "tv"
	}
}

type nominateReq struct {
	ItemID    string `json:"item_id"`
	TMDBID    int    `json:"tmdb_id"`
	MediaType string `json:"media_type"`
}

func (s *Server) handleNominate(w http.ResponseWriter, r *http.Request) {
	p, err := s.pollFromCode(r)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	me, ok := s.requireParticipant(w, r, p)
	if !ok {
		return
	}
	var req nominateReq
	if err := decodeJSON(r, &req); err != nil {
		s.writeErr(w, err)
		return
	}
	// A tmdb_id means a write-in (a title not in the library): resolve it via
	// Seerr for an authoritative snapshot, then record it.
	if req.TMDBID > 0 {
		if !s.seerrEnabled() {
			s.writeJSON(w, http.StatusBadRequest, errResp{"requesting new titles isn't available"})
			return
		}
		in, err := s.resolveWriteIn(r.Context(), req.TMDBID, req.MediaType)
		if err != nil {
			s.writeErr(w, err)
			return
		}
		if in == nil {
			s.writeJSON(w, http.StatusBadRequest, errResp{"that title was not found"})
			return
		}
		// Don't let a write-in re-request something already in the library — e.g.
		// a title hidden from the library tab by the poll's genre filter. Best
		// effort: a Jellyfin hiccup shouldn't block a legitimate new nomination.
		itemType := "Movie"
		if in.MediaType == "tv" {
			itemType = "Series"
		}
		if existing, ferr := s.jf.FindByTMDB(r.Context(), in.TMDBID, in.Title, itemType, in.Year); ferr != nil {
			log.Printf("library dup-check %q: %v", in.Title, ferr)
		} else if existing != nil {
			s.writeJSON(w, http.StatusConflict, errResp{fmt.Sprintf("%q is already in your library", existing.Name)})
			return
		}
		if _, err := s.svc.SubmitWriteIn(r.Context(), p, me, *in); err != nil {
			s.writeErr(w, err)
			return
		}
	} else if _, err := s.svc.SubmitNomination(r.Context(), p, me, req.ItemID); err != nil {
		s.writeErr(w, err)
		return
	}
	s.broadcast(p.ID, "nominations")
	s.respondView(w, r, p, me)
}

// resolveWriteIn looks up a Seerr title by TMDB id and media type and maps it to
// a write-in input. Returns (nil, nil) when the title isn't found.
func (s *Server) resolveWriteIn(ctx context.Context, tmdbID int, mediaType string) (*poll.WriteInInput, error) {
	var (
		res *seerr.Result
		err error
	)
	switch mediaType {
	case "movie":
		res, err = s.seerr.GetMovie(ctx, tmdbID)
	case "tv":
		res, err = s.seerr.GetTV(ctx, tmdbID)
	default:
		return nil, &poll.Error{Code: http.StatusBadRequest, Msg: "invalid media type"}
	}
	if err != nil || res == nil {
		return nil, err
	}
	return &poll.WriteInInput{
		TMDBID:    res.TMDBID,
		MediaType: res.MediaType,
		Title:     res.Title,
		Year:      res.Year,
		PosterURL: res.PosterURL,
		Overview:  res.Overview,
	}, nil
}

func (s *Server) handleWithdraw(w http.ResponseWriter, r *http.Request) {
	p, err := s.pollFromCode(r)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	me, ok := s.requireParticipant(w, r, p)
	if !ok {
		return
	}
	if err := s.svc.WithdrawNomination(r.Context(), p, me, chi.URLParam(r, "id")); err != nil {
		s.writeErr(w, err)
		return
	}
	s.broadcast(p.ID, "nominations")
	s.respondView(w, r, p, me)
}

func (s *Server) handleAdvance(w http.ResponseWriter, r *http.Request) {
	p, err := s.pollFromCode(r)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	me, ok := s.requireParticipant(w, r, p)
	if !ok {
		return
	}
	// Optional body: participant IDs whose votes the host chose to ignore.
	var req struct {
		IgnoreParticipantIDs []string `json:"ignore_participant_ids"`
	}
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &req); err != nil {
			s.writeErr(w, err)
			return
		}
	}
	if _, err := s.svc.Advance(r.Context(), p, me, req.IgnoreParticipantIDs...); err != nil {
		s.writeErr(w, err)
		return
	}
	s.broadcast(p.ID, "status")
	// When the poll just closed, auto-request any winning write-in via Seerr.
	if p.Status == poll.StatusClosed {
		s.autoRequestWinners(r.Context(), p)
	}
	s.respondView(w, r, p, me)
}

func (s *Server) handleMergeDuplicates(w http.ResponseWriter, r *http.Request) {
	p, err := s.pollFromCode(r)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	me, ok := s.requireParticipant(w, r, p)
	if !ok {
		return
	}
	if err := s.svc.MergeDuplicateNames(r.Context(), p, me); err != nil {
		s.writeErr(w, err)
		return
	}
	s.broadcast(p.ID, "votes")
	s.respondView(w, r, p, me)
}

// handleLink ties the caller's entry to their signed-in Jellyfin account. If
// they're already linked elsewhere in the poll, this browser moves to that entry.
func (s *Server) handleLink(w http.ResponseWriter, r *http.Request) {
	p, err := s.pollFromCode(r)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	me, ok := s.requireParticipant(w, r, p)
	if !ok {
		return
	}
	user, ok := s.signedInUser(r)
	if !ok {
		s.writeJSON(w, http.StatusUnauthorized, errResp{"sign in with Jellyfin first"})
		return
	}
	if me, err = s.svc.LinkJellyfin(r.Context(), p, me, user.UserID, false); err != nil {
		s.writeErr(w, err)
		return
	}
	s.setToken(w, r, p.ID, me.SessionToken)
	s.broadcast(p.ID, "votes")
	s.respondView(w, r, p, me)
}

// requireSignedInHost gates the host's manual-linking endpoints: the caller
// must be this poll's host and signed in to Jellyfin (which is what entitles
// them to see the server's account list).
func (s *Server) requireSignedInHost(w http.ResponseWriter, r *http.Request, p *poll.Poll) (*poll.Participant, bool) {
	me, ok := s.requireParticipant(w, r, p)
	if !ok {
		return nil, false
	}
	if !me.IsHost() {
		s.writeJSON(w, http.StatusForbidden, errResp{"only the host can link people"})
		return nil, false
	}
	if _, ok := s.signedInUser(r); !ok {
		s.writeJSON(w, http.StatusUnauthorized, errResp{"sign in with Jellyfin first"})
		return nil, false
	}
	return me, true
}

func (s *Server) handleJellyfinUsers(w http.ResponseWriter, r *http.Request) {
	p, err := s.pollFromCode(r)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	if _, ok := s.requireSignedInHost(w, r, p); !ok {
		return
	}
	users, err := s.jf.ListUsers(r.Context())
	if err != nil {
		s.writeErr(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, users)
}

type hostLinkReq struct {
	JellyfinUserID string `json:"jellyfin_user_id"`
}

func (s *Server) handleHostLink(w http.ResponseWriter, r *http.Request) {
	p, err := s.pollFromCode(r)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	me, ok := s.requireSignedInHost(w, r, p)
	if !ok {
		return
	}
	var req hostLinkReq
	if err := decodeJSON(r, &req); err != nil {
		s.writeErr(w, err)
		return
	}
	// Only real, enabled Jellyfin accounts can be linked.
	users, err := s.jf.ListUsers(r.Context())
	if err != nil {
		s.writeErr(w, err)
		return
	}
	known := false
	for _, u := range users {
		known = known || u.ID == req.JellyfinUserID
	}
	if !known {
		s.writeJSON(w, http.StatusBadRequest, errResp{"unknown Jellyfin account"})
		return
	}
	if err := s.svc.HostLinkJellyfin(r.Context(), p, me, chi.URLParam(r, "id"), req.JellyfinUserID); err != nil {
		s.writeErr(w, err)
		return
	}
	s.broadcast(p.ID, "votes")
	s.respondView(w, r, p, me)
}

// handleBreakTie lets the host resolve a tied result by drawing one of the
// co-winners at random; everyone gets the frozen pick via SSE.
func (s *Server) handleBreakTie(w http.ResponseWriter, r *http.Request) {
	p, err := s.pollFromCode(r)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	me, ok := s.requireParticipant(w, r, p)
	if !ok {
		return
	}
	p, err = s.svc.BreakTie(r.Context(), p, me)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	s.broadcast(p.ID, "results")
	s.respondView(w, r, p, me)
}

// handleRequestWinner lets the host request a winning write-in manually (for
// polls with auto-request turned off).
func (s *Server) handleRequestWinner(w http.ResponseWriter, r *http.Request) {
	p, err := s.pollFromCode(r)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	me, ok := s.requireParticipant(w, r, p)
	if !ok {
		return
	}
	if !me.IsHost() {
		s.writeJSON(w, http.StatusForbidden, errResp{"only the host can request the winner"})
		return
	}
	if !s.seerrEnabled() {
		s.writeJSON(w, http.StatusBadRequest, errResp{"Seerr is not configured"})
		return
	}
	noms, err := s.repo.ListNominations(r.Context(), p.ID)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	id := chi.URLParam(r, "id")
	var target *poll.Nomination
	for i := range noms {
		if noms[i].ID == id {
			target = &noms[i]
			break
		}
	}
	if target == nil {
		s.writeJSON(w, http.StatusNotFound, errResp{"nomination not found"})
		return
	}
	if target.Snapshot.Source != poll.SourceSeerr {
		s.writeJSON(w, http.StatusBadRequest, errResp{"that title is already in the library"})
		return
	}
	if _, err := s.requestWriteIn(r.Context(), p, target); err != nil {
		s.writeErr(w, err)
		return
	}
	s.broadcast(p.ID, "results")
	s.respondView(w, r, p, me)
}

// requestWriteIn submits a Seerr request for a write-in nomination, recording the
// outcome so it fires at most once. Returns the resulting status.
func (s *Server) requestWriteIn(ctx context.Context, p *poll.Poll, n *poll.Nomination) (string, error) {
	existing, err := s.repo.GetSeerrRequest(ctx, p.ID, n.ID)
	if err != nil {
		return "", err
	}
	if existing != nil {
		return existing.Status, nil // already requested
	}
	in := seerr.RequestInput{
		MediaType: n.Snapshot.MediaType,
		TMDBID:    n.Snapshot.TMDBID,
		ServerID:  s.cfg.Seerr.ServerID,
		UserID:    s.cfg.Seerr.RequestUserID,
	}
	if n.Snapshot.MediaType == "tv" {
		in.ProfileID, in.RootFolder = s.cfg.Seerr.TVProfileID, s.cfg.Seerr.TVRootFolder
	} else {
		in.ProfileID, in.RootFolder = s.cfg.Seerr.MovieProfileID, s.cfg.Seerr.MovieRootFolder
	}
	res, err := s.seerr.CreateRequest(ctx, in)
	if err != nil {
		return "", err
	}
	rec := &poll.SeerrRequest{PollID: p.ID, NominationID: n.ID, TMDBID: n.Snapshot.TMDBID, MediaType: n.Snapshot.MediaType, Status: res.Status}
	if err := s.repo.RecordSeerrRequest(ctx, rec); err != nil {
		return res.Status, err
	}
	return res.Status, nil
}

// autoRequestWinners requests any winning write-in when the poll closes, best
// effort — a Seerr hiccup never blocks closing the poll.
func (s *Server) autoRequestWinners(ctx context.Context, p *poll.Poll) {
	if !s.seerrEnabled() || !p.AutoRequestWinner {
		return
	}
	res, noms, err := s.svc.Results(ctx, p)
	if err != nil {
		log.Printf("auto-request: results: %v", err)
		return
	}
	byID := make(map[string]*poll.Nomination, len(noms))
	for i := range noms {
		byID[noms[i].ID] = &noms[i]
	}
	for _, id := range res.WinnerIDs {
		n := byID[id]
		if n == nil || n.Snapshot.Source != poll.SourceSeerr {
			continue
		}
		if _, err := s.requestWriteIn(ctx, p, n); err != nil {
			log.Printf("auto-request %q: %v", n.Snapshot.Title, err)
		}
	}
}

type voteReq struct {
	Selections map[string]int `json:"selections"`
}

func (s *Server) handleVote(w http.ResponseWriter, r *http.Request) {
	p, err := s.pollFromCode(r)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	me, ok := s.requireParticipant(w, r, p)
	if !ok {
		return
	}
	var req voteReq
	if err := decodeJSON(r, &req); err != nil {
		s.writeErr(w, err)
		return
	}
	if err := s.svc.CastVotes(r.Context(), p, me, req.Selections); err != nil {
		s.writeErr(w, err)
		return
	}
	s.broadcast(p.ID, "votes")
	s.respondView(w, r, p, me)
}

func (s *Server) handleResults(w http.ResponseWriter, r *http.Request) {
	p, err := s.pollFromCode(r)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	if !resultsVisible(p) {
		s.writeJSON(w, http.StatusForbidden, errResp{"results are not available yet"})
		return
	}
	noms, err := s.repo.ListNominations(r.Context(), p.ID)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	rv, err := s.computeResults(r.Context(), p, noms)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, rv)
}

func (s *Server) handleImage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	resp, err := s.jf.FetchImage(r.Context(), id, "Primary", r.URL.Query())
	if err != nil {
		s.writeJSON(w, http.StatusBadGateway, errResp{"image unavailable"})
		return
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// --- shared helpers ---

func (s *Server) requireParticipant(w http.ResponseWriter, r *http.Request, p *poll.Poll) (*poll.Participant, bool) {
	me := s.currentParticipant(r, p)
	if me == nil {
		s.writeJSON(w, http.StatusUnauthorized, errResp{"join the poll first"})
		return nil, false
	}
	return me, true
}

func (s *Server) respondView(w http.ResponseWriter, r *http.Request, p *poll.Poll, me *poll.Participant) {
	view, err := s.buildPollView(r.Context(), p, me)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, view)
}

func scopeTypes(scope poll.LibraryScope) []string {
	switch scope {
	case poll.ScopeMovies:
		return []string{"Movie"}
	case poll.ScopeSeries:
		return []string{"Series"}
	default:
		return []string{"Movie", "Series"}
	}
}

// containsFold reports whether s appears in list, case-insensitively.
func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}
