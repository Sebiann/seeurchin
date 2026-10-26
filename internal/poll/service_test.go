package poll_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/enderu/seeurchin/internal/poll"
	"github.com/enderu/seeurchin/internal/store"
)

// fakeResolver returns a movie for any item id, so nominations validate without
// a real Jellyfin.
type fakeResolver struct{}

func (fakeResolver) GetItem(_ context.Context, id string) (*poll.ResolvedItem, error) {
	return &poll.ResolvedItem{ID: id, Title: "Title " + id, Type: "Movie", Year: 2020}, nil
}

func newSvc(t *testing.T) (*poll.Service, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return poll.NewService(st, fakeResolver{}, 0), st
}

func baseInput() poll.CreatePollInput {
	return poll.CreatePollInput{
		Title:        "Movie Night",
		HostName:     "Alice",
		LibraryScope: poll.ScopeBoth,
		VotingMethod: "approval",
		AllowGuests:  true,
	}
}

func nominate(t *testing.T, svc *poll.Service, p *poll.Poll, by *poll.Participant, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if _, err := svc.SubmitNomination(context.Background(), p, by, id); err != nil {
			t.Fatalf("nominate %q: %v", id, err)
		}
	}
}

// near asserts that a duration in seconds is within tol of want.
func near(t *testing.T, label string, gotSec, want, tol int) {
	t.Helper()
	if gotSec < want-tol || gotSec > want+tol {
		t.Fatalf("%s = %ds, want ~%ds", label, gotSec, want)
	}
}

func TestQuickTimerLifecycle(t *testing.T) {
	svc, st := newSvc(t)
	ctx := context.Background()
	in := baseInput()
	in.DeadlineMode = poll.DeadlineQuick
	in.Round1DurationSec = 60
	in.Round2DurationSec = 30

	p, host, err := svc.CreatePoll(ctx, in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if p.DeadlineMode != poll.DeadlineQuick || p.Round1DurationSec != 60 {
		t.Fatalf("config not stored: %+v", p)
	}
	if p.Round1ClosesAt != nil {
		t.Fatal("round 1 should be armed (ClosesAt nil) until the host starts it")
	}

	// Start → running.
	if _, err := svc.StartTimer(ctx, p, host); err != nil {
		t.Fatalf("start: %v", err)
	}
	got, _ := st.GetPollByID(ctx, p.ID)
	if got.Round1ClosesAt == nil {
		t.Fatal("start should stamp round1_closes_at")
	}
	near(t, "remaining after start", int(time.Until(*got.Round1ClosesAt).Seconds()), 60, 3)

	// Pause → remembers remaining, clears ClosesAt.
	if _, err := svc.PauseTimer(ctx, got, host); err != nil {
		t.Fatalf("pause: %v", err)
	}
	got, _ = st.GetPollByID(ctx, p.ID)
	if got.Round1ClosesAt != nil {
		t.Fatal("pause should clear round1_closes_at")
	}
	near(t, "paused remaining", got.TimerPausedSec, 60, 3)

	// Extend while paused.
	if _, err := svc.ExtendTimer(ctx, got, host, 30); err != nil {
		t.Fatalf("extend: %v", err)
	}
	got, _ = st.GetPollByID(ctx, p.ID)
	near(t, "paused remaining after +30", got.TimerPausedSec, 90, 3)

	// Resume → re-stamps from remaining.
	if _, err := svc.StartTimer(ctx, got, host); err != nil {
		t.Fatalf("resume: %v", err)
	}
	got, _ = st.GetPollByID(ctx, p.ID)
	if got.TimerPausedSec != 0 || got.Round1ClosesAt == nil {
		t.Fatalf("resume should clear paused + restamp: %+v", got)
	}
	near(t, "remaining after resume", int(time.Until(*got.Round1ClosesAt).Seconds()), 90, 3)
}

func TestSweepAdvancesQuickRound1ToRound2(t *testing.T) {
	svc, st := newSvc(t)
	ctx := context.Background()
	in := baseInput()
	in.DeadlineMode = poll.DeadlineQuick
	in.Round1DurationSec = 10
	in.Round2DurationSec = 20

	p, host, err := svc.CreatePoll(ctx, in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	nominate(t, svc, p, host, "a", "b")
	if _, err := svc.StartTimer(ctx, p, host); err != nil {
		t.Fatalf("start: %v", err)
	}

	changed, err := svc.SweepDueTimers(ctx, time.Now().Add(11*time.Second))
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(changed) != 1 {
		t.Fatalf("expected 1 changed poll, got %d", len(changed))
	}
	got, _ := st.GetPollByID(ctx, p.ID)
	if got.Status != poll.StatusRound2 {
		t.Fatalf("status = %q, want round2", got.Status)
	}
	if got.Round2ClosesAt == nil {
		t.Fatal("entering round 2 should stamp round2_closes_at (quick)")
	}
	if got.TimerPausedSec != 0 {
		t.Fatalf("paused should reset on advance, got %d", got.TimerPausedSec)
	}
}

func TestSweepLoneNomineeWins(t *testing.T) {
	svc, st := newSvc(t)
	ctx := context.Background()
	in := baseInput()
	in.DeadlineMode = poll.DeadlineQuick
	in.Round1DurationSec = 10

	p, host, err := svc.CreatePoll(ctx, in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	nominate(t, svc, p, host, "solo")
	if _, err := svc.StartTimer(ctx, p, host); err != nil {
		t.Fatalf("start: %v", err)
	}

	if _, err := svc.SweepDueTimers(ctx, time.Now().Add(11*time.Second)); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	got, _ := st.GetPollByID(ctx, p.ID)
	if got.Status != poll.StatusClosed {
		t.Fatalf("status = %q, want closed", got.Status)
	}
	noms, _ := st.ListNominations(ctx, p.ID)
	if len(noms) != 1 || got.WinnerNominationID != noms[0].ID {
		t.Fatalf("lone nominee not crowned: winner=%q noms=%d", got.WinnerNominationID, len(noms))
	}
}

func TestSweepZeroNominationsClosesEmpty(t *testing.T) {
	svc, st := newSvc(t)
	ctx := context.Background()
	in := baseInput()
	in.DeadlineMode = poll.DeadlineQuick
	in.Round1DurationSec = 10

	p, host, err := svc.CreatePoll(ctx, in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.StartTimer(ctx, p, host); err != nil {
		t.Fatalf("start: %v", err)
	}

	if _, err := svc.SweepDueTimers(ctx, time.Now().Add(11*time.Second)); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	got, _ := st.GetPollByID(ctx, p.ID)
	if got.Status != poll.StatusClosed {
		t.Fatalf("status = %q, want closed", got.Status)
	}
	if got.WinnerNominationID != "" {
		t.Fatalf("empty poll should have no winner, got %q", got.WinnerNominationID)
	}
}

func TestScheduledValidation(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	now := time.Now()

	past := now.Add(-time.Hour)
	in := baseInput()
	in.DeadlineMode = poll.DeadlineScheduled
	in.Round1ClosesAt = &past
	if _, _, err := svc.CreatePoll(ctx, in); err == nil {
		t.Fatal("expected error for past close time")
	}

	r1 := now.Add(2 * time.Hour)
	r2 := now.Add(time.Hour) // before r1 — invalid
	in = baseInput()
	in.DeadlineMode = poll.DeadlineScheduled
	in.Round1ClosesAt = &r1
	in.Round2ClosesAt = &r2
	if _, _, err := svc.CreatePoll(ctx, in); err == nil {
		t.Fatal("expected error when nominations close after voting")
	}

	r1 = now.Add(time.Hour)
	r2 = now.Add(2 * time.Hour)
	in = baseInput()
	in.DeadlineMode = poll.DeadlineScheduled
	in.Round1ClosesAt = &r1
	in.Round2ClosesAt = &r2
	p, _, err := svc.CreatePoll(ctx, in)
	if err != nil {
		t.Fatalf("valid scheduled poll rejected: %v", err)
	}
	if p.Round1ClosesAt == nil || p.Round2ClosesAt == nil {
		t.Fatal("scheduled close times not stored")
	}
}

func TestQuickRequiresADuration(t *testing.T) {
	svc, _ := newSvc(t)
	in := baseInput()
	in.DeadlineMode = poll.DeadlineQuick // no durations
	if _, _, err := svc.CreatePoll(context.Background(), in); err == nil {
		t.Fatal("quick mode with no durations should error")
	}
}

func TestOnlyHostControlsTimer(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	in := baseInput()
	in.DeadlineMode = poll.DeadlineQuick
	in.Round1DurationSec = 30

	p, _, err := svc.CreatePoll(ctx, in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	guest, err := svc.JoinAsGuest(ctx, p, "Bob")
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	if _, err := svc.StartTimer(ctx, p, guest); err == nil {
		t.Fatal("a guest should not be able to start the timer")
	}
}

func TestManualAdvanceUntimedStillNeedsTwo(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	p, host, err := svc.CreatePoll(ctx, baseInput()) // DeadlineNone
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	nominate(t, svc, p, host, "only-one")
	if _, err := svc.Advance(ctx, p, host); err == nil {
		t.Fatal("untimed poll should still require 2 nominations to advance manually")
	}
}

func TestEndNowOnTimedPollCrownsLoneNominee(t *testing.T) {
	svc, st := newSvc(t)
	ctx := context.Background()
	in := baseInput()
	in.DeadlineMode = poll.DeadlineQuick
	in.Round1DurationSec = 60

	p, host, err := svc.CreatePoll(ctx, in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	nominate(t, svc, p, host, "solo")
	if _, err := svc.StartTimer(ctx, p, host); err != nil {
		t.Fatalf("start: %v", err)
	}
	// "End now" is a host-driven Advance; on a timed poll it resolves gracefully.
	if _, err := svc.Advance(ctx, p, host); err != nil {
		t.Fatalf("advance: %v", err)
	}
	got, _ := st.GetPollByID(ctx, p.ID)
	if got.Status != poll.StatusClosed || got.WinnerNominationID == "" {
		t.Fatalf("end-now should crown the lone nominee: %+v", got)
	}
}

// tiedClosedPoll builds a closed approval poll where host and guest each
// approve a different title, so the tally is a two-way tie.
func tiedClosedPoll(t *testing.T) (*poll.Service, *poll.Poll, *poll.Participant, *poll.Participant) {
	t.Helper()
	svc, st := newSvc(t)
	ctx := context.Background()
	p, host, err := svc.CreatePoll(ctx, baseInput())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	guest, err := svc.JoinAsGuest(ctx, p, "Bob")
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	nominate(t, svc, p, host, "a", "b")
	if p, err = svc.Advance(ctx, p, host); err != nil {
		t.Fatalf("advance to round 2: %v", err)
	}
	noms, _ := st.ListNominations(ctx, p.ID)
	if err := svc.CastVotes(ctx, p, host, map[string]int{noms[0].ID: 1}); err != nil {
		t.Fatalf("host vote: %v", err)
	}
	if err := svc.CastVotes(ctx, p, guest, map[string]int{noms[1].ID: 1}); err != nil {
		t.Fatalf("guest vote: %v", err)
	}
	if p, err = svc.Advance(ctx, p, host); err != nil {
		t.Fatalf("close: %v", err)
	}
	return svc, p, host, guest
}

func TestBreakTieFreezesOneOfTheTiedWinners(t *testing.T) {
	svc, p, host, guest := tiedClosedPoll(t)
	ctx := context.Background()

	before, _, err := svc.Results(ctx, p)
	if err != nil || len(before.WinnerIDs) != 2 {
		t.Fatalf("want a 2-way tie before breaking, got %v (err %v)", before.WinnerIDs, err)
	}
	if _, err := svc.BreakTie(ctx, p, guest); err == nil {
		t.Fatal("a non-host must not be able to break the tie")
	}
	p, err = svc.BreakTie(ctx, p, host)
	if err != nil {
		t.Fatalf("break tie: %v", err)
	}
	after, _, err := svc.Results(ctx, p)
	if err != nil {
		t.Fatalf("results: %v", err)
	}
	if len(after.WinnerIDs) != 1 || after.WinnerIDs[0] != p.WinnerNominationID {
		t.Fatalf("want the single frozen winner, got %v", after.WinnerIDs)
	}
	if len(after.TiedIDs) != 2 {
		t.Fatalf("want the 2 co-winners kept in TiedIDs, got %v", after.TiedIDs)
	}
	if _, err := svc.BreakTie(ctx, p, host); err == nil {
		t.Fatal("breaking an already-broken tie should conflict")
	}
}

func TestBreakTieWithoutTieConflicts(t *testing.T) {
	svc, st := newSvc(t)
	ctx := context.Background()
	p, host, _ := svc.CreatePoll(ctx, baseInput())
	nominate(t, svc, p, host, "a", "b")
	p, _ = svc.Advance(ctx, p, host)
	noms, _ := st.ListNominations(ctx, p.ID)
	if err := svc.CastVotes(ctx, p, host, map[string]int{noms[0].ID: 1}); err != nil {
		t.Fatalf("vote: %v", err)
	}
	p, _ = svc.Advance(ctx, p, host)
	if _, err := svc.BreakTie(ctx, p, host); err == nil {
		t.Fatal("a clear winner has no tie to break")
	}
}

func TestMergeDuplicateNames(t *testing.T) {
	svc, st := newSvc(t)
	ctx := context.Background()
	p, host, err := svc.CreatePoll(ctx, baseInput())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var bobs []*poll.Participant
	for range 3 {
		b, err := svc.JoinAsGuest(ctx, p, "Bob")
		if err != nil {
			t.Fatalf("join: %v", err)
		}
		bobs = append(bobs, b)
	}
	if _, err := svc.JoinAsGuest(ctx, p, "bob"); err != nil { // different capitalization: not a duplicate
		t.Fatalf("join: %v", err)
	}
	nominate(t, svc, p, bobs[0], "a", "b")
	nominate(t, svc, p, bobs[1], "b", "c")
	if p, err = svc.Advance(ctx, p, host); err != nil {
		t.Fatalf("advance: %v", err)
	}
	noms, _ := st.ListNominations(ctx, p.ID)
	if err := svc.CastVotes(ctx, p, bobs[0], map[string]int{noms[0].ID: 1}); err != nil {
		t.Fatalf("vote: %v", err)
	}
	if err := svc.CastVotes(ctx, p, bobs[2], map[string]int{noms[2].ID: 1}); err != nil {
		t.Fatalf("vote: %v", err)
	}

	if err := svc.MergeDuplicateNames(ctx, p, bobs[0]); err == nil {
		t.Fatal("a guest should not be able to merge")
	}
	if err := svc.MergeDuplicateNames(ctx, p, host); err != nil {
		t.Fatalf("merge: %v", err)
	}

	parts, _ := st.ListParticipants(ctx, p.ID)
	if len(parts) != 3 {
		t.Fatalf("participants = %d, want 3 (Alice, Bob, bob)", len(parts))
	}
	keep := bobs[2] // cast the newest ballot
	for _, pt := range parts {
		if pt.DisplayName == "Bob" && pt.ID != keep.ID {
			t.Fatalf("kept %s, want the newest voter %s", pt.ID, keep.ID)
		}
	}
	noms, _ = st.ListNominations(ctx, p.ID)
	for _, n := range noms {
		if len(n.Nominators) != 1 || n.Nominators[0] != keep.ID {
			t.Fatalf("nomination %s nominators = %v, want only %s", n.JellyfinItemID, n.Nominators, keep.ID)
		}
	}
	votes, _ := st.ListVotes(ctx, p.ID)
	if len(votes) != 1 || votes[0].ParticipantID != keep.ID || votes[0].NominationID != noms[2].ID {
		t.Fatalf("votes = %+v, want only the newest ballot (c) under %s", votes, keep.ID)
	}
}

func TestLinkJellyfin(t *testing.T) {
	svc, st := newSvc(t)
	ctx := context.Background()
	p, host, err := svc.CreatePoll(ctx, baseInput())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	phone, _ := svc.JoinAsGuest(ctx, p, "Cookie")
	laptop, _ := svc.JoinAsGuest(ctx, p, "Cookie")
	nominate(t, svc, p, phone, "a")
	nominate(t, svc, p, laptop, "b")

	got, err := svc.LinkJellyfin(ctx, p, phone, "jf-cookie", false)
	if err != nil || got.ID != phone.ID {
		t.Fatalf("first link = %v, %v; want phone", got, err)
	}
	// The same account linked from a second entry folds that entry into the first.
	got, err = svc.LinkJellyfin(ctx, p, laptop, "jf-cookie", false)
	if err != nil || got.ID != phone.ID {
		t.Fatalf("second link = %v, %v; want merge into phone", got, err)
	}
	if _, err := st.GetParticipant(ctx, laptop.ID); err == nil {
		t.Fatal("laptop entry should be gone after merging")
	}
	noms, _ := st.ListNominations(ctx, p.ID)
	for _, n := range noms {
		if len(n.Nominators) != 1 || n.Nominators[0] != phone.ID {
			t.Fatalf("nomination %s nominators = %v, want phone", n.JellyfinItemID, n.Nominators)
		}
	}

	// Self-link can't move an already-linked entry; the host can.
	phone, _ = st.GetParticipant(ctx, phone.ID)
	if _, err := svc.LinkJellyfin(ctx, p, phone, "jf-other", false); err == nil {
		t.Fatal("self relink to another account should fail")
	}
	guest, _ := svc.JoinAsGuest(ctx, p, "Vee")
	if err := svc.HostLinkJellyfin(ctx, p, guest, phone.ID, "jf-other"); err == nil {
		t.Fatal("a non-host should not be able to link people")
	}
	if err := svc.HostLinkJellyfin(ctx, p, host, phone.ID, "jf-other"); err != nil {
		t.Fatalf("host relink: %v", err)
	}
	if pt, _ := st.GetParticipant(ctx, phone.ID); pt.JellyfinUserID != "jf-other" {
		t.Fatalf("after host relink id = %q, want jf-other", pt.JellyfinUserID)
	}
	// Host linking Vee to an account already linked here merges Vee into that entry.
	if err := svc.HostLinkJellyfin(ctx, p, host, guest.ID, "jf-other"); err != nil {
		t.Fatalf("host link merge: %v", err)
	}
	if _, err := st.GetParticipant(ctx, guest.ID); err == nil {
		t.Fatal("Vee should have been merged into the linked entry")
	}
}

func TestMergeDuplicateNamesSkipsDifferentAccounts(t *testing.T) {
	svc, st := newSvc(t)
	ctx := context.Background()
	p, host, _ := svc.CreatePoll(ctx, baseInput())
	a, _ := svc.JoinAsGuest(ctx, p, "Steven")
	b, _ := svc.JoinAsGuest(ctx, p, "Steven")
	if _, err := svc.LinkJellyfin(ctx, p, a, "jf-1", false); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.LinkJellyfin(ctx, p, b, "jf-2", false); err != nil {
		t.Fatal(err)
	}
	if err := svc.MergeDuplicateNames(ctx, p, host); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if parts, _ := st.ListParticipants(ctx, p.ID); len(parts) != 3 {
		t.Fatalf("participants = %d, want 3: two linked Stevens are different people", len(parts))
	}
}

func TestMergePeople(t *testing.T) {
	svc, st := newSvc(t)
	ctx := context.Background()
	p, host, _ := svc.CreatePoll(ctx, baseInput())
	seb, _ := svc.JoinAsGuest(ctx, p, "Seb")
	sebastian, _ := svc.JoinAsGuest(ctx, p, "Sebastian")
	other, _ := svc.JoinAsGuest(ctx, p, "Vee")
	nominate(t, svc, p, seb, "a")
	nominate(t, svc, p, sebastian, "b")
	if _, err := svc.LinkJellyfin(ctx, p, seb, "jf-seb", false); err != nil {
		t.Fatal(err)
	}

	if err := svc.MergePeople(ctx, p, seb, seb.ID, sebastian.ID); err == nil {
		t.Fatal("a guest should not be able to merge")
	}
	if err := svc.MergePeople(ctx, p, host, seb.ID, seb.ID); err == nil {
		t.Fatal("merging someone into themselves should fail")
	}
	if err := svc.MergePeople(ctx, p, host, seb.ID, sebastian.ID); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if _, err := st.GetParticipant(ctx, seb.ID); err == nil {
		t.Fatal("Seb should be gone after merging")
	}
	kept, _ := st.GetParticipant(ctx, sebastian.ID)
	if kept.DisplayName != "Sebastian" || kept.JellyfinUserID != "jf-seb" {
		t.Fatalf("kept = %q/%q, want Sebastian carrying jf-seb", kept.DisplayName, kept.JellyfinUserID)
	}
	noms, _ := st.ListNominations(ctx, p.ID)
	for _, n := range noms {
		if len(n.Nominators) != 1 || n.Nominators[0] != sebastian.ID {
			t.Fatalf("nomination %s nominators = %v, want Sebastian", n.JellyfinItemID, n.Nominators)
		}
	}

	// Merging the host into someone keeps the host entry.
	if err := svc.MergePeople(ctx, p, host, host.ID, other.ID); err != nil {
		t.Fatalf("merge host: %v", err)
	}
	if _, err := st.GetParticipant(ctx, host.ID); err != nil {
		t.Fatalf("host entry should survive: %v", err)
	}

	// Two different Jellyfin accounts are different people.
	if _, err := svc.LinkJellyfin(ctx, p, kept, "jf-seb", false); err != nil {
		t.Fatal(err)
	}
	third, _ := svc.JoinAsGuest(ctx, p, "Kai")
	if _, err := svc.LinkJellyfin(ctx, p, third, "jf-kai", false); err != nil {
		t.Fatal(err)
	}
	if err := svc.MergePeople(ctx, p, host, third.ID, kept.ID); err == nil {
		t.Fatal("entries linked to different accounts should not merge")
	}
}

func TestAdvanceIgnoresVoters(t *testing.T) {
	svc, st := newSvc(t)
	ctx := context.Background()
	p, host, _ := svc.CreatePoll(ctx, baseInput())
	nominate(t, svc, p, host, "a", "b")
	p, _ = svc.Advance(ctx, p, host)
	guest, _ := svc.JoinAsGuest(ctx, p, "Troll")
	noms, _ := st.ListNominations(ctx, p.ID)
	_ = svc.CastVotes(ctx, p, host, map[string]int{noms[0].ID: 1})
	_ = svc.CastVotes(ctx, p, guest, map[string]int{noms[1].ID: 1})
	p, err := svc.Advance(ctx, p, host, guest.ID)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	res, _, _ := svc.Results(ctx, p)
	if len(res.WinnerIDs) != 1 || res.WinnerIDs[0] != noms[0].ID {
		t.Fatalf("winners = %v, want only the host's pick", res.WinnerIDs)
	}
}

func TestUpdateSettingsKeepsCastBallotsValid(t *testing.T) {
	svc, st := newSvc(t)
	ctx := context.Background()
	p, host, err := svc.CreatePoll(ctx, baseInput())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	guest, err := svc.JoinAsGuest(ctx, p, "Bob")
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	nominate(t, svc, p, host, "a", "b", "c")
	if p, err = svc.Advance(ctx, p, host); err != nil {
		t.Fatalf("advance to round 2: %v", err)
	}
	noms, _ := st.ListNominations(ctx, p.ID)
	if err := svc.CastVotes(ctx, p, guest, map[string]int{noms[0].ID: 1, noms[1].ID: 1}); err != nil {
		t.Fatalf("guest vote: %v", err)
	}

	if err := svc.UpdateSettings(ctx, p, guest, []byte(`{"votes_per_user":5}`), false); err == nil {
		t.Error("a guest changed settings")
	}
	// Bob used 2 picks: dropping to 1 would undo his ballot.
	if err := svc.UpdateSettings(ctx, p, host, []byte(`{"votes_per_user":1,"max_votes_per_option":1}`), false); err == nil {
		t.Error("tightening below a cast ballot was accepted")
	}
	if err := svc.UpdateSettings(ctx, p, host, []byte(`{"votes_per_user":-1,"max_vetoes":2}`), true); err != nil {
		t.Fatalf("loosening rejected: %v", err)
	}
	got, _ := st.GetPollByID(ctx, p.ID)
	if !got.ResultsLive || string(got.VotingConfig) != `{"votes_per_user":-1,"max_vetoes":2}` {
		t.Fatalf("settings not saved: live=%v cfg=%s", got.ResultsLive, got.VotingConfig)
	}
}
