// Package voting implements seeurchin's pluggable round-2 voting engine.
//
// Each voting method satisfies the Method interface and is registered in a
// global registry, so adding a new (or custom) method is a single Register
// call. A poll stores its chosen method key plus an opaque JSON config blob
// that the method interprets.
package voting

import (
	"encoding/json"
	"fmt"
	"sort"
)

// Ballot is one participant's submission, interpreted per method. Selections
// maps a nomination ID to an integer whose meaning depends on the method:
//
//	approval: votes placed on that nomination (>= 1)
//	score:    the score given to that nomination (0..MaxScore)
//	ranked:   the 1-based rank position (1 = top choice)
//
// In every method the value Veto marks a "definitely not" vote instead.
type Ballot struct {
	ParticipantID string
	Selections    map[string]int
}

// Result is the tally outcome for a single nomination.
type Result struct {
	NominationID string  `json:"nomination_id"`
	Score        float64 `json:"score"`
	Detail       string  `json:"detail,omitempty"`
	Vetoed       bool    `json:"vetoed,omitempty"`
}

// RoundResult records one elimination round (ranked-choice only).
type RoundResult struct {
	Round      int                `json:"round"`
	Counts     map[string]float64 `json:"counts"`
	Eliminated []string           `json:"eliminated,omitempty"`
}

// Results is a complete tally, with Ranked sorted best-first and WinnerIDs
// holding one or more IDs (more than one only on a tie).
type Results struct {
	Method    string        `json:"method"`
	Ranked    []Result      `json:"ranked"`
	WinnerIDs []string      `json:"winner_ids"`
	Rounds    []RoundResult `json:"rounds,omitempty"`
	// TiedIDs lists the tallied co-winners when the host broke a tie at random
	// (WinnerIDs then holds just the drawn one). Set by the poll service, never
	// by a Method's Tally.
	TiedIDs []string `json:"tied_ids,omitempty"`
}

// Method is a pluggable voting strategy.
type Method interface {
	// Key is the stable identifier stored on the poll, e.g. "approval".
	Key() string
	// Label is a human-friendly name for the UI.
	Label() string
	// DefaultConfig returns the method's default configuration as JSON.
	DefaultConfig() json.RawMessage
	// ValidateConfig checks a host-provided configuration blob.
	ValidateConfig(raw json.RawMessage) error
	// ValidateBallot checks a participant's ballot against the config. allIDs
	// is the full candidate set; ownIDs are nominations the voter submitted
	// (used to enforce the self-vote rule).
	ValidateBallot(raw json.RawMessage, b Ballot, allIDs, ownIDs []string) error
	// Tally computes results from every ballot.
	Tally(raw json.RawMessage, allIDs []string, ballots []Ballot) (Results, error)
}

// AutoDecider is an optional interface for methods that choose a winner without
// a voting round (e.g. Random). A poll whose method is an AutoDecider skips
// round 2: the host closes round 1 and Decide picks the winner, which is then
// persisted so it stays stable across reads.
type AutoDecider interface {
	Method
	Decide(raw json.RawMessage, allIDs []string) (string, error)
}

// Decider returns the registered method for key if it decides without voting.
func Decider(key string) (AutoDecider, bool) {
	m, ok := registry[key]
	if !ok {
		return nil, false
	}
	d, ok := m.(AutoDecider)
	return d, ok
}

var registry = map[string]Method{}

// Register adds a method to the registry, replacing any existing one with the
// same key.
func Register(m Method) { registry[m.Key()] = m }

// Get returns the method for key, if registered.
func Get(key string) (Method, bool) {
	m, ok := registry[key]
	return m, ok
}

// All returns every registered method sorted by key.
func All() []Method {
	out := make([]Method, 0, len(registry))
	for _, m := range registry {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out
}

func init() {
	Register(Approval{})
	Register(Ranked{})
	Register(Score{})
	Register(Random{})
}

// selfVoteLimit resolves the effective cap on a voter's support for their own
// nominations. maxSelfVotes, when present, is authoritative: a negative value
// means unlimited, 0 means none, and N means at most N. When absent (nil) it
// falls back to the legacy allow_self_vote bool (true = unlimited, false =
// none). The returned limit is -1 for unlimited, 0 for none, or a positive cap.
//
// "Support" is counted per method: total votes spent (approval), or the number
// of own nominations ranked (ranked) or scored (score).
func selfVoteLimit(allowSelfVote bool, maxSelfVotes *int) int {
	if maxSelfVotes != nil {
		if *maxSelfVotes < 0 {
			return -1
		}
		return *maxSelfVotes
	}
	if allowSelfVote {
		return -1
	}
	return 0
}

// toSet builds a lookup set from a slice of IDs.
func toSet(ids []string) map[string]bool {
	s := make(map[string]bool, len(ids))
	for _, id := range ids {
		s[id] = true
	}
	return s
}

// rankByScore turns a score map into a best-first Result slice and the set of
// winner IDs (all sharing the top, strictly-positive score). A top score of 0
// (nobody voted) yields no winners.
func rankByScore(scores map[string]float64, allIDs []string) ([]Result, []string) {
	results := make([]Result, 0, len(allIDs))
	for _, id := range allIDs {
		results = append(results, Result{NominationID: id, Score: scores[id]})
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].NominationID < results[j].NominationID
	})
	var winners []string
	if len(results) > 0 && results[0].Score > 0 {
		top := results[0].Score
		for _, r := range results {
			if r.Score == top {
				winners = append(winners, r.NominationID)
			}
		}
	}
	return results, winners
}

// Veto is the Selections value for a "definitely not" vote. Any veto knocks the
// nomination out of the running. Vetoes are method-agnostic: the poll config's
// max_vetoes (0 = vetoes off, <0 = unlimited) caps them per voter, and ValidateBallot / Tally
// strip them before the method sees the ballot.
const Veto = -1

func maxVetoes(raw json.RawMessage) int {
	var cfg struct {
		MaxVetoes int `json:"max_vetoes"`
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &cfg) // a malformed blob is caught by the method's own parse
	}
	return cfg.MaxVetoes
}

// ValidateBallot checks a ballot's vetoes against the config, then hands the
// remaining selections to the method.
func ValidateBallot(m Method, raw json.RawMessage, b Ballot, allIDs, ownIDs []string) error {
	all := toSet(allIDs)
	rest := make(map[string]int, len(b.Selections))
	vetoes := 0
	for id, v := range b.Selections {
		if v != Veto {
			rest[id] = v
			continue
		}
		if !all[id] {
			return fmt.Errorf("unknown nomination %q", id)
		}
		vetoes++
	}
	if limit := maxVetoes(raw); limit >= 0 && vetoes > limit {
		if limit == 0 {
			return fmt.Errorf("vetoes are not allowed in this poll")
		}
		return fmt.Errorf("at most %d veto(es)", limit)
	}
	b.Selections = rest
	return m.ValidateBallot(raw, b, allIDs, ownIDs)
}

// Tally drops every vetoed nomination before the method counts, then lists the
// vetoed ones last with Vetoed set.
func Tally(m Method, raw json.RawMessage, allIDs []string, ballots []Ballot) (Results, error) {
	vetoed := map[string]bool{}
	for _, b := range ballots {
		for id, v := range b.Selections {
			if v == Veto {
				vetoed[id] = true
			}
		}
	}
	// Strip vetoed IDs from every ballot too, so methods never see unknown IDs.
	clean := make([]Ballot, len(ballots))
	for i, b := range ballots {
		sel := make(map[string]int, len(b.Selections))
		for id, v := range b.Selections {
			if !vetoed[id] {
				sel[id] = v
			}
		}
		clean[i] = Ballot{ParticipantID: b.ParticipantID, Selections: sel}
	}
	live := make([]string, 0, len(allIDs))
	for _, id := range allIDs {
		if !vetoed[id] {
			live = append(live, id)
		}
	}
	res, err := m.Tally(raw, live, clean)
	if err != nil {
		return res, err
	}
	for _, id := range allIDs {
		if vetoed[id] {
			res.Ranked = append(res.Ranked, Result{NominationID: id, Vetoed: true})
		}
	}
	return res, nil
}
