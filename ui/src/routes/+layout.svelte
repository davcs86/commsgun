<script lang="ts">
	import './layout.css';
	import LogOutIcon from '@lucide/svelte/icons/log-out';
	import RefreshCwIcon from '@lucide/svelte/icons/refresh-cw';
	import { invalidateAll } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import favicon from '#lib/assets/favicon.svg';
	import { Button, buttonVariants } from '#lib/components/ui/button/index.js';
	import type { LayoutProps } from './$types';

	let { data, children }: LayoutProps = $props();

	let refreshing = $state(false);
	const messagesQuery = $derived.by(() => {
		const kept = [...page.url.searchParams].filter(([k]) => k !== 'cursor');
		return kept.length > 0 ? `?${new URLSearchParams(kept)}` : '';
	});

	async function refresh() {
		refreshing = true;
		try {
			await invalidateAll();
		} finally {
			refreshing = false;
		}
	}
</script>

<svelte:head>
	<link rel="icon" href={favicon} />
</svelte:head>

{#if data.authed}
	<header class="flex items-center gap-1 border-b px-4 py-2">
		<h1 class="mr-auto font-semibold"><a href={resolve('/')}>commsgun</a></h1>
		<!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- keeps the current query -->
		<a href="/messages{messagesQuery}" class={buttonVariants({ variant: 'ghost', size: 'sm' })}
			>Messages</a
		>
		<Button
			variant="ghost"
			size="icon"
			aria-label="Refresh"
			disabled={refreshing}
			onclick={refresh}
		>
			<RefreshCwIcon class={refreshing ? 'animate-spin' : ''} />
		</Button>
		<form method="POST" action="/logout">
			<Button type="submit" variant="ghost" size="icon" aria-label="Sign out">
				<LogOutIcon />
			</Button>
		</form>
	</header>
{/if}

{@render children()}
