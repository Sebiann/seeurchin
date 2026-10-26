<script lang="ts">
	import type { PollView } from '$lib/types';

	let { poll }: { poll: PollView } = $props();

	const r = $derived(poll.results);
	const max = $derived(Math.max(1, ...(r?.ranked.map((x) => x.score) ?? [1])));
	const winners = $derived(new Set(r?.winners.map((w) => w.nomination_id) ?? []));
</script>

{#if r}
	<div class="panel p-4">
		<h3 class="section-h">Live tally</h3>
		<div class="mt-3 space-y-2.5">
			{#each r.ranked as e (e.nomination_id)}
				<div>
					<div class="flex items-baseline justify-between gap-2 text-sm font-bold">
						<span class="truncate {winners.has(e.nomination_id) ? 'text-accent-ink' : 'text-muted'}">{e.title}</span>
						<span class="tabular-nums text-faint">{e.vetoed ? 'vetoed' : e.score}</span>
					</div>
					<div class="bar mt-1.5 {winners.has(e.nomination_id) ? 'bar-win' : ''}">
						<i style="width: {(e.score / max) * 100}%"></i>
					</div>
				</div>
			{/each}
		</div>
	</div>
{/if}
