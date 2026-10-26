<script lang="ts">
	import { api } from '$lib/api';
	import { lockScroll } from '$lib/scrollLock';
	import type { PollView } from '$lib/types';
	import VotingSettings from './VotingSettings.svelte';

	let {
		poll,
		code,
		update,
		close
	}: { poll: PollView; code: string; update: (p: PollView) => void; close: () => void } = $props();

	// Edit a copy; nothing changes for anyone until Save.
	let config = $state({ ...(poll.voting_config ?? {}) } as Record<string, number | boolean | string>);
	let resultsLive = $state(poll.results_live);
	let busy = $state(false);
	let error = $state('');

	$effect(() => lockScroll());

	async function save() {
		busy = true;
		error = '';
		try {
			update(await api.updateSettings(code, config, resultsLive));
			close();
		} catch (err) {
			error = err instanceof Error ? err.message : 'could not save settings';
		} finally {
			busy = false;
		}
	}
</script>

<svelte:window onkeydown={(e) => e.key === 'Escape' && close()} />

<div class="overlay" onclick={close} aria-hidden="true"></div>
<div class="sheet" role="dialog" aria-modal="true" aria-labelledby="poll-settings-title">
	<div class="flex justify-center pt-2" aria-hidden="true"><span class="h-[5px] w-10 rounded-full bg-line2"></span></div>
	<div class="flex items-center justify-between py-1.5 pr-2 pl-5">
		<h2 id="poll-settings-title" class="font-display text-2xl font-bold text-ink">Settings</h2>
		<button type="button" onclick={close} class="h-11 px-3 text-base font-extrabold text-muted">Cancel</button>
	</div>

	<div class="sheet-body space-y-4 px-4 pt-1 pb-[calc(1.5rem+env(safe-area-inset-bottom))]">
		<section>
			<h3 class="settings-title">Voting</h3>
			<VotingSettings method={poll.voting_method} bind:config bind:resultsLive />
		</section>
		{#if poll.status === 'round2'}
			<p class="px-1 text-sm font-semibold text-muted">Votes already cast are kept, so only changes that fit every ballot can be saved.</p>
		{/if}
		{#if error}<p class="px-1 text-sm font-bold text-coral-ink" role="alert">{error}</p>{/if}
		<button type="button" onclick={save} disabled={busy} class="btn btn-primary h-[54px] w-full text-[17px]">{busy ? 'Saving…' : 'Save'}</button>
	</div>
</div>
