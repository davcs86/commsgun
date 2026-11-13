<script lang="ts">
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import { buttonVariants } from '#lib/components/ui/button/index.js';
</script>

<svelte:head>
	<title>{page.status === 404 ? 'Not found' : 'Error'} · commsgun</title>
</svelte:head>

<main class="mx-auto flex w-full max-w-xl flex-col items-start gap-4 p-4">
	<h2 class="text-lg font-semibold">
		{page.status === 404 ? 'Not found' : 'Something went wrong'}
	</h2>
	<div class="flex gap-2">
		<a href={resolve('/messages')} class={buttonVariants({ variant: 'outline' })}
			>Back to messages</a
		>
		<a href={resolve('/')} class={buttonVariants({ variant: 'outline' })}>Overview</a>
	</div>
</main>
