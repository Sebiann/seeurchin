<script lang="ts">
	import { api } from '$lib/api';
	import type { PollView } from '$lib/types';
	import UrchinMark from './UrchinMark.svelte';
	import JellyfinLogin from './JellyfinLogin.svelte';

	let {
		poll,
		code,
		update
	}: { poll: PollView; code: string; update: (p: PollView) => void } = $props();

	let name = $state('');
	let passcode = $state('');
	let busy = $state(false);
	let error = $state('');
	let showLogin = $state(false);
	let signedInAs = $state('');

	// When signed in to Jellyfin, joining links this entry to the account
	// (the server defaults an empty name to the Jellyfin name).
	$effect(() => {
		if (poll.login_enabled)
			api.userSession().then((s) => {
				if (s.authenticated) signedInAs = s.display_name;
			}, () => {});
	});

	async function join(e?: Event) {
		e?.preventDefault();
		if ((!name.trim() && !signedInAs) || busy) return;
		busy = true;
		error = '';
		try {
			update(await api.join(code, name.trim(), passcode.trim()));
		} catch (err) {
			error = err instanceof Error ? err.message : 'could not join';
		} finally {
			busy = false;
		}
	}

	function loggedIn(s: { display_name: string }) {
		showLogin = false;
		signedInAs = s.display_name;
		join();
	}
</script>

<div class="card mx-auto max-w-sm p-6">
	{#if poll.allow_guests}
		<div class="mb-3.5 flex justify-center"><UrchinMark size={46} /></div>
		<h2 class="text-center font-display text-2xl leading-tight font-bold text-ink [overflow-wrap:anywhere]">Join “{poll.title}”</h2>
		<p class="mt-1.5 text-center text-[15px] font-semibold text-muted">Pick a name so others know who’s voting.</p>
		<form onsubmit={join} class="mt-4 space-y-3">
			<input
				bind:value={name}
				maxlength="40"
				placeholder={signedInAs || 'Your name'}
				autocomplete="off"
				class="input text-center text-lg"
			/>
			{#if poll.passcode_required}
				<input
					bind:value={passcode}
					maxlength="40"
					placeholder="Passcode"
					autocomplete="off"
					class="input text-center"
				/>
			{/if}
			{#if error}<p class="text-sm font-semibold text-coral-ink">{error}</p>{/if}
			<button type="submit" disabled={busy || (!name.trim() && !signedInAs)} class="btn btn-primary w-full">
				{busy ? 'Joining…' : 'Join'}
			</button>
		</form>
		{#if poll.login_enabled}
			{#if signedInAs}
				<p class="mt-3 text-center text-sm font-semibold text-muted">Signed in as {signedInAs}. Joining remembers you on any device.</p>
			{:else if showLogin}
				<div class="mt-4">
					<JellyfinLogin onsuccess={loggedIn} sub="We'll remember you here on any device." />
				</div>
			{:else}
				<button type="button" onclick={() => (showLogin = true)} class="mt-3 w-full text-center text-sm font-bold text-accent-ink hover:underline">Have a Jellyfin account? Sign in instead</button>
			{/if}
		{/if}
	{:else}
		<h2 class="font-title text-lg font-bold text-ink">This poll is closed to guests</h2>
		<p class="mt-1.5 text-sm font-semibold text-muted">
			The host disabled guest access, so you'll need an account to take part. (Account login is
			coming soon.)
		</p>
	{/if}
</div>
