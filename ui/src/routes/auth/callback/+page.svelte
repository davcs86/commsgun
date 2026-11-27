<script lang="ts">
	import { resolve } from '$app/paths';
	import * as Alert from '#lib/components/ui/alert/index.js';
	import { buttonVariants } from '#lib/components/ui/button/index.js';
	import * as Card from '#lib/components/ui/card/index.js';
	import type { PageProps } from './$types';

	let { data }: PageProps = $props();
</script>

<svelte:head>
	<title>Sign in · commsgun</title>
	{#if data.next}
		<meta http-equiv="refresh" content="0; url={data.next}" />
	{/if}
</svelte:head>

<main class="flex min-h-screen items-center justify-center p-4">
	<Card.Root class="w-full max-w-sm">
		<Card.Header>
			<Card.Title>commsgun</Card.Title>
			<Card.Description>{data.next ? 'Signed in' : 'Sign in with SSO'}</Card.Description>
		</Card.Header>
		<Card.Content class="flex flex-col gap-4">
			{#if data.next}
				<!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- next is a safeNext path -->
				<a href={data.next} class={buttonVariants()} data-sveltekit-reload>Continue</a>
			{:else}
				<Alert.Root variant="destructive" role="alert">
					<Alert.Description>{data.message}</Alert.Description>
				</Alert.Root>
				<a
					href={resolve('/login')}
					class={buttonVariants({ variant: 'outline' })}
					data-sveltekit-reload>Back to sign in</a
				>
			{/if}
		</Card.Content>
	</Card.Root>
</main>
