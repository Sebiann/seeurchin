<script lang="ts">
	import { onMount } from 'svelte';
	import { goto, pushState } from '$app/navigation';
	import { page } from '$app/state';
	import { api } from '$lib/api';
	import { lockScroll } from '$lib/scrollLock';
	import type { VotingMethod, CreatePollBody, UserSession } from '$lib/types';
	import UrchinMark from '$lib/components/UrchinMark.svelte';
	import ThemeToggle from '$lib/components/ThemeToggle.svelte';
	import JellyfinLogin from '$lib/components/JellyfinLogin.svelte';
	import VotingSettings from '$lib/components/VotingSettings.svelte';

	// The create form shows only the few decisions every host makes (name,
	// what to pick from, how to decide). Everything else lives behind
	// "More options" with sensible defaults, summarized in one line.

	const NAME_KEY = 'seeurchin-name';

	// --- join by code ---
	let joinOpen = $state(false);
	let joinCode = $state('');
	function join(e: Event) {
		e.preventDefault();
		const c = joinCode.trim();
		if (c) goto(`/p/${encodeURIComponent(c)}`);
	}

	// --- create a poll ---
	let methods = $state<VotingMethod[]>([]);
	let customTitle = $state<string | null>(null); // null = follow the default
	let hostName = $state('');
	let editingName = $state(false);
	let nameKnown = $state(false); // show the compact "Hosting as" chip only for a name we already had
	let scope = $state('both');
	let method = $state('approval');
	let config = $state<Record<string, number | boolean | string>>({});
	let nomLimit = $state(0); // 0 = no limit
	let nomExact = $state(false); // with nomLimit > 0: everyone adds exactly that many
	let allowGuests = $state(true);
	let resultsLive = $state(false);
	let reveal = $state<'none' | 'winner' | 'all'>('winner');
	let allGenres = $state<string[]>([]);
	let selectedGenres = $state<string[]>([]);
	let genreError = $state('');
	let showGenres = $state(false);
	let seerrEnabled = $state(false);
	let adminEnabled = $state(false);
	let userLoginEnabled = $state(false); // Jellyfin login required to create a poll
	let loggedIn = $state(false);
	let jellyfinName = $state('');
	let allowWriteins = $state(true);
	let autoRequestWinner = $state(true);
	let passcode = $state(''); // optional per-poll guest passcode
	let deadlineMode = $state<'none' | 'quick' | 'scheduled'>('none');
	let quickR1 = $state(120); // seconds (default 2 min)
	let quickR2 = $state(120);
	let schedR1 = $state(''); // datetime-local strings
	let schedR2 = $state('');
	// The sheet is a shallow-routing entry: opening pushes history state, so
	// browser back / swipe-back closes it instead of leaving the page.
	const optionsOpen = $derived(!!page.state.options);
	const openOptions = () => pushState('', { options: true });
	const closeOptions = () => {
		if (page.state.options) history.back();
	};
	let creating = $state(false);
	let error = $state('');

	// "Movie night · Fri, Sep 25" — so a host can create without typing.
	const today = new Date().toLocaleDateString(undefined, {
		weekday: 'short',
		month: 'short',
		day: 'numeric'
	});
	const defaultTitle = $derived(`${scope === 'series' ? 'Show night' : 'Movie night'} · ${today}`);
	const title = $derived(customTitle ?? defaultTitle);

	onMount(async () => {
		try {
			hostName = localStorage.getItem(NAME_KEY) ?? '';
			nameKnown = !!hostName.trim();
		} catch {
			/* storage blocked — just start empty */
		}
		try {
			methods = await api.methods();
			if (methods.length) selectMethod(method);
		} catch (e) {
			error = e instanceof Error ? e.message : 'could not load voting methods';
		}
		try {
			const features = await api.features();
			seerrEnabled = features.seerr;
			adminEnabled = features.admin;
			userLoginEnabled = features.user_login;
		} catch {
			seerrEnabled = false;
		}
		if (userLoginEnabled) {
			try {
				const sess = await api.userSession();
				if (sess.authenticated) onLogin(sess);
			} catch {
				loggedIn = false;
			}
		}
	});

	function onLogin(s: UserSession) {
		loggedIn = true;
		jellyfinName = s.display_name;
		if (!hostName.trim()) hostName = s.display_name;
		nameKnown = !!hostName.trim();
	}

	function selectMethod(key: string) {
		method = key;
		const m = methods.find((x) => x.key === key);
		config = { ...(m?.default_config ?? {}) } as Record<string, number | boolean | string>;
	}

	// Load the genre list for the chosen scope. Changing scope resets the
	// selection, since movie and show genres differ.
	$effect(() => {
		const s = scope;
		let cancelled = false;
		genreError = '';
		selectedGenres = [];
		api
			.genres(s)
			.then((res) => {
				if (!cancelled) allGenres = res.genres;
			})
			.catch((err) => {
				if (!cancelled) genreError = err instanceof Error ? err.message : 'could not load genres';
			});
		return () => {
			cancelled = true;
		};
	});
	function toggleGenre(g: string) {
		selectedGenres = selectedGenres.includes(g)
			? selectedGenres.filter((x) => x !== g)
			: [...selectedGenres, g];
	}

	// Keep the page behind the options sheet from scrolling (iOS "slip").
	$effect(() => {
		if (optionsOpen) return lockScroll();
	});

	async function create(e: Event) {
		e.preventDefault();
		if (creating) return;
		error = '';
		const name = hostName.trim();
		if (!name) {
			editingName = true;
			error = 'Add your name so people know who’s hosting.';
			return;
		}
		const rules =
			nomLimit > 0 && nomExact
				? { min: 0, max: 0, required: nomLimit }
				: { min: 0, max: nomLimit, required: 0 };
		const body: CreatePollBody = {
			title: title.trim() || defaultTitle,
			host_name: name,
			library_scope: scope,
			voting_method: method,
			voting_config: config,
			submission_rules: rules,
			allow_guests: allowGuests,
			results_live: resultsLive,
			reveal_nominators: reveal !== 'none',
			reveal_scope: reveal === 'all' ? 'all' : 'winner',
			genres: selectedGenres,
			allow_writeins: seerrEnabled && allowWriteins,
			auto_request_winner: seerrEnabled && allowWriteins && autoRequestWinner,
			passcode: allowGuests ? passcode.trim() : ''
		};
		if (deadlineMode === 'quick') {
			body.deadline_mode = 'quick';
			body.round1_duration_sec = quickR1;
			if (!isRandom) body.round2_duration_sec = quickR2;
		} else if (deadlineMode === 'scheduled') {
			body.deadline_mode = 'scheduled';
			if (schedR1) body.round1_closes_at = new Date(schedR1).toISOString();
			if (!isRandom && schedR2) body.round2_closes_at = new Date(schedR2).toISOString();
		}
		creating = true;
		try {
			try {
				localStorage.setItem(NAME_KEY, name);
			} catch {
				/* ignore */
			}
			const poll = await api.createPoll(body);
			goto(`/p/${poll.code}`);
		} catch (err) {
			error = err instanceof Error ? err.message : 'could not create poll';
			creating = false;
		}
	}

	function resetOptions() {
		nomLimit = 0;
		nomExact = false;
		selectedGenres = [];
		allowWriteins = true;
		autoRequestWinner = true;
		selectMethod(method);
		resultsLive = false;
		deadlineMode = 'none';
		quickR1 = 120;
		quickR2 = 120;
		schedR1 = '';
		schedR2 = '';
		allowGuests = true;
		passcode = '';
		reveal = 'winner';
	}

	const num = (v: unknown) => Number(v ?? 0);

	// Random has no voting round, so voting options and round-2 timers are hidden.
	const isRandom = $derived(method === 'random');

	// Friendly names + one-liners for each method; falls back to the server label.
	const methodCopy = $derived<Record<string, { name: string; desc: string }>>({
		approval: {
			name: 'Pick favorites',
			desc:
				num(config.votes_per_user) < 0
					? 'Everyone picks as many as they like. Most votes wins.'
					: `Everyone picks up to ${num(config.votes_per_user) || 3}. Most votes wins.`
		},
		ranked: { name: 'Rank them', desc: 'Everyone orders their favorites. Fairest for bigger groups.' },
		score: { name: 'Rate them', desc: `Everyone gives each title 1–${num(config.max_score) || 5} stars.` },
		random: { name: 'Leave it to chance', desc: 'A random nomination wins. No voting round.' }
	});
	const methodName = $derived(methodCopy[method]?.name ?? methods.find((m) => m.key === method)?.label ?? '');

	const quickPresets: [number, string][] = [
		[30, '30s'],
		[60, '1 min'],
		[120, '2 min'],
		[300, '5 min']
	];
	// "now" in the local-wall-clock format datetime-local expects, as a min bound.
	const minDateTime = new Date(Date.now() - new Date().getTimezoneOffset() * 60000)
		.toISOString()
		.slice(0, 16);

	const summary = $derived.by(() => {
		const parts: string[] = [];
		parts.push(
			deadlineMode === 'quick' ? 'Quick timer' : deadlineMode === 'scheduled' ? 'Scheduled' : 'No timer'
		);
		parts.push(!allowGuests ? 'Signed-in only' : passcode.trim() ? 'Passcode' : 'Guests welcome');
		parts.push(
			nomLimit === 0
				? 'Any number of nominations'
				: `${nomExact ? 'Exactly' : 'Up to'} ${nomLimit} each`
		);
		if (selectedGenres.length) parts.push(`${selectedGenres.length} genre${selectedGenres.length > 1 ? 's' : ''}`);
		return parts.join(' · ');
	});
</script>

<svelte:head><title>seeurchin — group movie night picker</title></svelte:head>
<svelte:window onkeydown={(e) => optionsOpen && e.key === 'Escape' && closeOptions()} />

{#snippet check()}
	<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M5 12l5 5 9-10" /></svg>
{/snippet}

{#snippet methodIcon(key: string)}
	<svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
		{#if key === 'approval'}
			<path d="M9 11l3 3 8-8" /><path d="M20 12v6a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h9" />
		{:else if key === 'ranked'}
			<path d="M10 6h10M10 12h10M10 18h10M4 6h1M4 12h1M4 18h1" />
		{:else if key === 'score'}
			<path d="M12 3l2.7 5.6 6.1.9-4.4 4.3 1 6.1L12 17l-5.4 2.9 1-6.1-4.4-4.3 6.1-.9z" />
		{:else}
			<rect x="4" y="4" width="16" height="16" rx="3" /><circle cx="9" cy="9" r="1.2" fill="currentColor" /><circle cx="15" cy="15" r="1.2" fill="currentColor" /><circle cx="15" cy="9" r="1.2" fill="currentColor" /><circle cx="9" cy="15" r="1.2" fill="currentColor" />
		{/if}
	</svg>
{/snippet}

{#snippet toggle(on: boolean, label: string, sub: string, onclick: () => void)}
	<button type="button" role="switch" aria-checked={on} {onclick} class="settings-row">
		<span><span class="lbl">{label}</span>{#if sub}<span class="sub">{sub}</span>{/if}</span>
		<span class="switch"></span>
	</button>
{/snippet}

<main class="mx-auto max-w-xl px-4">
	<header class="flex h-16 items-center justify-between gap-2">
		<a href="/" class="flex items-center gap-2 text-ink">
			<UrchinMark size={28} />
			<span class="font-display text-[22px] font-semibold tracking-tight">seeurchin</span>
		</a>
		<div class="flex items-center gap-2">
			<button
				type="button"
				onclick={() => (joinOpen = !joinOpen)}
				aria-expanded={joinOpen}
				class="h-10 rounded-full border-[1.5px] border-line2 px-3.5 text-[15px] font-extrabold text-ink transition hover:border-accent"
			>
				Join a poll
			</button>
			<ThemeToggle />
		</div>
	</header>

	{#if joinOpen}
		<form onsubmit={join} class="card mb-4 flex gap-2 p-3">
			<label for="join-code" class="sr-only">Poll code</label>
			<!-- svelte-ignore a11y_autofocus -->
			<input
				id="join-code"
				bind:value={joinCode}
				placeholder="CODE"
				maxlength="6"
				autocomplete="off"
				autocapitalize="characters"
				autofocus
				class="input input-code min-w-0 flex-1"
			/>
			<button type="submit" disabled={!joinCode.trim()} class="btn btn-primary px-5">Join</button>
		</form>
	{/if}

	<div class="mt-2">
		<h1 class="font-display text-[32px] leading-[1.1] font-bold tracking-tight text-ink">Start a movie night</h1>
		<p class="mt-2 text-base leading-relaxed font-medium text-muted text-pretty">
			Share the link, everyone nominates, then you vote. Guests don’t need an account.
		</p>
	</div>

	{#if userLoginEnabled && !loggedIn}
		<!-- Creating a poll (which can request downloads) requires a Jellyfin login. -->
		<div class="mt-6">
			<JellyfinLogin
				heading="Sign in to start a poll"
				sub="Creating a poll uses your Jellyfin account. Guests can still join and vote with just the link."
				onsuccess={onLogin}
			/>
		</div>
	{:else}
		<form onsubmit={create} class="mt-5">
			<div class="card space-y-6 px-[18px] py-5">
				<!-- Poll name — prefilled, so creating needs zero typing -->
				<div>
					<label for="poll-name" class="mb-2 block text-[15px] font-extrabold text-ink">Poll name</label>
					<div class="relative">
						<input
							id="poll-name"
							value={title}
							oninput={(e) => (customTitle = e.currentTarget.value)}
							maxlength="80"
							autocomplete="off"
							placeholder={defaultTitle}
							class="input h-[50px] pr-11 text-[17px] font-bold"
						/>
						{#if title}
							<button
								type="button"
								onclick={() => {
									customTitle = '';
									document.getElementById('poll-name')?.focus();
								}}
								aria-label="Clear poll name"
								class="absolute top-1/2 right-1.5 grid h-9 w-9 -translate-y-1/2 place-items-center rounded-full text-faint hover:text-ink"
							>
								<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round"><path d="M18 6 6 18M6 6l12 12" /></svg>
							</button>
						{/if}
					</div>
					{#if customTitle === null}
						<p class="mt-2 text-sm font-semibold text-faint">Named from today’s date. Keep it or type your own.</p>
					{/if}
				</div>

				<!-- Host identity -->
				{#if nameKnown && hostName.trim() && !editingName}
					<div class="flex items-center gap-3 rounded-[14px] bg-surface2 px-3 py-2.5">
						<div class="grid h-9 w-9 shrink-0 place-items-center rounded-full bg-accent text-base font-extrabold text-on-accent">
							{hostName.trim().charAt(0).toUpperCase()}
						</div>
						<div class="min-w-0 flex-1">
							<div class="truncate text-[15px] font-extrabold text-ink">Hosting as {hostName.trim()}</div>
							<div class="text-[13px] font-semibold text-faint">
								{loggedIn && jellyfinName === hostName.trim() ? 'Signed in with Jellyfin' : 'Remembered on this device'}
							</div>
						</div>
						<button type="button" onclick={() => (editingName = true)} class="h-9 px-2 text-[15px] font-extrabold text-accent-ink">Change</button>
					</div>
				{:else}
					<div>
						<label for="host-name" class="mb-2 block text-[15px] font-extrabold text-ink">Your name</label>
						<input id="host-name" bind:value={hostName} maxlength="40" placeholder="So people know who’s hosting" autocomplete="nickname" class="input" />
					</div>
				{/if}

				<!-- Scope -->
				<div>
					<span id="scope-label" class="mb-2 block text-[15px] font-extrabold text-ink">Pick from</span>
					<div class="seg" role="group" aria-labelledby="scope-label">
						{#each [['both', 'Both'], ['movie', 'Movies'], ['series', 'Shows']] as [val, label] (val)}
							<button type="button" aria-pressed={scope === val} onclick={() => (scope = val)}>{label}</button>
						{/each}
					</div>
				</div>

				<!-- Method -->
				<div>
					<span id="method-label" class="mb-2 block text-[15px] font-extrabold text-ink">How you’ll decide</span>
					<div class="space-y-2" role="group" aria-labelledby="method-label">
						{#each methods as m (m.key)}
							<button type="button" class="choice" aria-pressed={method === m.key} onclick={() => selectMethod(m.key)}>
								<span class="ico">{@render methodIcon(m.key)}</span>
								<span class="min-w-0 flex-1">
									<span class="block text-base font-extrabold">{methodCopy[m.key]?.name ?? m.label}</span>
									<span class="mt-0.5 block text-sm font-semibold text-muted">{methodCopy[m.key]?.desc ?? ''}</span>
								</span>
								<span class="radio">{#if method === m.key}{@render check()}{/if}</span>
							</button>
						{/each}
					</div>
				</div>

				<!-- More options -->
				<button
					type="button"
					onclick={openOptions}
					aria-haspopup="dialog"
					class="-mx-1 flex w-[calc(100%+0.5rem)] items-center gap-3 rounded-[12px] border-t border-line px-1 pt-4 text-left"
				>
					<span class="text-accent-ink">
						<svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" aria-hidden="true"><path d="M4 7h10M18 7h2M4 17h4M12 17h8" /><circle cx="16" cy="7" r="2" /><circle cx="10" cy="17" r="2" /></svg>
					</span>
					<span class="min-w-0 flex-1">
						<span class="block text-base font-extrabold text-ink">More options</span>
						<span class="mt-0.5 block text-sm font-semibold text-faint">{summary}</span>
					</span>
					<svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" class="shrink-0 text-faint" aria-hidden="true"><path d="M9 6l6 6-6 6" /></svg>
				</button>
			</div>

			<div class="action-bar">
				{#if error}<p class="mb-2 text-sm font-bold text-coral-ink" role="alert">{error}</p>{/if}
				<button type="submit" disabled={creating || methods.length === 0} class="btn btn-primary h-[54px] w-full text-[17px]">
					{#if creating}Creating…{:else}Start poll
						<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M5 12h14M13 6l6 6-6 6" /></svg>
					{/if}
				</button>
			</div>
		</form>
	{/if}

	<footer class="pt-6 pb-8 text-center">
		<p class="text-[13px] font-semibold text-faint">Self-hosted movie voting for your Jellyfin library</p>
		{#if adminEnabled}
			<a href="/admin" class="mt-2 inline-block py-2 text-sm font-bold text-accent-ink hover:opacity-80">Admin dashboard →</a>
		{/if}
	</footer>
</main>

{#if optionsOpen}
	<div class="overlay" onclick={closeOptions} aria-hidden="true"></div>
	<div class="sheet" role="dialog" aria-modal="true" aria-labelledby="opts-title">
		<div class="flex justify-center pt-2" aria-hidden="true"><span class="h-[5px] w-10 rounded-full bg-line2"></span></div>
		<div class="flex items-center justify-between py-1.5 pr-2 pl-5">
			<h2 id="opts-title" class="font-display text-2xl font-bold text-ink">More options</h2>
			<button type="button" onclick={closeOptions} class="h-11 px-3 text-base font-extrabold text-accent-ink">Done</button>
		</div>

		<div class="sheet-body space-y-6 px-4 pt-1 pb-[calc(1.5rem+env(safe-area-inset-bottom))]">
			<!-- Nominating -->
			<section>
				<h3 class="settings-title">Nominating</h3>
				<div class="settings">
					<div class="settings-row">
						<span><span class="lbl">Titles per person</span><span class="sub">How many each person can add</span></span>
						<div class="stepper">
							<button type="button" aria-label="Fewer" disabled={nomLimit === 0} onclick={() => (nomLimit = Math.max(0, nomLimit - 1))}>−</button>
							<span class="min-w-[4.5rem]!">{nomLimit === 0 ? 'No limit' : nomLimit}</span>
							<button type="button" aria-label="More" disabled={nomLimit >= 20} onclick={() => (nomLimit += 1)}>+</button>
						</div>
					</div>
					{#if nomLimit > 0}
						{@render toggle(nomExact, `Everyone adds exactly ${nomLimit}`, 'Otherwise it’s up to ' + nomLimit, () => (nomExact = !nomExact))}
					{/if}
					{#if allGenres.length > 0}
						<div>
							<button type="button" class="settings-row" aria-expanded={showGenres} onclick={() => (showGenres = !showGenres)}>
								<span class="lbl">Genres</span>
								<span class="flex items-center gap-1.5 text-[15px] font-bold text-muted">
									{selectedGenres.length === 0 ? 'Any genre' : `${selectedGenres.length} selected`}
									<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" class="text-faint transition-transform {showGenres ? 'rotate-90' : ''}" aria-hidden="true"><path d="M9 6l6 6-6 6" /></svg>
								</span>
							</button>
							{#if showGenres}
								<div class="flex flex-wrap gap-2 px-4 pb-4">
									{#each allGenres as g (g)}
										<button type="button" onclick={() => toggleGenre(g)} class="chip chip-genre" aria-pressed={selectedGenres.includes(g)}>{g}</button>
									{/each}
								</div>
							{/if}
						</div>
					{:else if genreError}
						<div class="settings-row"><span class="sub">Genres unavailable ({genreError})</span></div>
					{/if}
					{#if seerrEnabled}
						{@render toggle(allowWriteins, 'Titles not in the library', 'Requested through Seerr', () => (allowWriteins = !allowWriteins))}
						{#if allowWriteins}
							{@render toggle(autoRequestWinner, 'Auto-request a winning request', 'Sent to Seerr when the poll closes', () => (autoRequestWinner = !autoRequestWinner))}
						{/if}
					{/if}
				</div>
			</section>

			<!-- Voting -->
			<section>
				<h3 class="settings-title">Voting · {methodName}</h3>
				{#if isRandom}
					<p class="panel px-4 py-3.5 text-[15px] font-semibold text-muted">A random nomination is drawn as the winner, so there’s nothing to configure.</p>
				{:else}
					<VotingSettings {method} bind:config bind:resultsLive />
				{/if}
			</section>

			<!-- Timer -->
			<section>
				<h3 class="settings-title">Timer</h3>
				<div class="space-y-3 rounded-2xl border border-line bg-surface p-3">
					<div class="seg" role="group" aria-label="Timer">
						{#each [['none', 'None'], ['quick', 'In the room'], ['scheduled', 'By a date']] as [val, label] (val)}
							<button type="button" aria-pressed={deadlineMode === val} onclick={() => (deadlineMode = val as typeof deadlineMode)}>{label}</button>
						{/each}
					</div>
					<p class="px-1 text-sm leading-snug font-semibold text-muted">
						{#if deadlineMode === 'none'}
							You move each round along yourself.
						{:else if deadlineMode === 'quick'}
							Short rounds for when everyone’s together. You start the clock; it moves on when time’s up.
						{:else}
							People take part on their own time. Leave a round blank to close it yourself.
						{/if}
					</p>
					{#if deadlineMode === 'quick'}
						<div class="space-y-3 px-1">
							<div>
								<span class="mb-1.5 block text-sm font-bold text-ink">Time to nominate</span>
								<div class="seg">
									{#each quickPresets as [sec, label] (sec)}
										<button type="button" aria-pressed={quickR1 === sec} onclick={() => (quickR1 = sec)}>{label}</button>
									{/each}
								</div>
							</div>
							{#if !isRandom}
								<div>
									<span class="mb-1.5 block text-sm font-bold text-ink">Time to vote</span>
									<div class="seg">
										{#each quickPresets as [sec, label] (sec)}
											<button type="button" aria-pressed={quickR2 === sec} onclick={() => (quickR2 = sec)}>{label}</button>
										{/each}
									</div>
								</div>
							{/if}
						</div>
					{:else if deadlineMode === 'scheduled'}
						<div class="grid gap-3 px-1 sm:grid-cols-2">
							<label class="block">
								<span class="mb-1.5 block text-sm font-bold text-ink">Nominations close</span>
								<input type="datetime-local" min={minDateTime} bind:value={schedR1} class="input" />
							</label>
							{#if !isRandom}
								<label class="block">
									<span class="mb-1.5 block text-sm font-bold text-ink">Voting closes</span>
									<input type="datetime-local" min={minDateTime} bind:value={schedR2} class="input" />
								</label>
							{/if}
						</div>
					{/if}
				</div>
			</section>

			<!-- Access -->
			<section>
				<h3 class="settings-title">Who can join</h3>
				<div class="settings">
					{@render toggle(allowGuests, 'Guests with the link', 'No account needed', () => (allowGuests = !allowGuests))}
					{#if allowGuests}
						<div class="settings-row">
							<label for="passcode" class="lbl">Passcode</label>
							<input id="passcode" bind:value={passcode} autocomplete="off" maxlength="40" placeholder="Optional" class="input h-10 w-40 px-3 py-0 text-right" />
						</div>
					{/if}
					<div class="settings-row flex-wrap">
						<span class="lbl">Show who nominated</span>
						<div class="seg w-full" role="group" aria-label="Show who nominated">
							{#each [['none', 'Nobody'], ['winner', 'Winner only'], ['all', 'Every title']] as [val, label] (val)}
								<button type="button" aria-pressed={reveal === val} onclick={() => (reveal = val as typeof reveal)}>{label}</button>
							{/each}
						</div>
					</div>
				</div>
			</section>

			<div class="flex justify-center">
				<button type="button" onclick={resetOptions} class="h-11 px-4 text-[15px] font-extrabold text-muted hover:text-ink">Reset to defaults</button>
			</div>
		</div>
	</div>
{/if}
