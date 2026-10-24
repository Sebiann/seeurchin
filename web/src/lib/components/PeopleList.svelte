<script lang="ts">
	import { api } from '$lib/api';
	import type { PollView } from '$lib/types';
	import JellyfinLogin from './JellyfinLogin.svelte';

	let {
		poll,
		code,
		update
	}: { poll: PollView; code: string; update: (p: PollView) => void } = $props();

	let busy = $state(false);
	let error = $state('');
	let afterLogin = $state<(() => Promise<void>) | null>(null);
	let users = $state<{ id: string; name: string }[] | null>(null);
	let target = $state(''); // participant the host is linking
	let account = $state('');

	const open = $derived(poll.status !== 'closed');
	const canHostLink = $derived(poll.login_enabled && !!poll.me?.is_host && open);
	const hasDuplicates = $derived(
		new Set(poll.participants.map((p) => p.display_name)).size < poll.participants.length
	);
	const targetName = $derived(poll.participants.find((p) => p.id === target)?.display_name ?? '');

	async function run(fn: () => Promise<void>) {
		busy = true;
		error = '';
		try {
			await fn();
		} catch (err) {
			error = err instanceof Error ? err.message : 'something went wrong';
		} finally {
			busy = false;
		}
	}

	// Runs fn once this browser is signed in to Jellyfin, asking to sign in first if needed.
	async function signedIn(fn: () => Promise<void>) {
		if ((await api.userSession()).authenticated) await fn();
		else afterLogin = fn;
	}

	function loggedIn() {
		const fn = afterLogin;
		afterLogin = null;
		if (fn) run(fn);
	}

	function linkMe() {
		run(() => signedIn(async () => update(await api.link(code))));
	}

	function pickTarget(id: string) {
		target = target === id ? '' : id;
		account = '';
		if (target && !users) run(() => signedIn(async () => void (users = await api.jellyfinUsers(code))));
	}

	function hostLink() {
		run(async () => {
			update(await api.hostLink(code, target, account));
			target = '';
		});
	}
</script>

<ul class="mt-2 flex flex-wrap gap-1.5" aria-label="People in this poll">
	{#each poll.participants as p (p.id)}
		{@const label = `${p.display_name}${p.linked ? ' 🔗' : ''}${p.is_host ? ' · host' : ''}${poll.status !== 'round1' && p.has_voted ? ' · voted ✓' : ''}`}
		<li>
			{#if canHostLink}
				<button type="button" onclick={() => pickTarget(p.id)} aria-expanded={target === p.id} class="pill {target === p.id ? 'ring-2 ring-accent' : ''}" title="Link to a Jellyfin account">{label}</button>
			{:else}
				<span class="pill">{label}</span>
			{/if}
		</li>
	{/each}
</ul>

{#if target && users}
	<div class="mt-2 flex flex-wrap items-center gap-2">
		<select bind:value={account} class="input w-auto py-1.5 text-sm" aria-label="Jellyfin account for {targetName}">
			<option value="" disabled>Jellyfin account…</option>
			{#each users as u (u.id)}<option value={u.id}>{u.name}</option>{/each}
		</select>
		<button type="button" onclick={hostLink} disabled={busy || !account} class="btn btn-primary btn-sm">Link {targetName}</button>
		<button type="button" onclick={() => (target = '')} class="btn btn-ghost btn-sm">Cancel</button>
	</div>
{/if}

{#if open && ((poll.me?.is_host && hasDuplicates) || (poll.login_enabled && poll.me && !poll.me.linked))}
	<div class="mt-2 flex flex-wrap gap-2">
		{#if poll.me?.is_host && hasDuplicates}
			<button type="button" onclick={() => run(async () => update(await api.mergeDuplicates(code)))} disabled={busy} class="btn btn-ghost btn-sm" title="Combine people with exactly the same name, keeping one ballot each">Merge same names</button>
		{/if}
		{#if poll.login_enabled && poll.me && !poll.me.linked}
			<button type="button" onclick={linkMe} disabled={busy} class="btn btn-ghost btn-sm" title="Get back into this poll from any device by signing in">Link my Jellyfin</button>
		{/if}
	</div>
{/if}

{#if afterLogin}
	<div class="mt-3">
		<JellyfinLogin onsuccess={loggedIn} sub="Sign in so this poll remembers you on any device." />
	</div>
{/if}
{#if error}<p class="mt-1.5 text-xs font-semibold text-coral-ink">{error}</p>{/if}
