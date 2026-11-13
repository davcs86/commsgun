<script lang="ts">
	import { onMount } from 'svelte';
	import { invalidateAll } from '$app/navigation';
	import { page } from '$app/state';
	import ApiError from '#lib/components/ApiError.svelte';
	import Breakdown from '#lib/components/Breakdown.svelte';
	import FilterSheet from '#lib/components/FilterSheet.svelte';
	import OutcomeChart from '#lib/components/OutcomeChart.svelte';
	import ProviderUsage from '#lib/components/ProviderUsage.svelte';
	import RangeChips from '#lib/components/RangeChips.svelte';
	import StatTiles from '#lib/components/StatTiles.svelte';
	import { REFRESH_INTERVAL_MS } from '#lib/constants.js';
	import type { PageProps } from './$types';

	let { data }: PageProps = $props();

	const retryHref = $derived(page.url.pathname + page.url.search);
	let editing = $state(false);
	let refreshing = false;

	onMount(() => {
		const timer = setInterval(() => {
			if (document.visibilityState !== 'visible' || editing || refreshing) return;
			refreshing = true;
			invalidateAll().finally(() => (refreshing = false));
		}, REFRESH_INTERVAL_MS);
		return () => clearInterval(timer);
	});
</script>

<svelte:head>
	<title>Overview · commsgun</title>
</svelte:head>

<main class="mx-auto flex w-full max-w-6xl flex-col gap-4 p-4">
	<div class="flex flex-col gap-2">
		<RangeChips filters={data.filters} />
		<FilterSheet filters={data.filters} facets={data.facets} bind:editing />
	</div>

	{#if data.stats.ok}
		<StatTiles totals={data.stats.data.totals} />
	{:else}
		<ApiError reason={data.stats.reason} {retryHref} />
	{/if}

	<div class="grid min-w-0 gap-4 md:grid-cols-2">
		{#if data.stats.ok}
			<OutcomeChart stats={data.stats.data} />
		{/if}
		{#if data.overview.ok}
			<ProviderUsage providers={data.overview.data.providers} />
		{:else}
			<ApiError reason={data.overview.reason} {retryHref} />
		{/if}
	</div>

	{#if data.stats.ok}
		<div class="grid min-w-0 gap-4 md:grid-cols-2">
			<Breakdown
				title="Errors by type"
				rows={data.stats.data.by_error_type.map((r) => ({ label: r.error_type, count: r.count }))}
			/>
			<Breakdown
				title="By app"
				rows={data.stats.data.by_app.map((r) => ({ label: r.app, count: r.count }))}
			/>
		</div>
	{/if}
</main>
