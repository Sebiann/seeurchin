package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/enderu/seeurchin/internal/poll"
)

// mkPoll inserts a poll with the given code/title/created-at for the admin tests.
func mkPoll(t *testing.T, s *Store, code, title string, createdAt time.Time) *poll.Poll {
	t.Helper()
	p := &poll.Poll{
		ID:           poll.NewID(),
		Code:         code,
		Title:        title,
		LibraryScope: poll.ScopeBoth,
		Status:       poll.StatusRound1,
		VotingMethod: "approval",
		VotingConfig: json.RawMessage(`{}`),
		AllowGuests:  true,
		CreatedAt:    createdAt,
	}
	host := &poll.Participant{ID: poll.NewID(), DisplayName: "Host", CreatedAt: createdAt}
	if err := s.CreatePoll(context.Background(), p, host); err != nil {
		t.Fatalf("CreatePoll %q: %v", code, err)
	}
	return p
}

func TestListPollsNewestFirst(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	mkPoll(t, s, "AAAAAA", "Older", now.Add(-2*time.Hour))
	mkPoll(t, s, "BBBBBB", "Newer", now.Add(-1*time.Hour))

	polls, err := s.ListPolls(ctx)
	if err != nil {
		t.Fatalf("ListPolls: %v", err)
	}
	if len(polls) != 2 {
		t.Fatalf("got %d polls, want 2", len(polls))
	}
	if polls[0].Title != "Newer" || polls[1].Title != "Older" {
		t.Fatalf("not newest-first: %q, %q", polls[0].Title, polls[1].Title)
	}
}

func TestAllPollCounts(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	p := mkPoll(t, s, "AAAAAA", "Counted", now)
	mkPoll(t, s, "BBBBBB", "Empty", now) // a second poll with only its host

	// Two guests join; both nominate the same item (merged), and both vote on it.
	g1 := &poll.Participant{ID: poll.NewID(), PollID: p.ID, DisplayName: "G1"}
	g2 := &poll.Participant{ID: poll.NewID(), PollID: p.ID, DisplayName: "G2"}
	for _, g := range []*poll.Participant{g1, g2} {
		if err := s.CreateParticipant(ctx, g); err != nil {
			t.Fatalf("CreateParticipant: %v", err)
		}
	}
	nom := &poll.Nomination{PollID: p.ID, JellyfinItemID: "m1", Snapshot: poll.ItemSnapshot{Title: "Dune", Type: "Movie"}}
	got, err := s.AddNomination(ctx, nom, g1.ID)
	if err != nil {
		t.Fatalf("AddNomination: %v", err)
	}
	if _, err := s.AddNomination(ctx, &poll.Nomination{PollID: p.ID, JellyfinItemID: "m1"}, g2.ID); err != nil {
		t.Fatalf("AddNomination (merge): %v", err)
	}
	for _, g := range []*poll.Participant{g1, g2} {
		if err := s.ReplaceVotes(ctx, p.ID, g.ID, []poll.Vote{{NominationID: got.ID, Value: 1}}); err != nil {
			t.Fatalf("ReplaceVotes: %v", err)
		}
	}

	counts, err := s.AllPollCounts(ctx)
	if err != nil {
		t.Fatalf("AllPollCounts: %v", err)
	}
	c := counts[p.ID]
	if c.Participants != 3 { // host + 2 guests
		t.Errorf("participants = %d, want 3", c.Participants)
	}
	if c.Nominations != 1 { // merged into one
		t.Errorf("nominations = %d, want 1", c.Nominations)
	}
	if c.Voters != 2 {
		t.Errorf("voters = %d, want 2", c.Voters)
	}
}

func TestCompareAndSetStatusStampsClosedAt(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p := mkPoll(t, s, "AAAAAA", "Closing", time.Now().UTC())

	// A non-closing transition leaves closed_at unset.
	if ok, err := s.CompareAndSetStatus(ctx, p.ID, poll.StatusRound1, poll.StatusRound2); err != nil || !ok {
		t.Fatalf("to round2: ok=%v err=%v", ok, err)
	}
	got, _ := s.GetPollByID(ctx, p.ID)
	if got.ClosedAt != nil {
		t.Fatalf("closed_at set too early: %v", got.ClosedAt)
	}

	// Closing stamps closed_at.
	if ok, err := s.CompareAndSetStatus(ctx, p.ID, poll.StatusRound2, poll.StatusClosed); err != nil || !ok {
		t.Fatalf("to closed: ok=%v err=%v", ok, err)
	}
	got, _ = s.GetPollByID(ctx, p.ID)
	if got.ClosedAt == nil {
		t.Fatal("closed_at not stamped on close")
	}
}

func TestDeletePollCascades(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p := mkPoll(t, s, "AAAAAA", "Doomed", time.Now().UTC())
	g := &poll.Participant{ID: poll.NewID(), PollID: p.ID, DisplayName: "G"}
	if err := s.CreateParticipant(ctx, g); err != nil {
		t.Fatalf("CreateParticipant: %v", err)
	}
	nom, err := s.AddNomination(ctx, &poll.Nomination{PollID: p.ID, JellyfinItemID: "m1"}, g.ID)
	if err != nil {
		t.Fatalf("AddNomination: %v", err)
	}
	if err := s.ReplaceVotes(ctx, p.ID, g.ID, []poll.Vote{{NominationID: nom.ID, Value: 1}}); err != nil {
		t.Fatalf("ReplaceVotes: %v", err)
	}

	if err := s.DeletePoll(ctx, p.ID); err != nil {
		t.Fatalf("DeletePoll: %v", err)
	}
	if _, err := s.GetPollByID(ctx, p.ID); err != poll.ErrNotFound {
		t.Fatalf("poll still present: %v", err)
	}
	if parts, _ := s.ListParticipants(ctx, p.ID); len(parts) != 0 {
		t.Errorf("participants not cascaded: %d", len(parts))
	}
	if noms, _ := s.ListNominations(ctx, p.ID); len(noms) != 0 {
		t.Errorf("nominations not cascaded: %d", len(noms))
	}
	if votes, _ := s.ListVotes(ctx, p.ID); len(votes) != 0 {
		t.Errorf("votes not cascaded: %d", len(votes))
	}

	// Deleting a missing poll reports not found.
	if err := s.DeletePoll(ctx, "nope"); err != poll.ErrNotFound {
		t.Fatalf("delete missing: %v, want ErrNotFound", err)
	}
}

func TestDeleteClosedPollsBefore(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	open := mkPoll(t, s, "OPENAA", "Open", now)
	closed := mkPoll(t, s, "CLOSED1", "Closed", now)
	if ok, err := s.CompareAndSetStatus(ctx, closed.ID, poll.StatusRound1, poll.StatusClosed); err != nil || !ok {
		t.Fatalf("close: ok=%v err=%v", ok, err)
	}

	// A cutoff in the future is past the just-stamped closed_at, so the closed
	// poll is purged and the open one is left alone.
	n, err := s.DeleteClosedPollsBefore(ctx, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("DeleteClosedPollsBefore: %v", err)
	}
	if n != 1 {
		t.Fatalf("purged %d, want 1", n)
	}
	if _, err := s.GetPollByID(ctx, closed.ID); err != poll.ErrNotFound {
		t.Errorf("closed poll survived purge: %v", err)
	}
	if _, err := s.GetPollByID(ctx, open.ID); err != nil {
		t.Errorf("open poll was purged: %v", err)
	}

	// A cutoff before any close time purges nothing.
	if n, err := s.DeleteClosedPollsBefore(ctx, now.Add(-24*time.Hour)); err != nil || n != 0 {
		t.Fatalf("early cutoff purged %d (err %v), want 0", n, err)
	}
}

func TestClonePollCopiesStateWithFreshIDs(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	src := mkPoll(t, s, "AAAAAA", "Original", time.Now().UTC())
	g := &poll.Participant{ID: poll.NewID(), PollID: src.ID, DisplayName: "G", SessionToken: "tok"}
	if err := s.CreateParticipant(ctx, g); err != nil {
		t.Fatalf("CreateParticipant: %v", err)
	}
	nom, err := s.AddNomination(ctx, &poll.Nomination{PollID: src.ID, JellyfinItemID: "m1", Snapshot: poll.ItemSnapshot{Title: "Dune"}}, g.ID)
	if err != nil {
		t.Fatalf("AddNomination: %v", err)
	}
	if err := s.ReplaceVotes(ctx, src.ID, g.ID, []poll.Vote{{NominationID: nom.ID, Value: 3}}); err != nil {
		t.Fatalf("ReplaceVotes: %v", err)
	}
	if err := s.SetPollWinner(ctx, src.ID, nom.ID); err != nil {
		t.Fatalf("SetPollWinner: %v", err)
	}
	src, _ = s.GetPollByID(ctx, src.ID)

	dst := *src
	dst.ID, dst.Code, dst.Title = poll.NewID(), "BBBBBB", "Copy"
	if err := s.ClonePoll(ctx, src.ID, &dst); err != nil {
		t.Fatalf("ClonePoll: %v", err)
	}

	got, err := s.GetPollByCode(ctx, "BBBBBB")
	if err != nil {
		t.Fatalf("GetPollByCode: %v", err)
	}
	parts, _ := s.ListParticipants(ctx, got.ID)
	noms, _ := s.ListNominations(ctx, got.ID)
	votes, _ := s.ListVotes(ctx, got.ID)
	if got.Title != "Copy" || len(parts) != 2 || len(noms) != 1 || len(votes) != 1 {
		t.Fatalf("copy = %q, %d participants, %d noms, %d votes", got.Title, len(parts), len(noms), len(votes))
	}
	if got.HostParticipantID == src.HostParticipantID || got.HostParticipantID != parts[0].ID || got.HostParticipantID != dst.HostParticipantID {
		t.Errorf("host not remapped: %q (parts[0] %q)", got.HostParticipantID, parts[0].ID)
	}
	if noms[0].ID == nom.ID || got.WinnerNominationID != noms[0].ID {
		t.Errorf("winner not remapped: %q, nom %q", got.WinnerNominationID, noms[0].ID)
	}
	if parts[1].SessionToken == "tok" || noms[0].Nominators[0] != parts[1].ID ||
		votes[0].ParticipantID != parts[1].ID || votes[0].NominationID != noms[0].ID || votes[0].Value != 3 {
		t.Errorf("links not remapped: part %+v nom %+v vote %+v", parts[1], noms[0], votes[0])
	}
	// The source is untouched.
	if v, _ := s.ListVotes(ctx, src.ID); len(v) != 1 || v[0].NominationID != nom.ID {
		t.Errorf("source votes changed: %+v", v)
	}
}
