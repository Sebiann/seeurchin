package poll

import (
	"context"
	"time"
)

// Repository is the persistence contract for the poll domain. The sqlite
// implementation lives in internal/store.
type Repository interface {
	// CreatePoll inserts a poll together with its host participant atomically,
	// setting p.HostParticipantID to host.ID.
	CreatePoll(ctx context.Context, p *Poll, host *Participant) error
	GetPollByCode(ctx context.Context, code string) (*Poll, error)
	GetPollByID(ctx context.Context, id string) (*Poll, error)
	UpdatePollStatus(ctx context.Context, id string, status Status) error
	// CompareAndSetStatus atomically moves a poll from one status to another,
	// reporting whether the row was changed (false = it wasn't in `from`, so a
	// concurrent advance already happened). Used to make round transitions safe
	// against the timer sweeper racing a manual advance.
	CompareAndSetStatus(ctx context.Context, id string, from, to Status) (bool, error)
	// UpdatePollTimers persists the deadline-related fields (durations, close
	// times, paused remainder) for a poll.
	UpdatePollTimers(ctx context.Context, p *Poll) error
	// UpdatePollSettings persists the host-editable settings (voting config,
	// live results) for a poll.
	UpdatePollSettings(ctx context.Context, p *Poll) error
	// ListActiveTimedPolls returns every poll currently in round 1 or round 2
	// that has a close time set for its active round (i.e. a running timer). The
	// caller decides which have actually expired.
	ListActiveTimedPolls(ctx context.Context) ([]*Poll, error)
	// SetPollWinner freezes the decided winner (for methods that decide without
	// a voting round, e.g. random) and stamps decided_at.
	SetPollWinner(ctx context.Context, id, nominationID string) error
	// SetPollWinnerIfUnset freezes a winner only if none is frozen yet,
	// reporting whether it did — so two concurrent tie-breaks can't both win.
	SetPollWinnerIfUnset(ctx context.Context, id, nominationID string) (bool, error)
	CodeExists(ctx context.Context, code string) (bool, error)

	// ListPolls returns every poll, newest first, for the admin history view.
	ListPolls(ctx context.Context) ([]*Poll, error)
	// AllPollCounts returns the participant/nomination/voter tallies for every
	// poll, keyed by poll ID, in a fixed number of grouped queries (no per-poll
	// fan-out).
	AllPollCounts(ctx context.Context) (map[string]PollCounts, error)
	// DeletePoll removes a poll and (via ON DELETE CASCADE) all of its
	// participants, nominations, votes, and Seerr requests.
	DeletePoll(ctx context.Context, id string) error
	// DeleteClosedPollsBefore deletes every closed poll whose end time
	// (closed_at, or decided_at as a fallback) is before cutoff, returning how
	// many were removed. Polls with no recorded end time are never purged.
	DeleteClosedPollsBefore(ctx context.Context, cutoff time.Time) (int, error)

	CreateParticipant(ctx context.Context, p *Participant) error
	GetParticipant(ctx context.Context, id string) (*Participant, error)
	GetParticipantBySession(ctx context.Context, pollID, sessionToken string) (*Participant, error)
	ListParticipants(ctx context.Context, pollID string) ([]Participant, error)
	// GetParticipantByJellyfinUser returns the poll's participant linked to a
	// Jellyfin account, or ErrNotFound.
	GetParticipantByJellyfinUser(ctx context.Context, pollID, jellyfinUserID string) (*Participant, error)
	SetParticipantJellyfinUser(ctx context.Context, participantID, jellyfinUserID string) error
	// MergeParticipants atomically folds dropIDs into keepID: their nominations
	// move to keepID, ballotFromID's votes (if set and not keepID) become
	// keepID's, and the dropped participants are deleted with their other votes.
	MergeParticipants(ctx context.Context, pollID, keepID, ballotFromID string, dropIDs []string) error

	// AddNomination records nominatorID's nomination of an item. If the item is
	// already nominated in the poll it adds the nominator to the existing
	// nomination; otherwise it creates one. The effective nomination is returned.
	AddNomination(ctx context.Context, n *Nomination, nominatorID string) (*Nomination, error)
	// WithdrawNomination removes participantID as a nominator; if none remain the
	// nomination is deleted.
	WithdrawNomination(ctx context.Context, pollID, nominationID, participantID string) error
	ListNominations(ctx context.Context, pollID string) ([]Nomination, error)
	CountNominationsByParticipant(ctx context.Context, pollID, participantID string) (int, error)

	// ReplaceVotes atomically replaces all of participantID's votes in the poll.
	ReplaceVotes(ctx context.Context, pollID, participantID string, votes []Vote) error
	ListVotes(ctx context.Context, pollID string) ([]Vote, error)
	CountVoters(ctx context.Context, pollID string) (int, error)
	HasVoted(ctx context.Context, pollID, participantID string) (bool, error)

	// RecordSeerrRequest stores the outcome of requesting a winning write-in
	// (idempotent on (poll_id, nomination_id)). GetSeerrRequest returns it, or
	// (nil, nil) if none exists.
	RecordSeerrRequest(ctx context.Context, req *SeerrRequest) error
	GetSeerrRequest(ctx context.Context, pollID, nominationID string) (*SeerrRequest, error)
}
