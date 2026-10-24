<script lang="ts">
	import { api } from '$lib/api';
	import type { PollView } from '$lib/types';
	import PosterImage from './PosterImage.svelte';
	import LiveResults from './LiveResults.svelte';

	let {
		poll,
		code,
		update
	}: { poll: PollView; code: string; update: (p: PollView) => void } = $props();

	const method = poll.voting_method;
	// voting_config can be null (e.g. a poll created via the API without one);
	// fall back to an empty object so the per-method reads below use defaults
	// instead of throwing on a null deref.
	const cfg = (poll.voting_config ?? {}) as Record<string, number | boolean>;
	// max_self_votes (when present) is authoritative over the legacy
	// allow_self_vote bool — mirrors selfVoteLimit in internal/voting.
	// A positive cap is enforced server-side; here only "none" blocks.
	const allowSelf =
		cfg.max_self_votes === undefined || cfg.max_self_votes === null
			? cfg.allow_self_vote !== false
			: Number(cfg.max_self_votes) !== 0;
	const noms = $derived(poll.nominations);
	const isHost = $derived(poll.me?.is_host ?? false);

	// approval / score selections; initialized once from any existing ballot.
	let selections = $state<Record<string, number>>({ ...(poll.me?.my_selections ?? {}) });
	// ranked uses an ordered list as the source of truth.
	let ranking = $state<string[]>(
		Object.entries(poll.me?.my_selections ?? {})
			.sort((a, b) => a[1] - b[1])
			.map(([id]) => id)
	);

	let busy = $state(false);
	let error = $state('');

	// Host-only: voters whose ballots are dropped when revealing results.
	let pickIgnored = $state(false);
	let ignored = $state<string[]>([]);
	const voters = $derived(poll.participants.filter((p) => p.has_voted));

	// --- approval ---
	const votesPerUser = Number(cfg.votes_per_user ?? 3);
	const maxPer = Number(cfg.max_votes_per_option ?? 1);
	const used = $derived(Object.values(selections).reduce((a, b) => a + (b > 0 ? b : 0), 0));
	const remaining = $derived(votesPerUser - used);

	function selfBlocked(mine: boolean) {
		return !allowSelf && mine;
	}
	function setApproval(id: string, v: number) {
		if (v <= 0) delete selections[id];
		else selections[id] = v;
	}
	function bumpApproval(id: string, delta: number, mine: boolean) {
		if (selfBlocked(mine)) return;
		const cur = selections[id] ?? 0;
		let next = cur + delta;
		if (next < 0) next = 0;
		if (maxPer > 0 && next > maxPer) next = maxPer;
		if (delta > 0 && remaining <= 0) return;
		setApproval(id, next);
	}

	// --- score ---
	const maxScore = Number(cfg.max_score ?? 5);
	function setScore(id: string, s: number, mine: boolean) {
		if (selfBlocked(mine)) return;
		if ((selections[id] ?? 0) === s) delete selections[id];
		else selections[id] = s;
	}

	// --- ranked ---
	const maxRanked = Number(cfg.max_ranked ?? 0);
	const unranked = $derived(
		noms.filter((n) => !ranking.includes(n.id) && !selfBlocked(n.mine_nominated))
	);
	function addRank(id: string) {
		if (maxRanked > 0 && ranking.length >= maxRanked) return;
		ranking = [...ranking, id];
	}
	function removeRank(id: string) {
		ranking = ranking.filter((x) => x !== id);
	}
	function moveRank(i: number, dir: number) {
		const j = i + dir;
		if (j < 0 || j >= ranking.length) return;
		const next = [...ranking];
		[next[i], next[j]] = [next[j], next[i]];
		ranking = next;
	}
	function titleOf(id: string) {
		return noms.find((n) => n.id === id)?.title ?? '';
	}

	async function submit() {
		busy = true;
		error = '';
		try {
			const sel: Record<string, number> = {};
			if (method === 'ranked') {
				ranking.forEach((id, i) => (sel[id] = i + 1));
			} else {
				for (const [id, v] of Object.entries(selections)) if (v > 0) sel[id] = v;
			}
			update(await api.vote(code, sel));
		} catch (err) {
			error = err instanceof Error ? err.message : 'could not submit vote';
		} finally {
			busy = false;
		}
	}
</script>

{#snippet check(size: number)}
	<svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M5 12l5 5 9-10" /></svg>
{/snippet}

<section>
	<div class="flex flex-wrap items-center justify-between gap-x-3 gap-y-1">
		{#if method === 'approval'}
			<h2 class="section-h">{maxPer === 1 ? `Pick up to ${votesPerUser}` : `You have ${votesPerUser} votes`}</h2>
			<div class="flex items-center gap-2">
				{#if votesPerUser <= 8}
					<span class="flex gap-1" aria-hidden="true">
						{#each Array(votesPerUser) as _, i (i)}
							<span class="h-2 w-5 rounded-full {i < used ? 'bg-accent' : 'bg-line2'}"></span>
						{/each}
					</span>
				{/if}
				<span class="text-sm font-extrabold {remaining === 0 ? 'text-mango-ink' : 'text-accent-ink'}">{remaining} left</span>
			</div>
		{:else if method === 'ranked'}
			<h2 class="section-h">Rank your favorites</h2>
			<span class="text-sm font-semibold text-muted">Best first{maxRanked > 0 ? ` · up to ${maxRanked}` : ''}</span>
		{:else}
			<h2 class="section-h">Rate each title</h2>
			<span class="text-sm font-semibold text-muted">Up to {maxScore} stars</span>
		{/if}
	</div>
	{#if poll.me?.has_voted}
		<p class="mt-1.5 flex items-center gap-1.5 text-sm font-bold text-accent-ink">{@render check(14)} Vote saved. You can change it until voting closes.</p>
	{/if}

	{#if method === 'ranked'}
		{#if ranking.length > 0}
			<ol class="mt-3 space-y-2">
				{#each ranking as id, i (id)}
					<li class="flex items-center gap-3 rounded-2xl border border-line bg-surface p-2 pl-3">
						<span class="grid h-8 w-8 shrink-0 place-items-center rounded-full bg-primary text-sm font-extrabold text-on-primary">{i + 1}</span>
						<span class="min-w-0 flex-1 truncate text-base font-bold text-ink">{titleOf(id)}</span>
						<div class="flex items-center gap-1">
							<button onclick={() => moveRank(i, -1)} disabled={i === 0} aria-label="Move {titleOf(id)} up" class="grid h-10 w-10 place-items-center rounded-xl bg-surface3 text-ink disabled:opacity-30">
								<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M6 15l6-6 6 6" /></svg>
							</button>
							<button onclick={() => moveRank(i, 1)} disabled={i === ranking.length - 1} aria-label="Move {titleOf(id)} down" class="grid h-10 w-10 place-items-center rounded-xl bg-surface3 text-ink disabled:opacity-30">
								<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M6 9l6 6 6-6" /></svg>
							</button>
							<button onclick={() => removeRank(id)} aria-label="Remove {titleOf(id)}" class="grid h-10 w-10 place-items-center rounded-xl bg-surface3 text-coral-ink">
								<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" aria-hidden="true"><path d="M18 6 6 18M6 6l12 12" /></svg>
							</button>
						</div>
					</li>
				{/each}
			</ol>
		{/if}
		{#if unranked.length > 0}
			<h3 class="mt-5 mb-2 text-[15px] font-extrabold text-muted">{ranking.length === 0 ? 'Tap your favorite first' : 'Tap to add next'}</h3>
			<div class="grid grid-cols-3 gap-x-3 gap-y-4 sm:grid-cols-4 md:grid-cols-5">
				{#each unranked as n (n.id)}
					<button onclick={() => addRank(n.id)} disabled={maxRanked > 0 && ranking.length >= maxRanked} class="min-w-0 text-left transition active:scale-[0.97] disabled:opacity-40">
						<PosterImage itemId={n.item_id} tag={n.image_tag} posterUrl={n.poster_url ?? ''} title={n.title} />
						<p class="mt-1.5 truncate text-sm font-bold text-ink">{n.title}</p>
					</button>
				{/each}
			</div>
		{/if}
	{:else}
		<div class="mt-3 space-y-2.5">
			{#each noms as n (n.id)}
				{@const mine = n.mine_nominated}
				{@const blocked = selfBlocked(mine)}
				{@const sel = selections[n.id] ?? 0}
				{@const meta = [n.year || '', mine ? 'Your pick' : ''].filter(Boolean).join(' · ')}
				{#if method === 'approval' && maxPer === 1 && !blocked}
					<!-- Single-vote approval: the whole row is the toggle. -->
					<button
						onclick={() => bumpApproval(n.id, sel > 0 ? -1 : 1, mine)}
						aria-pressed={sel > 0}
						disabled={sel === 0 && remaining <= 0}
						class="flex w-full items-center gap-3 rounded-2xl border-[1.5px] p-2 pr-3.5 text-left transition active:scale-[0.99] {sel > 0 ? 'border-accent bg-accent/10' : 'border-line bg-surface'} disabled:opacity-50"
					>
						<div class="w-[46px] shrink-0 [&_.poster]:rounded-lg"><PosterImage itemId={n.item_id} tag={n.image_tag} posterUrl={n.poster_url ?? ''} title={n.title} /></div>
						<div class="min-w-0 flex-1">
							<p class="truncate text-[17px] font-extrabold text-ink">{n.title}</p>
							{#if meta}<p class="text-sm font-semibold text-muted">{meta}</p>{/if}
						</div>
						<span class="grid h-[30px] w-[30px] shrink-0 place-items-center rounded-full border-2 {sel > 0 ? 'border-accent bg-accent text-on-accent' : 'border-line2'}">
							{#if sel > 0}{@render check(16)}{/if}
						</span>
					</button>
				{:else}
					<div class="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-2xl border-[1.5px] border-line bg-surface p-2 pr-3.5 {blocked ? 'opacity-60' : ''}">
						<div class="w-[46px] shrink-0 [&_.poster]:rounded-lg"><PosterImage itemId={n.item_id} tag={n.image_tag} posterUrl={n.poster_url ?? ''} title={n.title} /></div>
						<div class="min-w-0 flex-1">
							<p class="truncate text-[17px] font-extrabold text-ink">{n.title}</p>
							{#if meta}<p class="text-sm font-semibold text-muted">{meta}</p>{/if}
							{#if !blocked && method === 'score'}
								<div class="mt-1 -ml-1.5 flex flex-wrap" role="group" aria-label="Rate {n.title}">
									{#each Array(maxScore) as _, i (i)}
										<button onclick={() => setScore(n.id, i + 1, mine)} aria-label="{i + 1} of {maxScore}" aria-pressed={sel >= i + 1} class="grid h-9 w-8 place-items-center text-[24px] leading-none {sel >= i + 1 ? 'text-mango' : 'text-line2'}">★</button>
									{/each}
								</div>
							{/if}
						</div>
						{#if blocked}
							<span class="text-sm font-semibold text-faint">Can’t vote for your own</span>
						{:else if method === 'approval'}
							<div class="stepper">
								<button onclick={() => bumpApproval(n.id, -1, mine)} disabled={sel === 0} aria-label="Remove a vote from {n.title}">−</button>
								<span>{sel}</span>
								<button onclick={() => bumpApproval(n.id, 1, mine)} disabled={remaining <= 0 || (maxPer > 0 && sel >= maxPer)} aria-label="Add a vote to {n.title}">+</button>
							</div>
						{/if}
					</div>
				{/if}
			{/each}
		</div>
	{/if}

	{#if poll.results_live && poll.results}
		<div class="mt-6"><LiveResults {poll} /></div>
	{/if}

	<div class="action-bar space-y-2.5">
		{#if error}<p class="text-sm font-bold text-coral-ink" role="alert">{error}</p>{/if}
		{#if isHost && !poll.timer}
			<div class="flex items-center justify-between gap-3">
				<span class="text-sm font-bold text-muted">Hosting · {poll.voter_count} of {poll.participant_count} voted</span>
				<div class="flex shrink-0 items-center gap-2">
					{#if voters.length}
						<button onclick={() => (pickIgnored = !pickIgnored)} aria-expanded={pickIgnored} class="text-sm font-bold text-muted underline">
							Ignore votes{ignored.length ? ` (${ignored.length})` : ''}
						</button>
					{/if}
					<button onclick={async () => update(await api.advance(code, ignored))} class="h-10 rounded-xl border-[1.5px] border-coral px-3.5 text-[15px] font-extrabold text-coral-ink transition hover:bg-coral/10">
						Reveal results
					</button>
				</div>
			</div>
			{#if pickIgnored}
				<fieldset class="flex flex-wrap gap-x-4 gap-y-1.5">
					<legend class="mb-1 text-sm font-semibold text-muted">Don’t count votes from:</legend>
					{#each voters as v (v.id)}
						<label class="flex items-center gap-1.5 text-sm font-bold text-ink">
							<input type="checkbox" value={v.id} bind:group={ignored} class="accent-accent" />
							{v.display_name}
						</label>
					{/each}
				</fieldset>
			{/if}
		{/if}
		<button onclick={submit} disabled={busy} class="btn btn-primary h-[54px] w-full text-[17px]">
			{busy ? 'Saving…' : poll.me?.has_voted ? 'Update my vote' : 'Submit vote'}
		</button>
	</div>
</section>
