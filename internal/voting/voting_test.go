package voting

import (
	"encoding/json"
	"reflect"
	"testing"
)

func cfg(v any) json.RawMessage { return mustJSON(v) }

func ballot(sel map[string]int) Ballot { return Ballot{Selections: sel} }

func TestRegistryHasBuiltins(t *testing.T) {
	for _, key := range []string{"approval", "ranked", "score"} {
		if _, ok := Get(key); !ok {
			t.Errorf("method %q not registered", key)
		}
	}
	if len(All()) < 3 {
		t.Errorf("expected at least 3 methods, got %d", len(All()))
	}
}

func TestApprovalTally(t *testing.T) {
	all := []string{"A", "B", "C"}
	ballots := []Ballot{
		ballot(map[string]int{"A": 1, "B": 1}),
		ballot(map[string]int{"A": 1, "C": 1}),
		ballot(map[string]int{"A": 1}),
	}
	res, err := Approval{}.Tally(nil, all, ballots)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.WinnerIDs, []string{"A"}) {
		t.Fatalf("winners = %v, want [A]", res.WinnerIDs)
	}
	if res.Ranked[0].NominationID != "A" || res.Ranked[0].Score != 3 {
		t.Fatalf("top = %+v, want A=3", res.Ranked[0])
	}
}

func TestApprovalValidateBallot(t *testing.T) {
	all := []string{"A", "B", "C", "D"}
	// Default config: 3 votes, max 1 per option, self-vote allowed.
	if err := (Approval{}).ValidateBallot(nil, ballot(map[string]int{"A": 1, "B": 1, "C": 1, "D": 1}), all, nil); err == nil {
		t.Error("expected over-budget ballot to be rejected")
	}
	// Per-option cap with cumulative config.
	cumul := cfg(approvalConfig{VotesPerUser: 4, MaxVotesPerOption: 2, AllowSelfVote: true})
	if err := (Approval{}).ValidateBallot(cumul, ballot(map[string]int{"A": 3}), all, nil); err == nil {
		t.Error("expected per-option cap to be enforced")
	}
	if err := (Approval{}).ValidateBallot(cumul, ballot(map[string]int{"A": 2, "B": 2}), all, nil); err != nil {
		t.Errorf("valid cumulative ballot rejected: %v", err)
	}
	// Self-vote disallowed.
	noSelf := cfg(approvalConfig{VotesPerUser: 3, MaxVotesPerOption: 1, AllowSelfVote: false})
	if err := (Approval{}).ValidateBallot(noSelf, ballot(map[string]int{"A": 1}), all, []string{"A"}); err == nil {
		t.Error("expected self-vote to be rejected")
	}
	// Unlimited picks: any number of titles, but one vote each.
	unl := cfg(approvalConfig{VotesPerUser: -1, MaxVotesPerOption: 0, AllowSelfVote: true})
	if err := (Approval{}).ValidateBallot(unl, ballot(map[string]int{"A": 1, "B": 1, "C": 1, "D": 1}), all, nil); err != nil {
		t.Errorf("unlimited picks rejected: %v", err)
	}
	if err := (Approval{}).ValidateBallot(unl, ballot(map[string]int{"A": 2}), all, nil); err == nil {
		t.Error("expected stacking to be rejected with unlimited picks")
	}
}

func intp(n int) *int { return &n }

func TestSelfVoteLimits(t *testing.T) {
	all := []string{"A", "B", "C"}
	own := []string{"A"} // the voter nominated A

	// Approval: cap self-votes at 1 (cumulative budget so >1 on one option is
	// otherwise legal).
	apr := cfg(approvalConfig{VotesPerUser: 5, MaxVotesPerOption: 5, AllowSelfVote: true, MaxSelfVotes: intp(1)})
	if err := (Approval{}).ValidateBallot(apr, ballot(map[string]int{"A": 1}), all, own); err != nil {
		t.Errorf("1 self-vote within cap rejected: %v", err)
	}
	if err := (Approval{}).ValidateBallot(apr, ballot(map[string]int{"A": 2}), all, own); err == nil {
		t.Error("expected 2 self-votes to exceed cap of 1")
	}
	// max_self_votes overrides a legacy allow_self_vote=false (lets you self-vote
	// up to the cap).
	override := cfg(approvalConfig{VotesPerUser: 5, MaxVotesPerOption: 5, AllowSelfVote: false, MaxSelfVotes: intp(2)})
	if err := (Approval{}).ValidateBallot(override, ballot(map[string]int{"A": 2}), all, own); err != nil {
		t.Errorf("max_self_votes should override allow_self_vote=false: %v", err)
	}
	// max_self_votes = 0 means none, even if allow_self_vote is true.
	none := cfg(approvalConfig{VotesPerUser: 5, MaxVotesPerOption: 5, AllowSelfVote: true, MaxSelfVotes: intp(0)})
	if err := (Approval{}).ValidateBallot(none, ballot(map[string]int{"A": 1}), all, own); err == nil {
		t.Error("expected max_self_votes=0 to forbid any self-vote")
	}

	// Ranked: cap on how many of your own you may rank.
	rnk := cfg(rankedConfig{MaxSelfVotes: intp(1)})
	ownTwo := []string{"A", "B"}
	if err := (Ranked{}).ValidateBallot(rnk, ballot(map[string]int{"A": 1, "B": 2}), all, ownTwo); err == nil {
		t.Error("expected ranking 2 own nominations to exceed cap of 1")
	}
	if err := (Ranked{}).ValidateBallot(rnk, ballot(map[string]int{"A": 1, "C": 2}), all, ownTwo); err != nil {
		t.Errorf("ranking 1 own nomination within cap rejected: %v", err)
	}

	// Score: cap on how many of your own you may give a positive score.
	scr := cfg(scoreConfig{MaxScore: 5, AllowSelfVote: true, Aggregate: "total", MaxSelfVotes: intp(1)})
	if err := (Score{}).ValidateBallot(scr, ballot(map[string]int{"A": 5, "B": 5}), all, ownTwo); err == nil {
		t.Error("expected scoring 2 own nominations to exceed cap of 1")
	}
	if err := (Score{}).ValidateBallot(scr, ballot(map[string]int{"A": 5, "B": 0}), all, ownTwo); err != nil {
		t.Errorf("scoring 1 own (other at 0) within cap rejected: %v", err)
	}
}

func TestScoreTallyTotalVsAverage(t *testing.T) {
	all := []string{"A", "B", "C"}
	ballots := []Ballot{
		ballot(map[string]int{"A": 5, "B": 3}),
		ballot(map[string]int{"A": 4, "C": 5}),
	}
	total, _ := Score{}.Tally(cfg(scoreConfig{MaxScore: 5, AllowSelfVote: true, Aggregate: "total"}), all, ballots)
	if !reflect.DeepEqual(total.WinnerIDs, []string{"A"}) {
		t.Fatalf("total winners = %v, want [A]", total.WinnerIDs)
	}
	avg, _ := Score{}.Tally(cfg(scoreConfig{MaxScore: 5, AllowSelfVote: true, Aggregate: "average"}), all, ballots)
	if !reflect.DeepEqual(avg.WinnerIDs, []string{"C"}) {
		t.Fatalf("average winners = %v, want [C] (5.0 beats A's 4.5)", avg.WinnerIDs)
	}
}

func TestScoreValidateBallot(t *testing.T) {
	all := []string{"A", "B"}
	c := cfg(scoreConfig{MaxScore: 5, AllowSelfVote: false, Aggregate: "total"})
	if err := (Score{}).ValidateBallot(c, ballot(map[string]int{"A": 6}), all, nil); err == nil {
		t.Error("expected out-of-range score to be rejected")
	}
	if err := (Score{}).ValidateBallot(c, ballot(map[string]int{"A": 5}), all, []string{"A"}); err == nil {
		t.Error("expected self-score to be rejected")
	}
}

func TestRankedIRVRedistribution(t *testing.T) {
	// A leads first-round but B wins after C is eliminated and redistributed.
	all := []string{"A", "B", "C"}
	var ballots []Ballot
	for i := 0; i < 4; i++ {
		ballots = append(ballots, ballot(map[string]int{"A": 1, "B": 2, "C": 3}))
	}
	for i := 0; i < 3; i++ {
		ballots = append(ballots, ballot(map[string]int{"B": 1, "A": 2, "C": 3}))
	}
	for i := 0; i < 2; i++ {
		ballots = append(ballots, ballot(map[string]int{"C": 1, "B": 2, "A": 3}))
	}
	res, err := Ranked{}.Tally(nil, all, ballots)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.WinnerIDs, []string{"B"}) {
		t.Fatalf("winners = %v, want [B]", res.WinnerIDs)
	}
	if len(res.Rounds) != 2 {
		t.Fatalf("rounds = %d, want 2", len(res.Rounds))
	}
	if len(res.Rounds[0].Eliminated) != 1 || res.Rounds[0].Eliminated[0] != "C" {
		t.Fatalf("round 1 should eliminate C, got %v", res.Rounds[0].Eliminated)
	}
}

func TestRankedFirstRoundMajority(t *testing.T) {
	all := []string{"A", "B"}
	var ballots []Ballot
	for i := 0; i < 3; i++ {
		ballots = append(ballots, ballot(map[string]int{"A": 1, "B": 2}))
	}
	ballots = append(ballots, ballot(map[string]int{"B": 1, "A": 2}))
	res, _ := Ranked{}.Tally(nil, all, ballots)
	if !reflect.DeepEqual(res.WinnerIDs, []string{"A"}) {
		t.Fatalf("winners = %v, want [A]", res.WinnerIDs)
	}
	if len(res.Rounds) != 1 {
		t.Fatalf("rounds = %d, want 1", len(res.Rounds))
	}
}

func TestRankedTie(t *testing.T) {
	all := []string{"A", "B"}
	ballots := []Ballot{
		ballot(map[string]int{"A": 1}),
		ballot(map[string]int{"B": 1}),
	}
	res, _ := Ranked{}.Tally(nil, all, ballots)
	if len(res.WinnerIDs) != 2 {
		t.Fatalf("winners = %v, want a 2-way tie", res.WinnerIDs)
	}
}

func TestRankedValidateBallot(t *testing.T) {
	all := []string{"A", "B", "C"}
	if err := (Ranked{}).ValidateBallot(nil, ballot(map[string]int{"A": 1, "B": 1}), all, nil); err == nil {
		t.Error("expected duplicate rank to be rejected")
	}
	if err := (Ranked{}).ValidateBallot(nil, ballot(map[string]int{"A": 1, "Z": 2}), all, nil); err == nil {
		t.Error("expected unknown nomination to be rejected")
	}
	noSelf := cfg(rankedConfig{AllowSelfVote: false})
	if err := (Ranked{}).ValidateBallot(noSelf, ballot(map[string]int{"A": 1}), all, []string{"A"}); err == nil {
		t.Error("expected self-rank to be rejected")
	}
}

func TestDefaultConfigsValid(t *testing.T) {
	for _, m := range All() {
		if err := m.ValidateConfig(m.DefaultConfig()); err != nil {
			t.Errorf("%s default config invalid: %v", m.Key(), err)
		}
	}
}

func TestRankedAllWayTieSplitByLowerPreferences(t *testing.T) {
	// Every first choice is different, but most voters rank B second.
	all := []string{"A", "B", "C", "D"}
	ballots := []Ballot{
		ballot(map[string]int{"A": 1, "B": 2, "C": 3}),
		ballot(map[string]int{"B": 1, "A": 2}),
		ballot(map[string]int{"C": 1, "B": 2}),
		ballot(map[string]int{"D": 1, "B": 2}),
	}
	res, _ := Ranked{}.Tally(nil, all, ballots)
	if !reflect.DeepEqual(res.WinnerIDs, []string{"B"}) {
		t.Fatalf("winners = %v, want [B]", res.WinnerIDs)
	}
	if res.Ranked[0].NominationID != "B" {
		t.Fatalf("top = %+v, want B", res.Ranked[0])
	}
}

func TestRankedBatchEliminatesUnranked(t *testing.T) {
	// Nobody ranks C, D or E: they go out together in round 1. A and B are
	// then level on every preference, so that's a genuine tie.
	all := []string{"A", "B", "C", "D", "E"}
	ballots := []Ballot{
		ballot(map[string]int{"A": 1, "B": 2}),
		ballot(map[string]int{"A": 1}),
		ballot(map[string]int{"B": 1}),
		ballot(map[string]int{"B": 1, "A": 2}),
	}
	res, _ := Ranked{}.Tally(nil, all, ballots)
	if !reflect.DeepEqual(res.Rounds[0].Eliminated, []string{"C", "D", "E"}) {
		t.Fatalf("round 1 eliminated %v, want [C D E]", res.Rounds[0].Eliminated)
	}
	if !reflect.DeepEqual(res.WinnerIDs, []string{"A", "B"}) {
		t.Fatalf("winners = %v, want an A/B tie", res.WinnerIDs)
	}
}

func TestVetoKnocksOutFavoriteInEveryMethod(t *testing.T) {
	all := []string{"A", "B"}
	cases := map[Method]map[string]int{
		Approval{}: {"A": 1},
		Score{}:    {"A": 5, "B": 1},
		Ranked{}:   {"A": 1, "B": 2},
	}
	for m, fan := range cases {
		ballots := []Ballot{ballot(fan), ballot(fan), ballot(map[string]int{"A": Veto, "B": 1})}
		res, err := Tally(m, nil, all, ballots)
		if err != nil {
			t.Fatalf("%s: %v", m.Key(), err)
		}
		if !reflect.DeepEqual(res.WinnerIDs, []string{"B"}) {
			t.Errorf("%s: winners = %v, want [B]", m.Key(), res.WinnerIDs)
		}
		if last := res.Ranked[len(res.Ranked)-1]; last.NominationID != "A" || !last.Vetoed {
			t.Errorf("%s: last = %+v, want vetoed A", m.Key(), last)
		}
	}
}

func TestVetoBallotLimit(t *testing.T) {
	all := []string{"A", "B", "C"}
	two := ballot(map[string]int{"A": Veto, "B": Veto, "C": 1})
	if err := ValidateBallot(Approval{}, nil, two, all, nil); err == nil {
		t.Error("vetoes accepted with vetoes off")
	}
	if err := ValidateBallot(Approval{}, cfg(map[string]int{"votes_per_user": 3, "max_vetoes": 1}), two, all, nil); err == nil {
		t.Error("2 vetoes accepted with max_vetoes=1")
	}
	if err := ValidateBallot(Ranked{}, cfg(map[string]int{"max_vetoes": 2}), two, all, nil); err != nil {
		t.Errorf("2 vetoes rejected with max_vetoes=2: %v", err)
	}
	if err := ValidateBallot(Ranked{}, cfg(map[string]int{"max_vetoes": -1}), two, all, nil); err != nil {
		t.Errorf("2 vetoes rejected with unlimited vetoes: %v", err)
	}
}
