<script lang="ts">
	import { page } from '$app/state';
	import { buttonVariants } from '#lib/components/ui/button/index.js';
	import { RANGES, toSearchParams, type Filters, type RangeKey } from '#lib/filters.js';

	let { filters }: { filters: Filters } = $props();

	const href = (range: RangeKey) => `${page.url.pathname}?${toSearchParams({ ...filters, range })}`;
</script>

<nav aria-label="Range" class="-mx-4 overflow-x-auto px-4">
	<ul class="flex w-max gap-2 py-1">
		{#each RANGES as range (range)}
			{@const current = filters.range === range}
			<li>
				<!-- eslint-disable svelte/no-navigation-without-resolve -- same path, new query -->
				<a
					href={href(range)}
					aria-current={current ? 'page' : undefined}
					class={buttonVariants({ variant: current ? 'default' : 'outline', size: 'sm' })}
					>{range}</a
				>
				<!-- eslint-enable svelte/no-navigation-without-resolve -->
			</li>
		{/each}
	</ul>
</nav>
