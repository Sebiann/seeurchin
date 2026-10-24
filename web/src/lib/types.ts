export type Status = 'draft' | 'round1' | 'round2' | 'closed';

export interface SubmissionRules {
	min: number;
	max: number;
	required: number;
}

export interface MeView {
	id: string;
	display_name: string;
	is_host: boolean;
	nomination_count: number;
	has_voted: boolean;
	my_selections: Record<string, number> | null;
}

export interface NominationView {
	id: string;
	item_id: string;
	title: string;
	year: number;
	type: string;
	runtime_minutes: number;
	overview: string;
	image_tag: string;
	source?: string; // "seerr" for write-ins
	poster_url?: string;
	media_type?: string;
	nominator_count: number;
	mine_nominated: boolean;
}

export interface ExternalResult {
	tmdb_id: number;
	media_type: string; // "movie" | "tv"
	title: string;
	year: number;
	poster_url: string;
	overview: string;
	in_library: boolean;
}

export interface ResultEntry {
	nomination_id: string;
	title: string;
	score: number;
	nominators?: string[];
	request_status?: string; // Seerr status for a winning write-in
}

export interface RoundResult {
	round: number;
	counts: Record<string, number>;
	eliminated?: string[];
}

export interface ResultsView {
	method: string;
	winners: ResultEntry[];
	ranked: ResultEntry[];
	rounds?: RoundResult[];
	tied_ids?: string[]; // co-winners a random tie-break chose between
}

export type DeadlineMode = 'none' | 'quick' | 'scheduled';

// The active round's countdown, mirroring httpapi timerView. Absent when the
// current round has no timer. Drive the countdown from closes_at vs the poll's
// server_now (corrects device clock skew); total_sec sizes the ring + ramp.
export interface TimerView {
	mode: 'quick' | 'scheduled';
	closes_at?: string; // RFC3339; absent when armed or paused
	total_sec?: number; // full intended length of this round, seconds
	paused_sec?: number; // remaining seconds while paused
	armed?: boolean; // quick: configured but not yet started
	running: boolean; // closes_at set and still in the future
}

export interface PollView {
	code: string;
	title: string;
	status: Status;
	library_scope: string;
	submission_rules: SubmissionRules;
	min_to_submit: number;
	voting_method: string;
	voting_method_label: string;
	voting_config: Record<string, unknown>;
	allow_guests: boolean;
	results_live: boolean;
	reveal_nominators: boolean;
	reveal_scope: string;
	genres: string[];
	allow_writeins: boolean;
	auto_request_winner: boolean;
	passcode_required: boolean;
	seerr_enabled: boolean;
	participant_count: number;
	voter_count: number;
	participants: { display_name: string; is_host: boolean; has_voted: boolean }[]; // empty until you've joined
	nominations: NominationView[];
	me: MeView | null;
	results?: ResultsView;
	share_url: string;
	timer?: TimerView | null;
	server_now: string; // RFC3339, for client clock-skew correction
}

export interface LibraryItem {
	id: string;
	title: string;
	year: number;
	type: string;
	runtime_minutes: number;
	image_tag: string;
	nominated: boolean;
}

export interface VotingMethod {
	key: string;
	label: string;
	default_config: Record<string, unknown>;
}

// --- auth ---

// Jellyfin login state, mirroring GET /api/user/session.
export interface UserSession {
	login_enabled: boolean;
	authenticated: boolean;
	display_name: string;
	is_admin: boolean;
}

// --- admin dashboard ---

export interface AdminSession {
	authenticated: boolean;
	authorized: boolean;
}

// One row of the admin history list, mirroring httpapi adminPollView.
export interface AdminPollSummary {
	code: string;
	title: string;
	status: Status;
	voting_method: string;
	voting_method_label: string;
	deadline_mode?: string;
	participant_count: number;
	nomination_count: number;
	voter_count: number;
	winner_title?: string;
	created_at: string; // RFC3339
	closed_at?: string; // RFC3339, present once closed
}

export interface CreatePollBody {
	title: string;
	host_name: string;
	library_scope: string;
	voting_method: string;
	voting_config: Record<string, unknown>;
	submission_rules: SubmissionRules;
	allow_guests: boolean;
	results_live: boolean;
	reveal_nominators: boolean;
	reveal_scope: string;
	genres: string[];
	allow_writeins: boolean;
	auto_request_winner: boolean;
	passcode?: string; // optional per-poll guest passcode
	deadline_mode?: DeadlineMode;
	round1_duration_sec?: number; // quick mode
	round2_duration_sec?: number; // quick mode
	round1_closes_at?: string; // scheduled mode, RFC3339
	round2_closes_at?: string; // scheduled mode, RFC3339
}
