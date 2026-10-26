<script lang="ts">
	// The per-method voting rows, shared by the create page's options sheet
	// and the host's settings sheet on a running poll.
	let {
		method,
		config = $bindable(),
		resultsLive = $bindable()
	}: { method: string; config: Record<string, number | boolean | string>; resultsLive: boolean } = $props();

	const num = (v: unknown) => Number(v ?? 0);

	// Self-vote maps to the method config's max_self_votes (-1 unlimited,
	// 0 none). A legacy allow_self_vote=false still reads as "off".
	const selfVoteOn = $derived.by(() => {
		const m = config.max_self_votes;
		if (m === undefined || m === null) return config.allow_self_vote !== false;
		return Number(m) !== 0;
	});
	function toggleSelfVote() {
		config.max_self_votes = selfVoteOn ? 0 : -1;
	}
	// Steppers run min..max, then one more step to -1 ("Any" = unlimited).
	function stepUp(key: string, max: number) {
		const v = num(config[key]);
		config[key] = v >= max ? -1 : v + 1;
	}
	function stepDown(key: string, min: number, max: number) {
		const v = num(config[key]);
		config[key] = v < 0 ? max : Math.max(min, v - 1);
	}

	// Approval "stacking": max_votes_per_option 1 = one vote per title, 0 = no cap.
	const stackOn = $derived(num(config.max_votes_per_option) !== 1);
	function toggleStack() {
		config.max_votes_per_option = stackOn ? 1 : 0;
	}
</script>

{#snippet toggle(on: boolean, label: string, sub: string, onclick: () => void)}
	<button type="button" role="switch" aria-checked={on} {onclick} class="settings-row">
		<span><span class="lbl">{label}</span>{#if sub}<span class="sub">{sub}</span>{/if}</span>
		<span class="switch"></span>
	</button>
{/snippet}

	<div class="settings">
		{#if method === 'approval'}
			<div class="settings-row">
				<span class="lbl">Picks per person</span>
				<div class="stepper">
					<button type="button" aria-label="Fewer" disabled={num(config.votes_per_user) === 1} onclick={() => stepDown('votes_per_user', 1, 20)}>−</button>
					<span>{num(config.votes_per_user) < 0 ? 'Any' : num(config.votes_per_user)}</span>
					<button type="button" aria-label="More" disabled={num(config.votes_per_user) < 0} onclick={() => stepUp('votes_per_user', 20)}>+</button>
				</div>
			</div>
			{#if num(config.votes_per_user) > 0}
				{@render toggle(stackOn, 'Stack votes on one title', 'Spend several picks on a favorite', toggleStack)}
			{/if}
		{:else if method === 'ranked'}
			<div class="settings-row">
				<span><span class="lbl">How many to rank</span><span class="sub">“All” ranks every title</span></span>
				<div class="stepper">
					<button type="button" aria-label="Fewer" disabled={num(config.max_ranked) <= 0} onclick={() => (config.max_ranked = Math.max(0, num(config.max_ranked) - 1))}>−</button>
					<span>{num(config.max_ranked) === 0 ? 'All' : num(config.max_ranked)}</span>
					<button type="button" aria-label="More" disabled={num(config.max_ranked) >= 20} onclick={() => (config.max_ranked = num(config.max_ranked) + 1)}>+</button>
				</div>
			</div>
		{:else if method === 'score'}
			<div class="settings-row">
				<span class="lbl">Top rating</span>
				<div class="stepper">
					<button type="button" aria-label="Fewer" disabled={num(config.max_score) <= 2} onclick={() => (config.max_score = Math.max(2, num(config.max_score) - 1))}>−</button>
					<span>{num(config.max_score)}★</span>
					<button type="button" aria-label="More" disabled={num(config.max_score) >= 10} onclick={() => (config.max_score = num(config.max_score) + 1)}>+</button>
				</div>
			</div>
			<div class="settings-row">
				<span class="lbl">Winner by</span>
				<div class="seg w-44" role="group" aria-label="Winner by">
					<button type="button" aria-pressed={(config.aggregate ?? 'total') === 'total'} onclick={() => (config.aggregate = 'total')}>Total</button>
					<button type="button" aria-pressed={config.aggregate === 'average'} onclick={() => (config.aggregate = 'average')}>Average</button>
				</div>
			</div>
		{/if}
		<div class="settings-row">
			<span><span class="lbl">Vetoes per person</span><span class="sub">A veto knocks a title out</span></span>
			<div class="stepper">
				<button type="button" aria-label="Fewer" disabled={num(config.max_vetoes) === 0} onclick={() => stepDown('max_vetoes', 0, 10)}>−</button>
				<span>{num(config.max_vetoes) === 0 ? 'Off' : num(config.max_vetoes) < 0 ? 'Any' : num(config.max_vetoes)}</span>
				<button type="button" aria-label="More" disabled={num(config.max_vetoes) < 0} onclick={() => stepUp('max_vetoes', 10)}>+</button>
			</div>
		</div>
		{@render toggle(selfVoteOn, 'Vote for your own picks', selfVoteOn ? '' : 'Keeps it fair', toggleSelfVote)}
		{@render toggle(resultsLive, 'Show live results', 'Everyone sees the tally while voting', () => (resultsLive = !resultsLive))}
	</div>
