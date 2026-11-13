<script lang="ts">
	import * as Card from '#lib/components/ui/card/index.js';

	let { title, rows }: { title: string; rows: { label: string; count: number }[] } = $props();
	const id = $props.id();
</script>

<Card.Root role="region" aria-labelledby={id}>
	<Card.Header>
		<h2 {id} class="text-base font-medium">{title}</h2>
	</Card.Header>
	<Card.Content>
		{#if rows.length > 0}
			<ul class="flex flex-col gap-1">
				{#each rows as row, i (i)}
					<li class="flex items-baseline justify-between gap-3">
						<span class="min-w-0 break-all">{row.label}</span>
						<span class="shrink-0 font-medium tabular-nums">{row.count}</span>
					</li>
				{/each}
			</ul>
		{:else}
			<p class="text-muted-foreground">None in this range</p>
		{/if}
	</Card.Content>
</Card.Root>
