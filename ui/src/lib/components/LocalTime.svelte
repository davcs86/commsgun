<script lang="ts">
	import { onMount } from 'svelte';
	import { formatLocal, formatUtc } from '#lib/time.js';

	let { iso }: { iso: string } = $props();

	const date = $derived(new Date(iso));
	const valid = $derived(!Number.isNaN(date.getTime()));
	let hydrated = $state(false);

	onMount(() => {
		hydrated = true;
	});
</script>

{#if valid}
	<time datetime={date.toISOString()} data-testid="utc-time"
		>{hydrated ? formatLocal(date) : formatUtc(date)}</time
	>
{:else}
	<span>unknown</span>
{/if}
