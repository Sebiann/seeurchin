<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import type { PollView, ResultsView } from '$lib/types';
	import PosterImage from './PosterImage.svelte';
	import { launchConfetti } from '$lib/confetti';

	let {
		poll,
		code = '',
		update
	}: { poll: PollView; code?: string; update?: (p: PollView) => void } = $props();

	// While the "Choose for me" shuffle plays, hold the tied view on screen —
	// the host's own SSE refetch would otherwise swap in the result mid-shuffle.
	let frozen = $state<ResultsView | null>(null);
	const r = $derived(frozen ?? poll.results);
	const isHost = $derived(poll.me?.is_host ?? false);

	let reqError = $state('');
	async function requestWinner(id: string) {
		reqError = '';
		try {
			const p = await api.requestWinner(code, id);
			update?.(p);
		} catch (e) {
			reqError = e instanceof Error ? e.message : 'request failed';
		}
	}
	const nomById = $derived(new Map(poll.nominations.map((n) => [n.id, n])));
	const max = $derived(Math.max(1, ...(r?.ranked.map((x) => x.score) ?? [1])));
	// Winner entries don't always carry a score; the ranked list does.
	const scoreById = $derived(new Map((r?.ranked ?? []).map((e) => [e.nomination_id, e.score])));
	const winnerIds = $derived(new Set(r?.winners.map((w) => w.nomination_id) ?? []));
	const isTie = $derived((r?.winners.length ?? 0) > 1);
	const hasWinner = $derived((r?.winners.length ?? 0) > 0);
	const isRandom = $derived(r?.method === 'random');
	const others = $derived((r?.ranked ?? []).filter((e) => !winnerIds.has(e.nomination_id)));

	// celebratory ring around the winning poster (theme-aware via tokens)
	const ringStyle = (tie: boolean) =>
		tie
			? 'box-shadow: 0 10px 22px -12px rgba(0,0,0,.45), 0 0 0 2px var(--color-sun);'
			: 'box-shadow: 0 16px 32px -14px rgba(0,0,0,.45), 0 0 0 8px color-mix(in srgb, var(--color-sun) 24%, transparent), 0 0 0 3px var(--color-sun);';
	// Approval/ranked scores are vote counts; score-method totals are stars.
	const fmt = (n: number) => (r?.method === 'score' ? `${n} ★` : `${n} ${n === 1 ? 'vote' : 'votes'}`);

	// A broken tie keeps its co-winners so we can say what it was drawn from.
	const tiedFrom = $derived(
		(r?.tied_ids ?? [])
			.filter((id) => !winnerIds.has(id))
			.map((id) => nomById.get(id)?.title)
			.filter(Boolean)
	);
	const label = $derived(
		isRandom
			? 'Picked at random'
			: tiedFrom.length
				? 'Tie broken at random'
				: isTie
					? 'It’s a tie!'
					: 'Tonight you’re watching'
	);

	let confettiHost: HTMLElement;
	onMount(() => {
		if (hasWinner) requestAnimationFrame(() => launchConfetti(confettiHost));
	});

	// Celebrate again when the winner changes live (a tie broken by the host
	// reaches everyone via SSE without remounting this component).
	let lastWinnerKey = '';
	$effect(() => {
		const key = [...winnerIds].join(',');
		if (lastWinnerKey && key !== lastWinnerKey && winnerIds.size === 1) launchConfetti(confettiHost);
		lastWinnerKey = key;
	});

	// --- "Choose for me": the server draws the winner; we play a short
	// spotlight shuffle over the tied posters, then land on its pick.
	let picking = $state(false);
	let spotlight = $state(-1);
	let pickError = $state('');
	const sleep = (ms: number) => new Promise((res) => setTimeout(res, ms));

	async function chooseForMe() {
		if (picking || !r) return;
		picking = true;
		pickError = '';
		frozen = $state.snapshot(r) as ResultsView;
		const tied = r.winners.map((w) => w.nomination_id);
		const calm = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
		let i = 0;
		const spin = calm ? 0 : setInterval(() => (spotlight = i++ % tied.length), 120);
		try {
			const [next] = await Promise.all([api.breakTie(code), sleep(calm ? 0 : 1500)]);
			clearInterval(spin);
			spotlight = tied.indexOf(next.results?.winners[0]?.nomination_id ?? '');
			await sleep(calm ? 0 : 700);
			update?.(next);
		} catch (e) {
			clearInterval(spin);
			pickError = e instanceof Error ? e.message : 'could not pick';
		} finally {
			spotlight = -1;
			picking = false;
			frozen = null;
		}
	}
</script>

<!-- full-viewport overlay the confetti canvas mounts into -->
<div bind:this={confettiHost} class="pointer-events-none fixed inset-0 z-[60]"></div>

<section>
	{#if !r || !hasWinner}
		<div class="rounded-[20px] border border-line bg-surface2 p-8 text-center text-[15px] font-semibold text-muted">
			No votes were cast, so there’s no winner.
		</div>
	{:else}
		<div class="text-center">
			<p class="text-sm font-extrabold tracking-[0.06em] text-sun-ink uppercase" aria-live="polite">{label}</p>
			<!-- Ties sit side by side in one row: up to 3 share the width, more scroll. -->
			<div
				class={isTie && r.winners.length > 3
					? 'genre-scroll -mx-4 mt-5 flex snap-x gap-4 overflow-x-auto px-4 pt-2 pb-3'
					: 'mx-auto mt-5 grid justify-center gap-4 pt-2 sm:gap-6'}
				style={isTie && r.winners.length <= 3 ? `grid-template-columns: repeat(${r.winners.length}, minmax(0, 11rem));` : ''}
			>
				{#each r.winners as w, i (w.nomination_id)}
					{@const n = nomById.get(w.nomination_id)}
					{@const lit = spotlight === i}
					<!-- winner-pop's fill-mode pins transform, so the spotlight scales an inner box -->
					<div class="winner-pop min-w-0 {isTie ? (r.winners.length > 3 ? 'w-32 shrink-0 snap-center' : '') : 'w-44'}">
						<div class="transition duration-150 {picking && !lit ? 'scale-95 opacity-45' : ''} {lit ? 'scale-[1.04]' : ''}">
							{#if n}
								<button type="button" onclick={() => launchConfetti(confettiHost)} class="block w-full cursor-pointer overflow-hidden rounded-[18px] [&_.poster]:rounded-[18px]" style={ringStyle(isTie)} aria-label="Celebrate {n.title} again">
									<PosterImage itemId={n.item_id} tag={n.image_tag} posterUrl={n.poster_url ?? ''} title={n.title} />
								</button>
								<h2 class="mt-4 font-display leading-tight font-bold text-ink [overflow-wrap:anywhere] {isTie ? 'text-lg' : 'text-[28px]'}">{n.title}</h2>
							{:else}
								<h2 class="font-display leading-tight font-bold text-ink {isTie ? 'text-lg' : 'text-[28px]'}">{w.title}</h2>
							{/if}
							<p class="mt-1 font-semibold text-muted {isTie ? 'text-sm' : 'text-[15px]'}">
								{[n?.year || '', !isRandom ? fmt(scoreById.get(w.nomination_id) ?? w.score) : ''].filter(Boolean).join(' · ')}
							</p>
							{#if w.nominators && w.nominators.length > 0}
								<p class="mt-0.5 text-sm font-semibold text-faint">{isTie ? 'By' : 'Nominated by'} {w.nominators.join(', ')}</p>
							{/if}
							{#if w.request_status}
								<p class="mt-2 text-sm font-bold text-mango-ink">Requested via Seerr · {w.request_status}</p>
							{:else if n?.source === 'seerr' && poll.seerr_enabled && isHost && !isTie}
								<button onclick={() => requestWinner(w.nomination_id)} class="mt-3 h-10 w-full rounded-xl bg-mango px-3 text-sm font-extrabold text-[#3a230a] hover:brightness-95">
									Request via Seerr
								</button>
							{/if}
						</div>
					</div>
				{/each}
			</div>

			{#if tiedFrom.length}
				<p class="mt-2 text-sm font-semibold text-faint">Drawn from a tie with {tiedFrom.join(', ')}</p>
			{/if}

			{#if isTie}
				{#if isHost}
					<button onclick={chooseForMe} disabled={picking} class="btn btn-coral mt-6 h-[52px] px-6 text-base">
						<svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linejoin="round" aria-hidden="true"><rect x="4" y="4" width="16" height="16" rx="3" /><circle cx="9" cy="9" r="1.2" fill="currentColor" /><circle cx="15" cy="15" r="1.2" fill="currentColor" /><circle cx="15" cy="9" r="1.2" fill="currentColor" /><circle cx="9" cy="15" r="1.2" fill="currentColor" /></svg>
						{picking ? 'Choosing…' : 'Choose for me'}
					</button>
					{#if pickError}<p class="mt-2 text-sm font-bold text-coral-ink" role="alert">{pickError}</p>{/if}
				{:else}
					<p class="mt-5 text-sm font-semibold text-muted">The host can break the tie with a random pick.</p>
				{/if}
			{/if}
		</div>

		{#if reqError}<p class="mt-3 text-center text-sm font-bold text-coral-ink" role="alert">{reqError}</p>{/if}

		{#if isRandom}
			{#if others.length > 0}
				<div class="card mt-8 p-4">
					<h3 class="section-h">The other nominations</h3>
					<ul class="mt-3 space-y-2 text-[15px] font-semibold text-ink">
						{#each others as e (e.nomination_id)}
							<li>
								{e.title}{#if e.nominators && e.nominators.length > 0}<span class="text-faint"> · {e.nominators.join(', ')}</span>{/if}
							</li>
						{/each}
					</ul>
				</div>
			{/if}
		{:else}
			<div class="card mt-8 p-4">
				<h3 class="section-h">How everyone voted</h3>
				<div class="mt-4 space-y-4">
					{#each r.ranked as e (e.nomination_id)}
						{@const win = winnerIds.has(e.nomination_id)}
						<div>
							<div class="flex items-baseline justify-between gap-2">
								<span class="min-w-0 truncate text-[15px] font-bold {win ? 'text-accent-ink' : 'text-ink'}">{e.title}</span>
								<span class="shrink-0 text-sm font-extrabold tabular-nums {e.vetoed ? 'text-coral-ink' : 'text-muted'}">{e.vetoed ? 'Vetoed' : fmt(e.score)}</span>
							</div>
							<div class="bar mt-1.5 h-2.5 {win ? 'bar-win' : ''}">
								<i style="width: {Math.max(2, (e.score / max) * 100)}%"></i>
							</div>
							{#if e.nominators && e.nominators.length > 0}
								<p class="mt-1 text-[13px] font-semibold text-faint">by {e.nominators.join(', ')}</p>
							{/if}
						</div>
					{/each}
				</div>
				{#if r.method === 'ranked' && r.rounds && r.rounds.length > 1}
					<p class="mt-4 text-sm font-semibold text-faint">
						Decided by instant runoff over {r.rounds.length} rounds. Bars show each title’s final-round support.
					</p>
				{/if}
			</div>
		{/if}
	{/if}

	<div class="action-bar">
		<a href="/" class="btn btn-ghost h-[52px] w-full border-[1.5px] border-line2 bg-transparent text-base text-ink">Start another poll</a>
	</div>
</section>
