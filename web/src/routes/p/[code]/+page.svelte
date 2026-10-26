<script lang="ts">
	import { page } from '$app/state';
	import { api } from '$lib/api';
	import type { PollView } from '$lib/types';
	import PollHeader from '$lib/components/PollHeader.svelte';
	import JoinForm from '$lib/components/JoinForm.svelte';
	import Round1 from '$lib/components/Round1.svelte';
	import Ballot from '$lib/components/Ballot.svelte';
	import Results from '$lib/components/Results.svelte';

	const code = $derived(page.params.code ?? '');

	let poll = $state<PollView | null>(null);
	let error = $state('');
	let loading = $state(true);

	async function refresh(c: string) {
		try {
			poll = await api.getPoll(c);
			error = '';
		} catch (e) {
			error = e instanceof Error ? e.message : 'failed to load poll';
		} finally {
			loading = false;
		}
	}

	function update(p: PollView) {
		poll = p;
	}

	// Load + live-subscribe; re-run if the route code changes.
	$effect(() => {
		const c = code;
		refresh(c);
		const es = api.events(c);
		es.addEventListener('update', () => refresh(c));
		return () => es.close();
	});
</script>

<svelte:head><title>{poll ? poll.title : 'seeurchin'}</title></svelte:head>

<main class="mx-auto max-w-3xl px-4 pb-4">
	{#if loading}
		<p class="py-20 text-center font-semibold text-muted">Loading…</p>
	{:else if error}
		<div class="py-20 text-center">
			<p class="font-semibold text-coral-ink">{error}</p>
			<a href="/" class="mt-4 inline-block font-bold text-accent hover:underline">← Back home</a>
		</div>
	{:else if poll}
		<PollHeader {poll} {code} {update} />
		{#if !poll.me}
			<JoinForm {poll} {code} {update} />
		{:else if poll.status === 'round1'}
			<Round1 {poll} {code} {update} />
		{:else if poll.status === 'round2'}
			<!-- Ballot reads its rules once; remount when the host edits them. -->
			{#key JSON.stringify(poll.voting_config)}
				<Ballot {poll} {code} {update} />
			{/key}
		{:else}
			<Results {poll} {code} {update} />
		{/if}
	{/if}
</main>
