<script lang="ts">
	import { page } from '$app/state';
	import ApiError from '#lib/components/ApiError.svelte';
	import FilterSheet from '#lib/components/FilterSheet.svelte';
	import MessageList from '#lib/components/MessageList.svelte';
	import RangeChips from '#lib/components/RangeChips.svelte';
	import { buttonVariants } from '#lib/components/ui/button/index.js';
	import { toSearchParams } from '#lib/filters.js';
	import type { PageProps } from './$types';

	let { data }: PageProps = $props();

	const retryHref = $derived(page.url.pathname + page.url.search);
	const newestHref = $derived(`${page.url.pathname}?${toSearchParams(data.filters)}`);
	const olderHref = $derived.by(() => {
		if (!data.messages.ok || !data.messages.data.next_cursor) return undefined;
		const q = toSearchParams(data.filters);
		q.set('cursor', data.messages.data.next_cursor);
		return `${page.url.pathname}?${q}`;
	});
	const paged = $derived(page.url.searchParams.has('cursor'));
	let editing = $state(false);
</script>

<svelte:head>
	<title>Messages · commsgun</title>
</svelte:head>

<main class="mx-auto flex w-full max-w-6xl flex-col gap-4 p-4">
	<h2 class="text-lg font-semibold">Messages</h2>
	<div class="flex flex-col gap-2">
		<RangeChips filters={data.filters} />
		<FilterSheet filters={data.filters} facets={data.facets} bind:editing />
	</div>

	{#if data.messages.ok}
		<MessageList items={data.messages.data.items} />
		{#if paged || olderHref}
			<nav aria-label="Pages" class="flex gap-2">
				<!-- eslint-disable svelte/no-navigation-without-resolve -- same path, new query -->
				{#if paged}
					<a href={newestHref} class={buttonVariants({ variant: 'outline' })}>Newest</a>
				{/if}
				{#if olderHref}
					<a href={olderHref} class={buttonVariants({ variant: 'outline' })}>Older</a>
				{/if}
				<!-- eslint-enable svelte/no-navigation-without-resolve -->
			</nav>
		{/if}
	{:else}
		<ApiError reason={data.messages.reason} {retryHref} />
	{/if}
</main>
