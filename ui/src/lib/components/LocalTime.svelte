<script lang="ts">
	import { onMount } from 'svelte';
	import { formatCompact, formatLocal, formatUtc } from '#lib/time.js';

	let { iso, compact = false }: { iso: string; compact?: boolean } = $props();

	const date = $derived(new Date(iso));
	const valid = $derived(!Number.isNaN(date.getTime()));
	let hydrated = $state(false);

	const text = $derived.by(() => {
		if (compact) return formatCompact(date, new Date(), hydrated ? undefined : 'UTC');
		return hydrated ? formatLocal(date) : formatUtc(date);
	});

	onMount(() => {
		hydrated = true;
	});
</script>

{#if valid}
	<time datetime={date.toISOString()} data-testid="utc-time">{text}</time>
{:else}
	<span>unknown</span>
{/if}
