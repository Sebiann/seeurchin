package voting

import (
	"encoding/json"
	"fmt"
)

// Approval implements approval / cumulative voting: each voter has a budget of
// votes_per_user votes to distribute across nominations, with an optional cap
// of max_votes_per_option votes on any single option. With max=1 this is plain
// approval voting; with max>1 it becomes cumulative voting.
type Approval struct{}

type approvalConfig struct {
	VotesPerUser      int  `json:"votes_per_user"`       // <0 = unlimited (one vote per option)
	MaxVotesPerOption int  `json:"max_votes_per_option"` // 0 = unlimited (still capped by VotesPerUser)
	AllowSelfVote     bool `json:"allow_self_vote"`
	MaxSelfVotes      *int `json:"max_self_votes,omitempty"` // nil = use AllowSelfVote; <0 unlimited; 0 none; N cap
}

func (Approval) Key() string   { return "approval" }
func (Approval) Label() string { return "Approval / N votes" }

func (Approval) DefaultConfig() json.RawMessage {
	return mustJSON(approvalConfig{VotesPerUser: 3, MaxVotesPerOption: 1, AllowSelfVote: true})
}

func parseApprovalConfig(raw json.RawMessage) (approvalConfig, error) {
	cfg := approvalConfig{VotesPerUser: 3, MaxVotesPerOption: 1, AllowSelfVote: true}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return cfg, fmt.Errorf("approval config: %w", err)
		}
	}
	return cfg, nil
}

func (Approval) ValidateConfig(raw json.RawMessage) error {
	cfg, err := parseApprovalConfig(raw)
	if err != nil {
		return err
	}
	if cfg.VotesPerUser == 0 {
		return fmt.Errorf("votes_per_user must be >= 1 or < 0 for unlimited")
	}
	if cfg.MaxVotesPerOption < 0 {
		return fmt.Errorf("max_votes_per_option must be >= 0")
	}
	return nil
}

func (Approval) ValidateBallot(raw json.RawMessage, b Ballot, allIDs, ownIDs []string) error {
	cfg, err := parseApprovalConfig(raw)
	if err != nil {
		return err
	}
	all := toSet(allIDs)
	own := toSet(ownIDs)
	selfLimit := selfVoteLimit(cfg.AllowSelfVote, cfg.MaxSelfVotes)
	if cfg.VotesPerUser < 0 {
		cfg.MaxVotesPerOption = 1 // unlimited picks: stacking would be unbounded
	}
	total, selfVotes := 0, 0
	for id, votes := range b.Selections {
		if votes <= 0 {
			return fmt.Errorf("vote count for %q must be positive", id)
		}
		if !all[id] {
			return fmt.Errorf("unknown nomination %q", id)
		}
		if cfg.MaxVotesPerOption > 0 && votes > cfg.MaxVotesPerOption {
			return fmt.Errorf("at most %d vote(s) per option", cfg.MaxVotesPerOption)
		}
		if own[id] {
			selfVotes += votes
		}
		total += votes
	}
	if selfLimit == 0 && selfVotes > 0 {
		return fmt.Errorf("voting for your own nomination is not allowed")
	}
	if selfLimit > 0 && selfVotes > selfLimit {
		return fmt.Errorf("at most %d vote(s) on your own nominations", selfLimit)
	}
	if cfg.VotesPerUser > 0 && total > cfg.VotesPerUser {
		return fmt.Errorf("at most %d vote(s) total", cfg.VotesPerUser)
	}
	return nil
}

func (Approval) Tally(raw json.RawMessage, allIDs []string, ballots []Ballot) (Results, error) {
	scores := make(map[string]float64, len(allIDs))
	for _, b := range ballots {
		for id, v := range b.Selections {
			if v > 0 {
				scores[id] += float64(v)
			}
		}
	}
	ranked, winners := rankByScore(scores, allIDs)
	return Results{Method: "approval", Ranked: ranked, WinnerIDs: winners}, nil
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
