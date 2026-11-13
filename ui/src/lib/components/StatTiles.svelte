<script lang="ts">
	import type { Totals } from '#lib/types.js';

	let { totals }: { totals: Totals } = $props();
	const noteId = $props.id();

	const tiles = $derived([
		{ label: 'Delivered', value: totals.delivered },
		{ label: 'Sent', value: totals.sent },
		{ label: 'Queued', value: totals.queued_now },
		{ label: 'Bounced', value: totals.bounced },
		{
			label: 'Failed',
			value: totals.failed + totals.expired,
			note: totals.expired > 0 ? `incl. ${totals.expired} expired` : undefined
		},
		{ label: 'Retries', value: totals.retries }
	]);
</script>

<dl
	data-testid="stat-tiles"
	aria-describedby={noteId}
	class="grid grid-cols-3 gap-2 md:grid-cols-6"
>
	{#each tiles as tile (tile.label)}
		<div
			class="flex flex-col rounded-xl bg-card p-3 text-card-foreground ring-1 ring-foreground/10"
		>
			<dt class="text-xs text-muted-foreground">{tile.label}</dt>
			<dd class="text-2xl font-semibold tabular-nums">{tile.value}</dd>
			{#if tile.note}
				<dd class="text-xs text-muted-foreground">{tile.note}</dd>
			{/if}
		</div>
	{/each}
</dl>
<p id={noteId} class="mt-1 text-xs text-muted-foreground">
	Sent = accepted by the provider; Delivered = confirmed later
</p>
