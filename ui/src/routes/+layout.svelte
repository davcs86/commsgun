<script lang="ts">
	import './layout.css';
	import favicon from '#lib/assets/favicon.svg';
	import { Button } from '#lib/components/ui/button/index.js';
	import type { LayoutProps } from './$types';

	let { data, children }: LayoutProps = $props();
</script>

<svelte:head>
	<link rel="icon" href={favicon} />
</svelte:head>

{#if data.authed}
	<header class="flex items-center justify-between border-b px-4 py-2">
		<span class="font-semibold">commsgun</span>
		<form method="POST" action="/logout">
			<Button type="submit" variant="outline" size="sm">Sign out</Button>
		</form>
	</header>
{/if}

{@render children()}
