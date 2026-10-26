package poll

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/enderu/seeurchin/internal/codes"
	"github.com/enderu/seeurchin/internal/voting"
)

// ItemResolver resolves a Jellyfin item ID into the details seeurchin snapshots
// at nomination time. It decouples the domain from the Jellyfin client.
type ItemResolver interface {
	GetItem(ctx context.Context, id string) (*ResolvedItem, error)
}

// ResolvedItem is the minimal library item the service needs.
type ResolvedItem struct {
	ID       string
	Title    string
	Year     int
	Type     string // "Movie" | "Series"
	Runtime  int
	Overview string
	ImageTag string
	Genres   []string
}

// Error is a service error carrying an HTTP-friendly status code.
type Error struct {
	Code int
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

func errBad(format string, a ...any) *Error { return &Error{Code: 400, Msg: fmt.Sprintf(format, a...)} }
func errForbid(format string, a ...any) *Error {
	return &Error{Code: 403, Msg: fmt.Sprintf(format, a...)}
}
func errConflict(format string, a ...any) *Error {
	return &Error{Code: 409, Msg: fmt.Sprintf(format, a...)}
}

// Service holds the poll business logic.
type Service struct {
	repo    Repository
	items   ItemResolver
	codeLen int
}

// NewService constructs a Service. codeLen <= 0 uses the default code length.
func NewService(repo Repository, items ItemResolver, codeLen int) *Service {
	if codeLen <= 0 {
		codeLen = codes.DefaultLength
	}
	return &Service{repo: repo, items: items, codeLen: codeLen}
}

// CreatePollInput describes a new poll.
type CreatePollInput struct {
	Title              string
	HostName           string
	HostJellyfinUserID string // Jellyfin user id of the creator (empty for guests)
	PasscodeHash       string // optional per-poll guest passcode hash ("" = no gate)
	LibraryScope       LibraryScope
	SubmissionRules    SubmissionRules
	VotingMethod       string
	VotingConfig       json.RawMessage
	AllowGuests        bool
	ResultsLive        bool
	RevealNominators   bool
	RevealScope        string
	Genres             []string // restrict nominations to these genres (empty = any)
	AllowWriteins      bool
	AutoRequestWinner  bool
	// Deadline config. DeadlineMode selects the style; for "quick" the durations
	// arm each round (host starts the clock); for "scheduled" the ClosesAt times
	// are absolute. Leave zero for "none" (host advances manually).
	DeadlineMode      DeadlineMode
	Round1DurationSec int
	Round2DurationSec int
	Round1ClosesAt    *time.Time
	Round2ClosesAt    *time.Time
}

// Timer bounds. A per-round timer is at least minTimerSec; "quick" durations
// are capped at maxTimerSec and "scheduled" close times must fall within
// maxScheduleAhead of now.
const (
	minTimerSec      = 5
	maxTimerSec      = 30 * 24 * 3600 // 30 days
	maxScheduleAhead = 365 * 24 * time.Hour
)

// CreatePoll validates input, opens a poll directly into round 1, and creates
// the host participant. The returned host carries its session token.
func (s *Service) CreatePoll(ctx context.Context, in CreatePollInput) (*Poll, *Participant, error) {
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return nil, nil, errBad("title is required")
	}
	hostName := strings.TrimSpace(in.HostName)
	if hostName == "" {
		return nil, nil, errBad("your name is required")
	}
	if in.LibraryScope == "" {
		in.LibraryScope = ScopeBoth
	}
	switch in.LibraryScope {
	case ScopeMovies, ScopeSeries, ScopeBoth:
	default:
		return nil, nil, errBad("invalid library scope %q", in.LibraryScope)
	}
	if err := validateRules(in.SubmissionRules); err != nil {
		return nil, nil, err
	}
	if in.RevealScope == "" {
		in.RevealScope = RevealWinner
	}
	if in.RevealScope != RevealWinner && in.RevealScope != RevealAll {
		return nil, nil, errBad("invalid reveal scope %q", in.RevealScope)
	}
	genres := cleanGenres(in.Genres)

	method, ok := voting.Get(in.VotingMethod)
	if !ok {
		return nil, nil, errBad("unknown voting method %q", in.VotingMethod)
	}
	cfg := in.VotingConfig
	if len(cfg) == 0 {
		cfg = method.DefaultConfig()
	}
	if err := method.ValidateConfig(cfg); err != nil {
		return nil, nil, errBad("voting config: %v", err)
	}

	code, err := codes.GenerateUnique(s.codeLen, 10, func(c string) (bool, error) {
		return s.repo.CodeExists(ctx, c)
	})
	if err != nil {
		return nil, nil, err
	}

	p := &Poll{
		ID:                NewID(),
		Code:              code,
		Title:             title,
		LibraryScope:      in.LibraryScope,
		Status:            StatusRound1,
		SubmissionRules:   in.SubmissionRules,
		VotingMethod:      in.VotingMethod,
		VotingConfig:      cfg,
		AllowGuests:       in.AllowGuests,
		ResultsLive:       in.ResultsLive,
		RevealNominators:  in.RevealNominators,
		RevealScope:       in.RevealScope,
		Genres:            genres,
		AllowWriteins:     in.AllowWriteins,
		AutoRequestWinner: in.AutoRequestWinner,
		PasscodeHash:      in.PasscodeHash,
	}
	if err := applyDeadlineConfig(p, in, time.Now()); err != nil {
		return nil, nil, err
	}

	host := &Participant{
		ID:             NewID(),
		DisplayName:    hostName,
		SessionToken:   NewID(),
		JellyfinUserID: in.HostJellyfinUserID,
		Role:           RoleHost,
	}
	if err := s.repo.CreatePoll(ctx, p, host); err != nil {
		return nil, nil, err
	}
	return p, host, nil
}

// applyDeadlineConfig validates the deadline input and sets the poll's timer
// fields. "quick" arms per-round durations (ClosesAt stays nil until the host
// starts the clock); "scheduled" sets absolute per-round close times that run
// immediately. The poll's method matters: an AutoDecider (e.g. random) has no
// round 2, so round-2 settings are ignored.
func applyDeadlineConfig(p *Poll, in CreatePollInput, now time.Time) error {
	switch in.DeadlineMode {
	case DeadlineNone:
		return nil

	case DeadlineQuick:
		_, autoDecide := voting.Decider(p.VotingMethod)
		r1, r2 := in.Round1DurationSec, in.Round2DurationSec
		if autoDecide {
			r2 = 0 // no voting round to time
		}
		if r1 == 0 && r2 == 0 {
			return errBad("set a timer length for at least one round")
		}
		if err := validateDuration("nomination", r1); err != nil {
			return err
		}
		if err := validateDuration("voting", r2); err != nil {
			return err
		}
		p.DeadlineMode = DeadlineQuick
		p.Round1DurationSec = r1
		p.Round2DurationSec = r2
		// ClosesAt left nil: round 1 is armed until the host taps Start.
		return nil

	case DeadlineScheduled:
		_, autoDecide := voting.Decider(p.VotingMethod)
		r1, r2 := in.Round1ClosesAt, in.Round2ClosesAt
		if autoDecide {
			r2 = nil
		}
		if r1 == nil && r2 == nil {
			return errBad("set a close time for at least one round")
		}
		if err := validateCloseTime("nomination", r1, now); err != nil {
			return err
		}
		if err := validateCloseTime("voting", r2, now); err != nil {
			return err
		}
		if r1 != nil && r2 != nil && !r1.Before(*r2) {
			return errBad("nominations must close before voting")
		}
		p.DeadlineMode = DeadlineScheduled
		p.Round1ClosesAt = r1
		p.Round2ClosesAt = r2
		return nil

	default:
		return errBad("invalid deadline mode %q", in.DeadlineMode)
	}
}

func validateDuration(round string, sec int) error {
	if sec == 0 {
		return nil // that round is untimed
	}
	if sec < minTimerSec {
		return errBad("%s timer must be at least %d seconds", round, minTimerSec)
	}
	if sec > maxTimerSec {
		return errBad("%s timer is too long", round)
	}
	return nil
}

func validateCloseTime(round string, t *time.Time, now time.Time) error {
	if t == nil {
		return nil // that round is host-advanced
	}
	if !t.After(now.Add(minTimerSec * time.Second)) {
		return errBad("%s close time must be in the future", round)
	}
	if t.After(now.Add(maxScheduleAhead)) {
		return errBad("%s close time is too far in the future", round)
	}
	return nil
}

// cleanGenres trims, de-dupes (case-insensitively), and drops empty genre names,
// preserving the host's order.
func cleanGenres(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, g := range in {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		key := strings.ToLower(g)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, g)
	}
	return out
}

// allowsGenres reports whether an item with the given genres may be nominated in
// a poll restricted to allowed (empty allowed = no restriction).
func allowsGenres(allowed, itemGenres []string) bool {
	if len(allowed) == 0 {
		return true
	}
	have := make(map[string]bool, len(itemGenres))
	for _, g := range itemGenres {
		have[strings.ToLower(strings.TrimSpace(g))] = true
	}
	for _, g := range allowed {
		if have[strings.ToLower(strings.TrimSpace(g))] {
			return true
		}
	}
	return false
}

func validateRules(r SubmissionRules) error {
	if r.Min < 0 || r.Max < 0 || r.Required < 0 {
		return errBad("submission rules cannot be negative")
	}
	if r.Required > 0 && (r.Min > 0 || r.Max > 0) {
		return errBad("set either a required count or a min/max, not both")
	}
	if r.Max > 0 && r.Min > r.Max {
		return errBad("min cannot exceed max")
	}
	return nil
}

// effectiveMax returns the per-participant nomination cap (0 = unlimited).
func (r SubmissionRules) effectiveMax() int {
	if r.Required > 0 {
		return r.Required
	}
	return r.Max
}

// MinToSubmit returns how many nominations a participant must make to be
// considered complete (for UI guidance).
func (r SubmissionRules) MinToSubmit() int {
	if r.Required > 0 {
		return r.Required
	}
	return r.Min
}

// JoinAsGuest creates a guest participant for the poll and returns it with a
// fresh session token.
func (s *Service) JoinAsGuest(ctx context.Context, p *Poll, displayName string) (*Participant, error) {
	name := strings.TrimSpace(displayName)
	if name == "" {
		return nil, errBad("a display name is required")
	}
	if len(name) > 40 {
		name = name[:40]
	}
	if !p.AllowGuests {
		return nil, errForbid("this poll does not allow guests")
	}
	if p.Status == StatusClosed {
		return nil, errConflict("this poll has ended")
	}
	part := &Participant{
		ID:           NewID(),
		PollID:       p.ID,
		DisplayName:  name,
		SessionToken: NewID(),
		Role:         RoleParticipant,
	}
	if err := s.repo.CreateParticipant(ctx, part); err != nil {
		return nil, err
	}
	return part, nil
}

// MergeDuplicateNames folds participants that share an exact display name into
// one, for people who rejoined from another device. The survivor is the host,
// else the one linked to Jellyfin, else whoever cast the newest ballot, else the
// newest joiner; only the newest ballot in each group is kept so nobody votes
// twice. Groups linked to two different Jellyfin accounts are different people
// and are left alone. Host only.
func (s *Service) MergeDuplicateNames(ctx context.Context, p *Poll, participant *Participant) error {
	if !participant.IsHost() {
		return errForbid("only the host can merge people")
	}
	if p.Status == StatusClosed {
		return errConflict("this poll has ended")
	}
	parts, err := s.repo.ListParticipants(ctx, p.ID)
	if err != nil {
		return err
	}
	ballotAt, err := s.ballotTimes(ctx, p)
	if err != nil {
		return err
	}
	groups := map[string][]Participant{}
	for _, pt := range parts {
		groups[pt.DisplayName] = append(groups[pt.DisplayName], pt)
	}
	for _, g := range groups {
		if len(g) < 2 {
			continue
		}
		var host, linked, newestJoin, newestBallot *Participant
		accounts := map[string]bool{}
		for i := range g {
			pt := &g[i]
			if pt.IsHost() {
				host = pt
			}
			if pt.JellyfinUserID != "" {
				linked = pt
				accounts[pt.JellyfinUserID] = true
			}
			if newestJoin == nil || pt.CreatedAt.After(newestJoin.CreatedAt) {
				newestJoin = pt
			}
			if at, ok := ballotAt[pt.ID]; ok && (newestBallot == nil || at.After(ballotAt[newestBallot.ID])) {
				newestBallot = pt
			}
		}
		if len(accounts) > 1 {
			continue
		}
		keep := host
		for _, c := range []*Participant{linked, newestBallot, newestJoin} {
			if keep == nil {
				keep = c
			}
		}
		if err := s.mergeGroup(ctx, p, keep, g, ballotAt); err != nil {
			return err
		}
	}
	return nil
}

// MergePeople lets the host fold one participant into another, for the same
// person who joined under a different name. keepID's name survives, unless
// dropID is the host (the host entry always survives). Only the newest ballot
// is kept, and a Jellyfin link carries over. Entries linked to two different
// Jellyfin accounts are different people and can't be merged. Host only.
func (s *Service) MergePeople(ctx context.Context, p *Poll, host *Participant, dropID, keepID string) error {
	if !host.IsHost() {
		return errForbid("only the host can merge people")
	}
	if p.Status == StatusClosed {
		return errConflict("this poll has ended")
	}
	if dropID == keepID {
		return errBad("pick two different people")
	}
	var pair [2]*Participant
	for i, id := range []string{dropID, keepID} {
		pt, err := s.repo.GetParticipant(ctx, id)
		if errors.Is(err, ErrNotFound) || (err == nil && pt.PollID != p.ID) {
			return &Error{Code: 404, Msg: "participant not found"}
		}
		if err != nil {
			return err
		}
		pair[i] = pt
	}
	drop, keep := pair[0], pair[1]
	if drop.IsHost() {
		drop, keep = keep, drop
	}
	if drop.JellyfinUserID != "" && keep.JellyfinUserID != "" && drop.JellyfinUserID != keep.JellyfinUserID {
		return errConflict("they're linked to different Jellyfin accounts")
	}
	ballotAt, err := s.ballotTimes(ctx, p)
	if err != nil {
		return err
	}
	if err := s.mergeGroup(ctx, p, keep, []Participant{*keep, *drop}, ballotAt); err != nil {
		return err
	}
	if keep.JellyfinUserID == "" && drop.JellyfinUserID != "" {
		return s.repo.SetParticipantJellyfinUser(ctx, keep.ID, drop.JellyfinUserID)
	}
	return nil
}

// ballotTimes maps each participant who voted to when they cast their ballot.
func (s *Service) ballotTimes(ctx context.Context, p *Poll) (map[string]time.Time, error) {
	votes, err := s.repo.ListVotes(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	at := map[string]time.Time{}
	for _, v := range votes {
		if v.CreatedAt.After(at[v.ParticipantID]) {
			at[v.ParticipantID] = v.CreatedAt
		}
	}
	return at, nil
}

// mergeGroup folds every participant in group into keep, keeping only the
// group's newest ballot.
func (s *Service) mergeGroup(ctx context.Context, p *Poll, keep *Participant, group []Participant, ballotAt map[string]time.Time) error {
	var drop []string
	ballotFrom := ""
	for _, pt := range group {
		if pt.ID != keep.ID {
			drop = append(drop, pt.ID)
		}
		if at, ok := ballotAt[pt.ID]; ok && (ballotFrom == "" || at.After(ballotAt[ballotFrom])) {
			ballotFrom = pt.ID
		}
	}
	return s.repo.MergeParticipants(ctx, p.ID, keep.ID, ballotFrom, drop)
}

// LinkJellyfin ties target to a Jellyfin account so signing in on any device
// lands on this entry. If another entry in the poll is already linked to that
// account, target is merged into it. It returns the participant the caller's
// browser should now use. allowRelink lets the host move an already-linked
// entry to a different account; a self-link can't.
func (s *Service) LinkJellyfin(ctx context.Context, p *Poll, target *Participant, uid string, allowRelink bool) (*Participant, error) {
	if uid == "" {
		return nil, errBad("a Jellyfin account is required")
	}
	if target.JellyfinUserID == uid {
		return target, nil
	}
	if target.JellyfinUserID != "" && !allowRelink {
		return nil, errConflict("you're already linked to another Jellyfin account")
	}
	existing, err := s.repo.GetParticipantByJellyfinUser(ctx, p.ID, uid)
	if errors.Is(err, ErrNotFound) {
		if err := s.repo.SetParticipantJellyfinUser(ctx, target.ID, uid); err != nil {
			return nil, err
		}
		target.JellyfinUserID = uid
		return target, nil
	}
	if err != nil {
		return nil, err
	}
	if target.IsHost() {
		return nil, errConflict("that Jellyfin account is already linked to someone else in this poll")
	}
	ballotAt, err := s.ballotTimes(ctx, p)
	if err != nil {
		return nil, err
	}
	if err := s.mergeGroup(ctx, p, existing, []Participant{*existing, *target}, ballotAt); err != nil {
		return nil, err
	}
	return existing, nil
}

// HostLinkJellyfin lets the host link any participant to a Jellyfin account
// (the caller has already checked the account exists).
func (s *Service) HostLinkJellyfin(ctx context.Context, p *Poll, host *Participant, targetID, uid string) error {
	if !host.IsHost() {
		return errForbid("only the host can link people")
	}
	if p.Status == StatusClosed {
		return errConflict("this poll has ended")
	}
	target, err := s.repo.GetParticipant(ctx, targetID)
	if errors.Is(err, ErrNotFound) || (err == nil && target.PollID != p.ID) {
		return &Error{Code: 404, Msg: "participant not found"}
	}
	if err != nil {
		return err
	}
	_, err = s.LinkJellyfin(ctx, p, target, uid, true)
	return err
}

// SubmitNomination resolves the item, enforces scope and the per-participant
// max, and records the nomination.
func (s *Service) SubmitNomination(ctx context.Context, p *Poll, participant *Participant, itemID string) (*Nomination, error) {
	if p.Status != StatusRound1 {
		return nil, errConflict("nominations are closed")
	}
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		return nil, errBad("item id is required")
	}
	item, err := s.items.GetItem(ctx, itemID)
	if err != nil {
		return nil, err
	}
	if item == nil {
		return nil, errBad("that title is not in the library")
	}
	if !scopeAllows(p.LibraryScope, item.Type) {
		return nil, errBad("%ss can't be nominated in this poll", strings.ToLower(item.Type))
	}
	if !allowsGenres(p.Genres, item.Genres) {
		return nil, errBad("this poll only allows: %s", strings.Join(p.Genres, ", "))
	}

	snap := ItemSnapshot{
		Title:    item.Title,
		Year:     item.Year,
		Type:     item.Type,
		Runtime:  item.Runtime,
		Overview: item.Overview,
		ImageTag: item.ImageTag,
	}
	return s.addNomination(ctx, p, participant, itemID, snap)
}

// WriteInInput describes a Seerr/TMDB title nominated even though it isn't in
// the library.
type WriteInInput struct {
	TMDBID    int
	MediaType string // "movie" | "tv"
	Title     string
	Year      int
	PosterURL string
	Overview  string
}

// SubmitWriteIn records a write-in (a title not in the library) for a poll that
// allows them. Genre gating does not apply to write-ins.
func (s *Service) SubmitWriteIn(ctx context.Context, p *Poll, participant *Participant, in WriteInInput) (*Nomination, error) {
	if p.Status != StatusRound1 {
		return nil, errConflict("nominations are closed")
	}
	if !p.AllowWriteins {
		return nil, errForbid("this poll doesn't allow titles outside the library")
	}
	if in.MediaType != "movie" && in.MediaType != "tv" {
		return nil, errBad("invalid media type %q", in.MediaType)
	}
	if in.TMDBID <= 0 || strings.TrimSpace(in.Title) == "" {
		return nil, errBad("a title is required")
	}
	itemType := "Movie"
	if in.MediaType == "tv" {
		itemType = "Series"
	}
	if !scopeAllows(p.LibraryScope, itemType) {
		return nil, errBad("%ss can't be nominated in this poll", strings.ToLower(itemType))
	}
	key := fmt.Sprintf("seerr:%s:%d", in.MediaType, in.TMDBID)
	snap := ItemSnapshot{
		Title:     in.Title,
		Year:      in.Year,
		Type:      itemType,
		Overview:  in.Overview,
		Source:    SourceSeerr,
		TMDBID:    in.TMDBID,
		MediaType: in.MediaType,
		PosterURL: in.PosterURL,
	}
	return s.addNomination(ctx, p, participant, key, snap)
}

// addNomination enforces the per-participant cap and records a nomination keyed
// by key (the Jellyfin item id for library titles, or a "seerr:<type>:<id>"
// surrogate for write-ins).
func (s *Service) addNomination(ctx context.Context, p *Poll, participant *Participant, key string, snap ItemSnapshot) (*Nomination, error) {
	if max := p.SubmissionRules.effectiveMax(); max > 0 {
		count, err := s.repo.CountNominationsByParticipant(ctx, p.ID, participant.ID)
		if err != nil {
			return nil, err
		}
		// Allow re-affirming an existing nomination; only block brand-new ones
		// once at the cap.
		if count >= max {
			already, err := s.alreadyNominated(ctx, p.ID, key)
			if err != nil {
				return nil, err
			}
			if !already {
				return nil, errBad("you can nominate at most %d", max)
			}
		}
	}
	n := &Nomination{PollID: p.ID, JellyfinItemID: key, Snapshot: snap}
	return s.repo.AddNomination(ctx, n, participant.ID)
}

func (s *Service) alreadyNominated(ctx context.Context, pollID, itemID string) (bool, error) {
	noms, err := s.repo.ListNominations(ctx, pollID)
	if err != nil {
		return false, err
	}
	for _, n := range noms {
		if n.JellyfinItemID == itemID {
			return true, nil
		}
	}
	return false, nil
}

// WithdrawNomination removes the participant as a nominator (deleting the
// nomination if nobody else nominated it).
func (s *Service) WithdrawNomination(ctx context.Context, p *Poll, participant *Participant, nominationID string) error {
	if p.Status != StatusRound1 {
		return errConflict("nominations are closed")
	}
	return s.repo.WithdrawNomination(ctx, p.ID, nominationID, participant.ID)
}

// Advance moves the poll to the next stage at the host's request (this also
// backs the "end now" timer control). Only the host may advance. When round 2
// closes, the ballots of the participants in ignore are discarded so they don't
// count toward the results.
func (s *Service) Advance(ctx context.Context, p *Poll, participant *Participant, ignore ...string) (*Poll, error) {
	if !participant.IsHost() {
		return nil, errForbid("only the host can advance the poll")
	}
	wasRound2 := p.Status == StatusRound2
	np, err := s.advanceCore(ctx, p, false)
	if err != nil {
		return nil, err
	}
	// Dropped only after closing, so an ignored voter can't re-submit.
	if wasRound2 && np.Status == StatusClosed {
		for _, id := range ignore {
			if err := s.repo.ReplaceVotes(ctx, p.ID, id, nil); err != nil {
				return nil, err
			}
		}
	}
	return np, nil
}

// advanceCore performs the actual round transition, shared by the host-driven
// Advance and the timer sweeper. When auto is false and the poll has no timer,
// round 1 still requires ≥2 nominations (the long-standing manual guard);
// otherwise round 1 resolves gracefully — exactly one nomination is crowned the
// winner, zero closes the poll empty. A CompareAndSetStatus claims each
// transition so a manual advance and the sweeper can't double-advance.
func (s *Service) advanceCore(ctx context.Context, p *Poll, auto bool) (*Poll, error) {
	switch p.Status {
	case StatusRound1:
		noms, err := s.repo.ListNominations(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		if len(noms) < 2 {
			if !auto && p.DeadlineMode == DeadlineNone {
				return nil, errConflict("need at least 2 nominations to start voting")
			}
			return s.resolveSparseRound1(ctx, p, noms)
		}
		// Methods that decide without a voting round (e.g. random) close
		// immediately: draw the winner once and freeze it.
		if dec, ok := voting.Decider(p.VotingMethod); ok {
			ids := make([]string, len(noms))
			for i, n := range noms {
				ids[i] = n.ID
			}
			winner, err := dec.Decide(p.VotingConfig, ids)
			if err != nil {
				return nil, errBad("%v", err)
			}
			ok, err := s.repo.CompareAndSetStatus(ctx, p.ID, StatusRound1, StatusClosed)
			if err != nil {
				return nil, err
			}
			if !ok {
				return s.repo.GetPollByID(ctx, p.ID) // already advanced concurrently
			}
			if err := s.repo.SetPollWinner(ctx, p.ID, winner); err != nil {
				return nil, err
			}
			p.WinnerNominationID = winner
			p.Status = StatusClosed
			return p, nil
		}
		ok, err := s.repo.CompareAndSetStatus(ctx, p.ID, StatusRound1, StatusRound2)
		if err != nil {
			return nil, err
		}
		if !ok {
			return s.repo.GetPollByID(ctx, p.ID)
		}
		p.Status = StatusRound2
		// Entering round 2: a "quick" poll starts the voting clock now; any
		// paused state from round 1 is cleared.
		p.TimerPausedSec = 0
		if p.DeadlineMode == DeadlineQuick && p.Round2DurationSec > 0 {
			t := time.Now().Add(time.Duration(p.Round2DurationSec) * time.Second)
			p.Round2ClosesAt = &t
		}
		if err := s.repo.UpdatePollTimers(ctx, p); err != nil {
			return nil, err
		}
		return p, nil
	case StatusRound2:
		ok, err := s.repo.CompareAndSetStatus(ctx, p.ID, StatusRound2, StatusClosed)
		if err != nil {
			return nil, err
		}
		if !ok {
			return s.repo.GetPollByID(ctx, p.ID)
		}
		p.Status = StatusClosed
		return p, nil
	default:
		return nil, errConflict("the poll cannot be advanced from %q", p.Status)
	}
}

// resolveSparseRound1 closes a round 1 that has too few nominations to vote on:
// exactly one nomination is crowned the winner, zero closes empty.
func (s *Service) resolveSparseRound1(ctx context.Context, p *Poll, noms []Nomination) (*Poll, error) {
	ok, err := s.repo.CompareAndSetStatus(ctx, p.ID, StatusRound1, StatusClosed)
	if err != nil {
		return nil, err
	}
	if !ok {
		return s.repo.GetPollByID(ctx, p.ID)
	}
	p.Status = StatusClosed
	if len(noms) == 1 {
		if err := s.repo.SetPollWinner(ctx, p.ID, noms[0].ID); err != nil {
			return nil, err
		}
		p.WinnerNominationID = noms[0].ID
	}
	return p, nil
}

// StartTimer starts (or resumes) the active round's clock. Host only; "quick"
// mode. A paused timer resumes with its remaining time; an armed one runs for
// its configured duration.
func (s *Service) StartTimer(ctx context.Context, p *Poll, participant *Participant) (*Poll, error) {
	if !participant.IsHost() {
		return nil, errForbid("only the host can control the timer")
	}
	if p.Status != StatusRound1 && p.Status != StatusRound2 {
		return nil, errConflict("the timer can't be started now")
	}
	now := time.Now()
	switch {
	case p.TimerPausedSec > 0: // resume
		t := now.Add(time.Duration(p.TimerPausedSec) * time.Second)
		p.setActiveClosesAt(&t)
		p.TimerPausedSec = 0
	case p.activeClosesAt() != nil:
		return nil, errConflict("the timer is already running")
	case p.activeDurationSec() > 0: // armed → start
		t := now.Add(time.Duration(p.activeDurationSec()) * time.Second)
		p.setActiveClosesAt(&t)
	default:
		return nil, errConflict("this round has no timer to start")
	}
	if err := s.repo.UpdatePollTimers(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// PauseTimer freezes the active round's running clock, remembering how much
// time was left. Host only.
func (s *Service) PauseTimer(ctx context.Context, p *Poll, participant *Participant) (*Poll, error) {
	if !participant.IsHost() {
		return nil, errForbid("only the host can control the timer")
	}
	closesAt := p.activeClosesAt()
	if closesAt == nil {
		return nil, errConflict("there's no running timer to pause")
	}
	remaining := int(time.Until(*closesAt).Seconds())
	if remaining < 1 {
		remaining = 1
	}
	p.TimerPausedSec = remaining
	p.setActiveClosesAt(nil)
	if err := s.repo.UpdatePollTimers(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// ExtendTimer adds time to the active round's timer, whether it's running,
// paused, or armed. Host only.
func (s *Service) ExtendTimer(ctx context.Context, p *Poll, participant *Participant, addSec int) (*Poll, error) {
	if !participant.IsHost() {
		return nil, errForbid("only the host can control the timer")
	}
	if addSec < 1 || addSec > maxTimerSec {
		return nil, errBad("invalid amount of time to add")
	}
	switch {
	case p.TimerPausedSec > 0:
		p.TimerPausedSec += addSec
	case p.activeClosesAt() != nil: // running
		t := p.activeClosesAt().Add(time.Duration(addSec) * time.Second)
		p.setActiveClosesAt(&t)
	case p.activeDurationSec() > 0: // armed
		switch p.Status {
		case StatusRound1:
			p.Round1DurationSec += addSec
		case StatusRound2:
			p.Round2DurationSec += addSec
		}
	default:
		return nil, errConflict("this round has no timer to extend")
	}
	if err := s.repo.UpdatePollTimers(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// SweepDueTimers advances every poll whose active round's timer has run out,
// returning the polls that changed so the caller can broadcast / fire winner
// auto-requests. It is the unguarded (non-host) path into advanceCore.
func (s *Service) SweepDueTimers(ctx context.Context, now time.Time) ([]*Poll, error) {
	polls, err := s.repo.ListActiveTimedPolls(ctx)
	if err != nil {
		return nil, err
	}
	var changed []*Poll
	for _, p := range polls {
		closesAt := p.activeClosesAt()
		if closesAt == nil || closesAt.After(now) {
			continue
		}
		updated, err := s.advanceCore(ctx, p, true)
		if err != nil {
			return changed, err
		}
		changed = append(changed, updated)
	}
	return changed, nil
}

// ListHistory returns every poll (newest first) with its aggregate counts, for
// the admin dashboard.
func (s *Service) ListHistory(ctx context.Context) ([]PollSummary, error) {
	polls, err := s.repo.ListPolls(ctx)
	if err != nil {
		return nil, err
	}
	counts, err := s.repo.AllPollCounts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]PollSummary, 0, len(polls))
	for _, p := range polls {
		out = append(out, PollSummary{Poll: p, Counts: counts[p.ID]})
	}
	return out, nil
}

// ForceAdvance moves a poll forward on the admin's behalf (no host check). It
// uses the lenient auto path, so a round 1 with fewer than two nominations
// resolves gracefully (crowns the single nomination, or closes empty) instead
// of erroring as the host-driven Advance would.
func (s *Service) ForceAdvance(ctx context.Context, p *Poll) (*Poll, error) {
	if p.Status == StatusClosed {
		return nil, errConflict("the poll is already closed")
	}
	return s.advanceCore(ctx, p, true)
}

// DeletePoll permanently removes a poll and all of its data.
func (s *Service) DeletePoll(ctx context.Context, id string) error {
	return s.repo.DeletePoll(ctx, id)
}

// PurgeExpired deletes closed polls that ended more than retentionDays ago,
// returning how many were removed. retentionDays <= 0 disables retention and is
// a no-op.
func (s *Service) PurgeExpired(ctx context.Context, now time.Time, retentionDays int) (int, error) {
	if retentionDays <= 0 {
		return 0, nil
	}
	cutoff := now.Add(-time.Duration(retentionDays) * 24 * time.Hour)
	return s.repo.DeleteClosedPollsBefore(ctx, cutoff)
}

// UpdateSettings changes a running poll's voting config and live-results flag.
// Host only, and never once closed. In round 2 a config is accepted only if
// every ballot already cast still validates under it, so nobody has to revote.
func (s *Service) UpdateSettings(ctx context.Context, p *Poll, participant *Participant, cfg json.RawMessage, resultsLive bool) error {
	if !participant.IsHost() {
		return errForbid("only the host can change settings")
	}
	if p.Status == StatusClosed {
		return errConflict("the poll is closed")
	}
	method, ok := voting.Get(p.VotingMethod)
	if !ok {
		return errBad("unknown voting method %q", p.VotingMethod)
	}
	if len(cfg) == 0 {
		return errBad("voting config is required")
	}
	if err := method.ValidateConfig(cfg); err != nil {
		return errBad("voting config: %v", err)
	}
	if p.Status == StatusRound2 {
		if err := s.checkBallotsStillValid(ctx, p, method, cfg); err != nil {
			return err
		}
	}
	p.VotingConfig = cfg
	p.ResultsLive = resultsLive
	return s.repo.UpdatePollSettings(ctx, p)
}

func (s *Service) checkBallotsStillValid(ctx context.Context, p *Poll, method voting.Method, cfg json.RawMessage) error {
	noms, err := s.repo.ListNominations(ctx, p.ID)
	if err != nil {
		return err
	}
	votes, err := s.repo.ListVotes(ctx, p.ID)
	if err != nil {
		return err
	}
	allIDs := make([]string, 0, len(noms))
	own := map[string][]string{} // participant -> nominations they made
	for _, n := range noms {
		allIDs = append(allIDs, n.ID)
		for _, nominator := range n.Nominators {
			own[nominator] = append(own[nominator], n.ID)
		}
	}
	ballots := map[string]map[string]int{}
	for _, v := range votes {
		if ballots[v.ParticipantID] == nil {
			ballots[v.ParticipantID] = map[string]int{}
		}
		ballots[v.ParticipantID][v.NominationID] = v.Value
	}
	broken := 0
	for pid, sel := range ballots {
		b := voting.Ballot{ParticipantID: pid, Selections: sel}
		if voting.ValidateBallot(method, cfg, b, allIDs, own[pid]) != nil {
			broken++
		}
	}
	if broken > 0 {
		return errConflict("that change would undo %d ballot(s) already cast", broken)
	}
	return nil
}

// CastVotes validates and records a participant's ballot for round 2.
func (s *Service) CastVotes(ctx context.Context, p *Poll, participant *Participant, selections map[string]int) error {
	if p.Status != StatusRound2 {
		return errConflict("voting is not open")
	}
	method, ok := voting.Get(p.VotingMethod)
	if !ok {
		return errBad("unknown voting method %q", p.VotingMethod)
	}
	noms, err := s.repo.ListNominations(ctx, p.ID)
	if err != nil {
		return err
	}
	allIDs := make([]string, 0, len(noms))
	var ownIDs []string
	for _, n := range noms {
		allIDs = append(allIDs, n.ID)
		for _, nominator := range n.Nominators {
			if nominator == participant.ID {
				ownIDs = append(ownIDs, n.ID)
				break
			}
		}
	}
	ballot := voting.Ballot{ParticipantID: participant.ID, Selections: selections}
	if err := voting.ValidateBallot(method, p.VotingConfig, ballot, allIDs, ownIDs); err != nil {
		return errBad("%v", err)
	}
	votes := make([]Vote, 0, len(selections))
	for nomID, value := range selections {
		if value == 0 {
			continue
		}
		votes = append(votes, Vote{NominationID: nomID, Value: value})
	}
	return s.repo.ReplaceVotes(ctx, p.ID, participant.ID, votes)
}

// Results tallies the poll. nominations is returned alongside so callers can
// map IDs to titles.
func (s *Service) Results(ctx context.Context, p *Poll) (voting.Results, []Nomination, error) {
	method, ok := voting.Get(p.VotingMethod)
	if !ok {
		return voting.Results{}, nil, errBad("unknown voting method %q", p.VotingMethod)
	}
	noms, err := s.repo.ListNominations(ctx, p.ID)
	if err != nil {
		return voting.Results{}, nil, err
	}
	allIDs := make([]string, 0, len(noms))
	for _, n := range noms {
		allIDs = append(allIDs, n.ID)
	}
	votes, err := s.repo.ListVotes(ctx, p.ID)
	if err != nil {
		return voting.Results{}, nil, err
	}
	byParticipant := map[string]map[string]int{}
	for _, v := range votes {
		if byParticipant[v.ParticipantID] == nil {
			byParticipant[v.ParticipantID] = map[string]int{}
		}
		byParticipant[v.ParticipantID][v.NominationID] = v.Value
	}
	ballots := make([]voting.Ballot, 0, len(byParticipant))
	for pid, sel := range byParticipant {
		ballots = append(ballots, voting.Ballot{ParticipantID: pid, Selections: sel})
	}
	res, err := voting.Tally(method, p.VotingConfig, allIDs, ballots)
	if err != nil {
		return voting.Results{}, nil, err
	}
	// A frozen winner (random pick, or any decided-once method) is authoritative
	// over the tally. When it was drawn from a tallied tie (BreakTie), keep the
	// co-winners so the UI can say the tie was broken at random.
	if p.WinnerNominationID != "" {
		if _, decides := voting.Decider(p.VotingMethod); !decides && len(res.WinnerIDs) > 1 {
			for _, id := range res.WinnerIDs {
				if id == p.WinnerNominationID {
					res.TiedIDs = res.WinnerIDs
					break
				}
			}
		}
		res.WinnerIDs = []string{p.WinnerNominationID}
	}
	return res, noms, nil
}

// BreakTie draws one of a closed poll's tied winners at random and freezes it
// as the winner, so every participant sees the same pick. Host only; a poll
// without a tie (or whose winner is already frozen) is a conflict.
func (s *Service) BreakTie(ctx context.Context, p *Poll, participant *Participant) (*Poll, error) {
	if !participant.IsHost() {
		return nil, errForbid("only the host can break a tie")
	}
	if p.Status != StatusClosed {
		return nil, errConflict("the poll hasn't closed yet")
	}
	if p.WinnerNominationID != "" {
		return nil, errConflict("the winner has already been decided")
	}
	res, _, err := s.Results(ctx, p)
	if err != nil {
		return nil, err
	}
	if len(res.WinnerIDs) < 2 {
		return nil, errConflict("there's no tie to break")
	}
	winner, err := voting.Random{}.Decide(nil, res.WinnerIDs)
	if err != nil {
		return nil, err
	}
	ok, err := s.repo.SetPollWinnerIfUnset(ctx, p.ID, winner)
	if err != nil {
		return nil, err
	}
	if !ok {
		return s.repo.GetPollByID(ctx, p.ID) // someone else broke it first
	}
	p.WinnerNominationID = winner
	return p, nil
}

func scopeAllows(scope LibraryScope, itemType string) bool {
	switch scope {
	case ScopeMovies:
		return itemType == "Movie"
	case ScopeSeries:
		return itemType == "Series"
	default:
		return itemType == "Movie" || itemType == "Series"
	}
}

// IsNotFound reports whether err indicates a missing entity.
func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }
