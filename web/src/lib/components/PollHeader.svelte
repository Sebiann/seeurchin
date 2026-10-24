<script lang="ts">
	import type { PollView } from '$lib/types';
	import UrchinMark from './UrchinMark.svelte';
	import ThemeToggle from './ThemeToggle.svelte';
	import Countdown from './Countdown.svelte';
	import HostTimerControls from './HostTimerControls.svelte';
	import PeopleList from './PeopleList.svelte';

	let {
		poll,
		code,
		update
	}: { poll: PollView; code: string; update: (p: PollView) => void } = $props();

	let copied = $state(false);
	let showPeople = $state(false);

	// Derive the share link from the origin the app was actually loaded from, so
	// it's correct regardless of SEEURCHIN_BASE_URL (localhost, LAN IP, or the
	// public tunnel hostname all just work). Falls back to the server value.
	const shareUrl = $derived(
		typeof window !== 'undefined' ? `${window.location.origin}/p/${poll.code}` : poll.share_url
	);

	const statusLabel: Record<string, string> = {
		draft: 'Draft',
		round1: 'Nominating',
		round2: 'Voting',
		closed: 'Results'
	};
	const statusClass: Record<string, string> = {
		draft: 'pill-draft',
		round1: 'pill-round1',
		round2: 'pill-round2',
		closed: 'pill-closed'
	};

	async function copyLink() {
		try {
			if (navigator.clipboard && window.isSecureContext) {
				await navigator.clipboard.writeText(shareUrl);
			} else {
				// Clipboard API is unavailable on insecure origins (e.g. a LAN IP
				// over http). Fall back to a temporary textarea + execCommand.
				const ta = document.createElement('textarea');
				ta.value = shareUrl;
				ta.style.position = 'fixed';
				ta.style.top = '0';
				ta.style.opacity = '0';
				document.body.appendChild(ta);
				ta.focus();
				ta.select();
				document.execCommand('copy');
				ta.remove();
			}
			copied = true;
			setTimeout(() => (copied = false), 1500);
		} catch {
			// Last resort: show the link so it can be copied by hand.
			window.prompt('Copy this link:', shareUrl);
		}
	}

	// Invite opens the phone's share sheet when there is one (secure origins
	// only), and falls back to copying the link.
	async function invite() {
		if (navigator.share && window.isSecureContext) {
			try {
				await navigator.share({ title: poll.title, text: `Help pick what we watch: ${poll.title}`, url: shareUrl });
				return;
			} catch (e) {
				if (e instanceof DOMException && e.name === 'AbortError') return;
			}
		}
		await copyLink();
	}
</script>

<header class="mb-5">
	<div class="-mx-2 flex h-14 items-center justify-between gap-2">
		<a href="/" class="inline-flex h-11 items-center gap-1.5 px-2 text-accent-ink hover:opacity-80">
			<svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M15 6l-6 6 6 6" /></svg>
			<UrchinMark size={20} />
			<span class="font-display text-lg font-semibold tracking-tight">seeurchin</span>
		</a>
		<div class="flex items-center gap-2 pr-2">
			<button
				type="button"
				onclick={invite}
				class="inline-flex h-10 items-center gap-2 rounded-full bg-primary px-4 text-[15px] font-extrabold text-on-primary transition hover:bg-primary-deep"
			>
				{#if copied}
					<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M5 12l5 5 9-10" /></svg>
					Link copied
				{:else}
					<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 3v12M7 8l5-5 5 5M5 14v5a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2v-5" /></svg>
					Invite
				{/if}
			</button>
			<ThemeToggle />
		</div>
	</div>

	<div class="mt-1 flex flex-wrap items-center gap-x-2.5 gap-y-1">
		<span class="pill {statusClass[poll.status]}">{statusLabel[poll.status]}</span>
		<span class="text-sm font-bold text-muted">
			{#if poll.participants.length}
				<button type="button" onclick={() => (showPeople = !showPeople)} aria-expanded={showPeople} class="rounded px-0.5 underline decoration-line2 decoration-2 underline-offset-4 hover:decoration-accent">{poll.participant_count} {poll.participant_count === 1 ? 'person' : 'people'}</button>{:else}{poll.participant_count} {poll.participant_count === 1 ? 'person' : 'people'}{/if}{poll.status !== 'round1' ? ` · ${poll.voter_count} voted` : ''}
			·
			<button type="button" onclick={copyLink} class="rounded px-0.5 font-extrabold tracking-[0.12em] text-ink underline decoration-line2 decoration-2 underline-offset-4 hover:decoration-accent" title="Copy share link" aria-label="Poll code {poll.code}, copy share link">{poll.code}</button>
		</span>
	</div>
	{#if showPeople}
		<PeopleList {poll} {code} {update} />
	{/if}
	<h1 class="mt-2 font-display text-[28px] leading-[1.15] font-bold tracking-tight text-ink [overflow-wrap:anywhere] sm:text-[32px]">{poll.title}</h1>

	{#if poll.timer}
		<div class="mt-2.5">
			<Countdown
				timer={poll.timer}
				serverNow={poll.server_now}
				kind={poll.status === 'round1' ? 'nominate' : 'vote'}
			/>
		</div>
		{#if poll.me?.is_host}
			<HostTimerControls {poll} {code} {update} />
		{/if}
	{/if}
</header>
