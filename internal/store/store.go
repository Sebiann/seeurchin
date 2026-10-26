// Package store is the SQLite-backed implementation of poll.Repository, using
// the pure-Go modernc.org/sqlite driver (no CGO).
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	_ "modernc.org/sqlite"

	"github.com/enderu/seeurchin/internal/poll"
)

// Store persists the poll domain in SQLite.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite database at path and applies the
// schema. Sensible pragmas are enabled: WAL, foreign keys, and a busy timeout.
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)", url.PathEscape(path))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// modernc.org/sqlite is not safe for arbitrary concurrent writers on one
	// connection; a single open connection keeps writes serialized.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

const schema = `
CREATE TABLE IF NOT EXISTS polls (
  id                  TEXT PRIMARY KEY,
  code                TEXT NOT NULL UNIQUE,
  title               TEXT NOT NULL,
  host_participant_id TEXT NOT NULL DEFAULT '',
  library_scope       TEXT NOT NULL,
  status              TEXT NOT NULL,
  submission_rules    TEXT NOT NULL,
  voting_method       TEXT NOT NULL,
  voting_config       TEXT NOT NULL,
  allow_guests        INTEGER NOT NULL DEFAULT 1,
  results_live        INTEGER NOT NULL DEFAULT 0,
  reveal_nominators   INTEGER NOT NULL DEFAULT 0,
  reveal_scope        TEXT NOT NULL DEFAULT 'winner',
  genres              TEXT NOT NULL DEFAULT '[]',
  winner_nomination_id TEXT NOT NULL DEFAULT '',
  decided_at          TEXT,
  closed_at           TEXT,
  allow_writeins      INTEGER NOT NULL DEFAULT 0,
  auto_request_winner INTEGER NOT NULL DEFAULT 0,
  passcode_hash       TEXT NOT NULL DEFAULT '',
  created_at          TEXT NOT NULL,
  round1_closes_at    TEXT,
  round2_closes_at    TEXT,
  deadline_mode       TEXT NOT NULL DEFAULT '',
  round1_duration_sec INTEGER NOT NULL DEFAULT 0,
  round2_duration_sec INTEGER NOT NULL DEFAULT 0,
  timer_paused_sec    INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS participants (
  id               TEXT PRIMARY KEY,
  poll_id          TEXT NOT NULL REFERENCES polls(id) ON DELETE CASCADE,
  display_name     TEXT NOT NULL,
  session_token    TEXT NOT NULL,
  jellyfin_user_id TEXT NOT NULL DEFAULT '',
  role             TEXT NOT NULL DEFAULT 'participant',
  created_at       TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_participants_session ON participants(poll_id, session_token);
CREATE UNIQUE INDEX IF NOT EXISTS idx_participants_jellyfin ON participants(poll_id, jellyfin_user_id) WHERE jellyfin_user_id <> '';

CREATE TABLE IF NOT EXISTS nominations (
  id               TEXT PRIMARY KEY,
  poll_id          TEXT NOT NULL REFERENCES polls(id) ON DELETE CASCADE,
  jellyfin_item_id TEXT NOT NULL,
  snapshot         TEXT NOT NULL,
  created_at       TEXT NOT NULL,
  UNIQUE(poll_id, jellyfin_item_id)
);

CREATE TABLE IF NOT EXISTS nomination_nominators (
  nomination_id  TEXT NOT NULL REFERENCES nominations(id) ON DELETE CASCADE,
  participant_id TEXT NOT NULL REFERENCES participants(id) ON DELETE CASCADE,
  created_at     TEXT NOT NULL,
  PRIMARY KEY (nomination_id, participant_id)
);

CREATE TABLE IF NOT EXISTS votes (
  id             TEXT PRIMARY KEY,
  poll_id        TEXT NOT NULL REFERENCES polls(id) ON DELETE CASCADE,
  participant_id TEXT NOT NULL REFERENCES participants(id) ON DELETE CASCADE,
  nomination_id  TEXT NOT NULL REFERENCES nominations(id) ON DELETE CASCADE,
  value          INTEGER NOT NULL,
  created_at     TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_votes_participant ON votes(poll_id, participant_id);

CREATE TABLE IF NOT EXISTS seerr_requests (
  poll_id       TEXT NOT NULL REFERENCES polls(id) ON DELETE CASCADE,
  nomination_id TEXT NOT NULL,
  tmdb_id       INTEGER NOT NULL,
  media_type    TEXT NOT NULL,
  status        TEXT NOT NULL,
  created_at    TEXT NOT NULL,
  PRIMARY KEY (poll_id, nomination_id)
);
`

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return err
	}
	return s.addColumns(ctx)
}

// addColumns idempotently brings an existing database up to the current schema
// by adding any columns introduced after it was first created. The schema const
// above is the fresh-install path; this covers in-place upgrades (the deployed
// instance carries live data, so we never drop and recreate).
func (s *Store) addColumns(ctx context.Context) error {
	type col struct{ table, name, ddl string }
	wanted := []col{
		{"polls", "reveal_nominators", "INTEGER NOT NULL DEFAULT 0"},
		{"polls", "reveal_scope", "TEXT NOT NULL DEFAULT 'winner'"},
		{"polls", "genres", "TEXT NOT NULL DEFAULT '[]'"},
		{"polls", "winner_nomination_id", "TEXT NOT NULL DEFAULT ''"},
		{"polls", "decided_at", "TEXT"},
		{"polls", "closed_at", "TEXT"},
		{"polls", "allow_writeins", "INTEGER NOT NULL DEFAULT 0"},
		{"polls", "auto_request_winner", "INTEGER NOT NULL DEFAULT 0"},
		{"polls", "passcode_hash", "TEXT NOT NULL DEFAULT ''"},
		{"polls", "deadline_mode", "TEXT NOT NULL DEFAULT ''"},
		{"polls", "round1_duration_sec", "INTEGER NOT NULL DEFAULT 0"},
		{"polls", "round2_duration_sec", "INTEGER NOT NULL DEFAULT 0"},
		{"polls", "timer_paused_sec", "INTEGER NOT NULL DEFAULT 0"},
	}
	for _, c := range wanted {
		has, err := s.columnExists(ctx, c.table, c.name)
		if err != nil {
			return err
		}
		if has {
			continue
		}
		if _, err := s.db.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", c.table, c.name, c.ddl)); err != nil {
			return fmt.Errorf("add column %s.%s: %w", c.table, c.name, err)
		}
	}
	return nil
}

// columnExists reports whether table has a column named column.
func (s *Store) columnExists(ctx context.Context, table, column string) (bool, error) {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid, notnull, pk int
			name, ctype      string
			dflt             sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// --- time helpers ---

func nowText() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func timeText(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func parseNullTime(ns sql.NullString) *time.Time {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	t := parseTime(ns.String)
	return &t
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// --- polls ---

func (s *Store) CreatePoll(ctx context.Context, p *poll.Poll, host *poll.Participant) error {
	rules, err := json.Marshal(p.SubmissionRules)
	if err != nil {
		return err
	}
	if p.Genres == nil {
		p.Genres = []string{}
	}
	genres, err := json.Marshal(p.Genres)
	if err != nil {
		return err
	}
	if p.RevealScope == "" {
		p.RevealScope = poll.RevealWinner
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	if host.CreatedAt.IsZero() {
		host.CreatedAt = time.Now().UTC()
	}
	host.PollID = p.ID
	host.Role = poll.RoleHost
	p.HostParticipantID = host.ID

	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO polls (id, code, title, host_participant_id, library_scope, status,
				submission_rules, voting_method, voting_config, allow_guests, results_live,
				reveal_nominators, reveal_scope, genres, winner_nomination_id, decided_at,
				allow_writeins, auto_request_winner, passcode_hash,
				created_at, round1_closes_at, round2_closes_at,
				deadline_mode, round1_duration_sec, round2_duration_sec, timer_paused_sec)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			p.ID, p.Code, p.Title, p.HostParticipantID, string(p.LibraryScope), string(p.Status),
			string(rules), p.VotingMethod, string(p.VotingConfig), boolToInt(p.AllowGuests), boolToInt(p.ResultsLive),
			boolToInt(p.RevealNominators), p.RevealScope, string(genres), p.WinnerNominationID, timeText(p.DecidedAt),
			boolToInt(p.AllowWriteins), boolToInt(p.AutoRequestWinner), p.PasscodeHash,
			p.CreatedAt.UTC().Format(time.RFC3339Nano), timeText(p.Round1ClosesAt), timeText(p.Round2ClosesAt),
			string(p.DeadlineMode), p.Round1DurationSec, p.Round2DurationSec, p.TimerPausedSec,
		); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO participants (id, poll_id, display_name, session_token, jellyfin_user_id, role, created_at)
			VALUES (?,?,?,?,?,?,?)`,
			host.ID, host.PollID, host.DisplayName, host.SessionToken, host.JellyfinUserID, host.Role,
			host.CreatedAt.UTC().Format(time.RFC3339Nano),
		)
		return err
	})
}

func (s *Store) GetPollByCode(ctx context.Context, code string) (*poll.Poll, error) {
	return s.scanPoll(s.db.QueryRowContext(ctx, pollSelect+` WHERE code = ?`, code))
}

func (s *Store) GetPollByID(ctx context.Context, id string) (*poll.Poll, error) {
	return s.scanPoll(s.db.QueryRowContext(ctx, pollSelect+` WHERE id = ?`, id))
}

const pollSelect = `SELECT id, code, title, host_participant_id, library_scope, status,
	submission_rules, voting_method, voting_config, allow_guests, results_live,
	reveal_nominators, reveal_scope, genres, winner_nomination_id, decided_at,
	allow_writeins, auto_request_winner, passcode_hash,
	created_at, round1_closes_at, round2_closes_at,
	deadline_mode, round1_duration_sec, round2_duration_sec, timer_paused_sec, closed_at FROM polls`

func (s *Store) scanPoll(row rowScanner) (*poll.Poll, error) {
	var (
		p                                poll.Poll
		scope, status, rules, vmethod    string
		vconfig                          string
		allowGuests, resultsLive         int
		revealNominators                 int
		revealScope, genres, winnerNomID string
		decidedAt                        sql.NullString
		allowWriteins, autoRequestWinner int
		passcodeHash                     string
		createdAt                        string
		r1, r2                           sql.NullString
		deadlineMode                     string
		r1Dur, r2Dur, pausedSec          int
		closedAt                         sql.NullString
	)
	err := row.Scan(&p.ID, &p.Code, &p.Title, &p.HostParticipantID, &scope, &status,
		&rules, &vmethod, &vconfig, &allowGuests, &resultsLive,
		&revealNominators, &revealScope, &genres, &winnerNomID, &decidedAt,
		&allowWriteins, &autoRequestWinner, &passcodeHash,
		&createdAt, &r1, &r2,
		&deadlineMode, &r1Dur, &r2Dur, &pausedSec, &closedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, poll.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	p.LibraryScope = poll.LibraryScope(scope)
	p.Status = poll.Status(status)
	p.VotingMethod = vmethod
	p.VotingConfig = json.RawMessage(vconfig)
	p.AllowGuests = allowGuests != 0
	p.ResultsLive = resultsLive != 0
	p.RevealNominators = revealNominators != 0
	p.RevealScope = revealScope
	p.WinnerNominationID = winnerNomID
	p.DecidedAt = parseNullTime(decidedAt)
	p.AllowWriteins = allowWriteins != 0
	p.AutoRequestWinner = autoRequestWinner != 0
	p.PasscodeHash = passcodeHash
	p.CreatedAt = parseTime(createdAt)
	p.Round1ClosesAt = parseNullTime(r1)
	p.Round2ClosesAt = parseNullTime(r2)
	p.DeadlineMode = poll.DeadlineMode(deadlineMode)
	p.Round1DurationSec = r1Dur
	p.Round2DurationSec = r2Dur
	p.TimerPausedSec = pausedSec
	p.ClosedAt = parseNullTime(closedAt)
	p.Genres = []string{}
	if genres != "" {
		if err := json.Unmarshal([]byte(genres), &p.Genres); err != nil {
			return nil, fmt.Errorf("decode genres: %w", err)
		}
	}
	if err := json.Unmarshal([]byte(rules), &p.SubmissionRules); err != nil {
		return nil, fmt.Errorf("decode submission_rules: %w", err)
	}
	return &p, nil
}

// rowScanner is the common Scan surface of *sql.Row and *sql.Rows, so scanPoll
// works for both single-row and multi-row queries.
type rowScanner interface {
	Scan(dest ...any) error
}

func (s *Store) UpdatePollStatus(ctx context.Context, id string, status poll.Status) error {
	res, err := s.db.ExecContext(ctx, `UPDATE polls SET status = ? WHERE id = ?`, string(status), id)
	if err != nil {
		return err
	}
	return mustAffect(res)
}

// CompareAndSetStatus moves a poll from one status to another only if it is
// currently in `from`, returning whether the row changed. Transitions into
// "closed" also stamp closed_at, the authoritative poll-end time for retention.
func (s *Store) CompareAndSetStatus(ctx context.Context, id string, from, to poll.Status) (bool, error) {
	var (
		res sql.Result
		err error
	)
	if to == poll.StatusClosed {
		res, err = s.db.ExecContext(ctx,
			`UPDATE polls SET status = ?, closed_at = ? WHERE id = ? AND status = ?`,
			string(to), nowText(), id, string(from))
	} else {
		res, err = s.db.ExecContext(ctx,
			`UPDATE polls SET status = ? WHERE id = ? AND status = ?`, string(to), id, string(from))
	}
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// UpdatePollTimers persists the deadline-related columns for a poll.
func (s *Store) UpdatePollTimers(ctx context.Context, p *poll.Poll) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE polls SET
			deadline_mode = ?, round1_duration_sec = ?, round2_duration_sec = ?,
			round1_closes_at = ?, round2_closes_at = ?, timer_paused_sec = ?
		WHERE id = ?`,
		string(p.DeadlineMode), p.Round1DurationSec, p.Round2DurationSec,
		timeText(p.Round1ClosesAt), timeText(p.Round2ClosesAt), p.TimerPausedSec, p.ID)
	if err != nil {
		return err
	}
	return mustAffect(res)
}

func (s *Store) UpdatePollSettings(ctx context.Context, p *poll.Poll) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE polls SET voting_config = ?, results_live = ? WHERE id = ?`,
		string(p.VotingConfig), boolToInt(p.ResultsLive), p.ID)
	if err != nil {
		return err
	}
	return mustAffect(res)
}

// ListActiveTimedPolls returns polls in round 1 or round 2 whose active round
// has a close time set (a running timer).
func (s *Store) ListActiveTimedPolls(ctx context.Context) ([]*poll.Poll, error) {
	rows, err := s.db.QueryContext(ctx, pollSelect+`
		WHERE (status = ? AND round1_closes_at IS NOT NULL)
		   OR (status = ? AND round2_closes_at IS NOT NULL)`,
		string(poll.StatusRound1), string(poll.StatusRound2))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*poll.Poll
	for rows.Next() {
		p, err := s.scanPoll(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) SetPollWinner(ctx context.Context, id, nominationID string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE polls SET winner_nomination_id = ?, decided_at = ? WHERE id = ?`,
		nominationID, nowText(), id)
	if err != nil {
		return err
	}
	return mustAffect(res)
}

func (s *Store) SetPollWinnerIfUnset(ctx context.Context, id, nominationID string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE polls SET winner_nomination_id = ?, decided_at = ? WHERE id = ? AND winner_nomination_id = ''`,
		nominationID, nowText(), id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (s *Store) CodeExists(ctx context.Context, code string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM polls WHERE code = ?`, code).Scan(&n)
	return n > 0, err
}

// ListPolls returns every poll, newest first (for the admin history view).
func (s *Store) ListPolls(ctx context.Context) ([]*poll.Poll, error) {
	rows, err := s.db.QueryContext(ctx, pollSelect+` ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*poll.Poll
	for rows.Next() {
		p, err := s.scanPoll(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// AllPollCounts returns participant/nomination/voter tallies for every poll,
// keyed by poll ID, in three grouped queries (no per-poll fan-out).
func (s *Store) AllPollCounts(ctx context.Context) (map[string]poll.PollCounts, error) {
	out := map[string]poll.PollCounts{}
	groupedCount := func(query string, set func(c *poll.PollCounts, n int)) error {
		rows, err := s.db.QueryContext(ctx, query)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				id string
				n  int
			)
			if err := rows.Scan(&id, &n); err != nil {
				return err
			}
			c := out[id]
			set(&c, n)
			out[id] = c
		}
		return rows.Err()
	}
	if err := groupedCount(
		`SELECT poll_id, COUNT(*) FROM participants GROUP BY poll_id`,
		func(c *poll.PollCounts, n int) { c.Participants = n }); err != nil {
		return nil, err
	}
	if err := groupedCount(
		`SELECT poll_id, COUNT(*) FROM nominations GROUP BY poll_id`,
		func(c *poll.PollCounts, n int) { c.Nominations = n }); err != nil {
		return nil, err
	}
	if err := groupedCount(
		`SELECT poll_id, COUNT(DISTINCT participant_id) FROM votes GROUP BY poll_id`,
		func(c *poll.PollCounts, n int) { c.Voters = n }); err != nil {
		return nil, err
	}
	return out, nil
}

// DeletePoll removes a poll; ON DELETE CASCADE clears its participants,
// nominations, votes, and Seerr requests.
func (s *Store) DeletePoll(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM polls WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return mustAffect(res)
}

// DeleteClosedPollsBefore deletes closed polls whose end time is before cutoff.
// The end time is closed_at, falling back to decided_at for polls closed before
// closed_at existed; a closed poll with neither recorded is never purged. All
// timestamps are stored as UTC RFC3339Nano, so the lexicographic comparison is
// chronological.
func (s *Store) DeleteClosedPollsBefore(ctx context.Context, cutoff time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM polls
		WHERE status = ?
		  AND COALESCE(NULLIF(closed_at, ''), NULLIF(decided_at, '')) IS NOT NULL
		  AND COALESCE(NULLIF(closed_at, ''), NULLIF(decided_at, '')) < ?`,
		string(poll.StatusClosed), cutoff.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// --- participants ---

func (s *Store) CreateParticipant(ctx context.Context, p *poll.Participant) error {
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	if p.Role == "" {
		p.Role = poll.RoleParticipant
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO participants (id, poll_id, display_name, session_token, jellyfin_user_id, role, created_at)
		VALUES (?,?,?,?,?,?,?)`,
		p.ID, p.PollID, p.DisplayName, p.SessionToken, p.JellyfinUserID, p.Role,
		p.CreatedAt.UTC().Format(time.RFC3339Nano))
	return err
}

const participantSelect = `SELECT id, poll_id, display_name, session_token, jellyfin_user_id, role, created_at FROM participants`

func (s *Store) GetParticipant(ctx context.Context, id string) (*poll.Participant, error) {
	return scanParticipant(s.db.QueryRowContext(ctx, participantSelect+` WHERE id = ?`, id))
}

func (s *Store) GetParticipantBySession(ctx context.Context, pollID, token string) (*poll.Participant, error) {
	return scanParticipant(s.db.QueryRowContext(ctx, participantSelect+` WHERE poll_id = ? AND session_token = ?`, pollID, token))
}

func (s *Store) GetParticipantByJellyfinUser(ctx context.Context, pollID, jellyfinUserID string) (*poll.Participant, error) {
	return scanParticipant(s.db.QueryRowContext(ctx, participantSelect+` WHERE poll_id = ? AND jellyfin_user_id = ?`, pollID, jellyfinUserID))
}

func (s *Store) SetParticipantJellyfinUser(ctx context.Context, participantID, jellyfinUserID string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE participants SET jellyfin_user_id = ? WHERE id = ?`, jellyfinUserID, participantID)
	if err != nil {
		return err
	}
	return mustAffect(res)
}

func scanParticipant(row *sql.Row) (*poll.Participant, error) {
	var p poll.Participant
	var createdAt string
	err := row.Scan(&p.ID, &p.PollID, &p.DisplayName, &p.SessionToken, &p.JellyfinUserID, &p.Role, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, poll.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	p.CreatedAt = parseTime(createdAt)
	return &p, nil
}

func (s *Store) ListParticipants(ctx context.Context, pollID string) ([]poll.Participant, error) {
	rows, err := s.db.QueryContext(ctx, participantSelect+` WHERE poll_id = ? ORDER BY created_at`, pollID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []poll.Participant
	for rows.Next() {
		var p poll.Participant
		var createdAt string
		if err := rows.Scan(&p.ID, &p.PollID, &p.DisplayName, &p.SessionToken, &p.JellyfinUserID, &p.Role, &createdAt); err != nil {
			return nil, err
		}
		p.CreatedAt = parseTime(createdAt)
		out = append(out, p)
	}
	return out, rows.Err()
}

// --- nominations ---

func (s *Store) AddNomination(ctx context.Context, n *poll.Nomination, nominatorID string) (*poll.Nomination, error) {
	snapshot, err := json.Marshal(n.Snapshot)
	if err != nil {
		return nil, err
	}
	var effective *poll.Nomination
	err = s.tx(ctx, func(tx *sql.Tx) error {
		var existingID string
		row := tx.QueryRowContext(ctx, `SELECT id FROM nominations WHERE poll_id = ? AND jellyfin_item_id = ?`,
			n.PollID, n.JellyfinItemID)
		switch err := row.Scan(&existingID); {
		case errors.Is(err, sql.ErrNoRows):
			if n.ID == "" {
				n.ID = poll.NewID()
			}
			if n.CreatedAt.IsZero() {
				n.CreatedAt = time.Now().UTC()
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO nominations (id, poll_id, jellyfin_item_id, snapshot, created_at)
				VALUES (?,?,?,?,?)`,
				n.ID, n.PollID, n.JellyfinItemID, string(snapshot), n.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
				return err
			}
			existingID = n.ID
		case err != nil:
			return err
		default:
			n.ID = existingID
		}
		// Add nominator (idempotent).
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO nomination_nominators (nomination_id, participant_id, created_at)
			VALUES (?,?,?)
			ON CONFLICT(nomination_id, participant_id) DO NOTHING`,
			existingID, nominatorID, nowText()); err != nil {
			return err
		}
		effective = n
		return nil
	})
	if err != nil {
		return nil, err
	}
	return effective, nil
}

func (s *Store) WithdrawNomination(ctx context.Context, pollID, nominationID, participantID string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM nomination_nominators WHERE nomination_id = ? AND participant_id = ?`,
			nominationID, participantID); err != nil {
			return err
		}
		var remaining int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM nomination_nominators WHERE nomination_id = ?`,
			nominationID).Scan(&remaining); err != nil {
			return err
		}
		if remaining == 0 {
			if _, err := tx.ExecContext(ctx, `DELETE FROM nominations WHERE id = ? AND poll_id = ?`,
				nominationID, pollID); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) ListNominations(ctx context.Context, pollID string) ([]poll.Nomination, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, poll_id, jellyfin_item_id, snapshot, created_at
		FROM nominations WHERE poll_id = ? ORDER BY created_at`, pollID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var noms []poll.Nomination
	index := map[string]int{}
	for rows.Next() {
		var n poll.Nomination
		var snapshot, createdAt string
		if err := rows.Scan(&n.ID, &n.PollID, &n.JellyfinItemID, &snapshot, &createdAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(snapshot), &n.Snapshot); err != nil {
			return nil, fmt.Errorf("decode snapshot: %w", err)
		}
		n.CreatedAt = parseTime(createdAt)
		index[n.ID] = len(noms)
		noms = append(noms, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Attach nominators.
	nrows, err := s.db.QueryContext(ctx, `
		SELECT nn.nomination_id, nn.participant_id
		FROM nomination_nominators nn
		JOIN nominations n ON n.id = nn.nomination_id
		WHERE n.poll_id = ? ORDER BY nn.created_at`, pollID)
	if err != nil {
		return nil, err
	}
	defer nrows.Close()
	for nrows.Next() {
		var nomID, participantID string
		if err := nrows.Scan(&nomID, &participantID); err != nil {
			return nil, err
		}
		if i, ok := index[nomID]; ok {
			noms[i].Nominators = append(noms[i].Nominators, participantID)
		}
	}
	return noms, nrows.Err()
}

func (s *Store) CountNominationsByParticipant(ctx context.Context, pollID, participantID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(1) FROM nomination_nominators nn
		JOIN nominations nm ON nm.id = nn.nomination_id
		WHERE nm.poll_id = ? AND nn.participant_id = ?`, pollID, participantID).Scan(&n)
	return n, err
}

// --- votes ---

func (s *Store) ReplaceVotes(ctx context.Context, pollID, participantID string, votes []poll.Vote) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM votes WHERE poll_id = ? AND participant_id = ?`,
			pollID, participantID); err != nil {
			return err
		}
		for _, v := range votes {
			if v.ID == "" {
				v.ID = poll.NewID()
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO votes (id, poll_id, participant_id, nomination_id, value, created_at)
				VALUES (?,?,?,?,?,?)`,
				v.ID, pollID, participantID, v.NominationID, v.Value, nowText()); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) MergeParticipants(ctx context.Context, pollID, keepID, ballotFromID string, dropIDs []string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, id := range dropIDs {
			if _, err := tx.ExecContext(ctx, `
				INSERT OR IGNORE INTO nomination_nominators (nomination_id, participant_id, created_at)
				SELECT nomination_id, ?, created_at FROM nomination_nominators WHERE participant_id = ?`,
				keepID, id); err != nil {
				return err
			}
		}
		if ballotFromID != "" && ballotFromID != keepID {
			if _, err := tx.ExecContext(ctx, `DELETE FROM votes WHERE poll_id = ? AND participant_id = ?`,
				pollID, keepID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE votes SET participant_id = ? WHERE poll_id = ? AND participant_id = ?`,
				keepID, pollID, ballotFromID); err != nil {
				return err
			}
		}
		for _, id := range dropIDs {
			// ON DELETE CASCADE removes their remaining votes and nominator rows.
			if _, err := tx.ExecContext(ctx, `DELETE FROM participants WHERE poll_id = ? AND id = ?`,
				pollID, id); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) ListVotes(ctx context.Context, pollID string) ([]poll.Vote, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, poll_id, participant_id, nomination_id, value, created_at
		FROM votes WHERE poll_id = ? ORDER BY created_at`, pollID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []poll.Vote
	for rows.Next() {
		var v poll.Vote
		var createdAt string
		if err := rows.Scan(&v.ID, &v.PollID, &v.ParticipantID, &v.NominationID, &v.Value, &createdAt); err != nil {
			return nil, err
		}
		v.CreatedAt = parseTime(createdAt)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) CountVoters(ctx context.Context, pollID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT participant_id) FROM votes WHERE poll_id = ?`, pollID).Scan(&n)
	return n, err
}

func (s *Store) HasVoted(ctx context.Context, pollID, participantID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM votes WHERE poll_id = ? AND participant_id = ?`,
		pollID, participantID).Scan(&n)
	return n > 0, err
}

// --- seerr requests ---

func (s *Store) RecordSeerrRequest(ctx context.Context, req *poll.SeerrRequest) error {
	if req.CreatedAt.IsZero() {
		req.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO seerr_requests (poll_id, nomination_id, tmdb_id, media_type, status, created_at)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT(poll_id, nomination_id) DO UPDATE SET status = excluded.status`,
		req.PollID, req.NominationID, req.TMDBID, req.MediaType, req.Status,
		req.CreatedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) GetSeerrRequest(ctx context.Context, pollID, nominationID string) (*poll.SeerrRequest, error) {
	var (
		req       poll.SeerrRequest
		createdAt string
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT poll_id, nomination_id, tmdb_id, media_type, status, created_at
		FROM seerr_requests WHERE poll_id = ? AND nomination_id = ?`, pollID, nominationID).
		Scan(&req.PollID, &req.NominationID, &req.TMDBID, &req.MediaType, &req.Status, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	req.CreatedAt = parseTime(createdAt)
	return &req, nil
}

// --- helpers ---

func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func mustAffect(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return poll.ErrNotFound
	}
	return nil
}
