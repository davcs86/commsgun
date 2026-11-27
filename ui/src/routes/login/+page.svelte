<script lang="ts">
	import ApiError from '#lib/components/ApiError.svelte';
	import * as Alert from '#lib/components/ui/alert/index.js';
	import { Button, buttonVariants } from '#lib/components/ui/button/index.js';
	import * as Card from '#lib/components/ui/card/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import type { PageProps } from './$types';

	let { data, form }: PageProps = $props();
</script>

<svelte:head>
	<title>Sign in · commsgun</title>
</svelte:head>

<main class="flex min-h-screen items-center justify-center p-4">
	<Card.Root class="w-full max-w-sm">
		<Card.Header>
			<Card.Title>commsgun</Card.Title>
			<Card.Description>Sign in to the dashboard</Card.Description>
		</Card.Header>
		<Card.Content class="flex flex-col gap-4">
			{#if data.apiError}
				<ApiError reason={data.apiError} />
			{/if}
			{#if data.ssoMessage}
				<Alert.Root variant="destructive" role="alert">
					<Alert.Description>{data.ssoMessage}</Alert.Description>
				</Alert.Root>
			{/if}
			{#if data.options.password}
				<form method="POST" class="flex flex-col gap-4">
					{#if form?.message}
						<Alert.Root variant="destructive" role="alert">
							<Alert.Description>{form.message}</Alert.Description>
						</Alert.Root>
					{/if}
					<div class="flex flex-col gap-2">
						<Label for="username">Username</Label>
						<Input id="username" name="username" autocomplete="username" required />
					</div>
					<div class="flex flex-col gap-2">
						<Label for="password">Password</Label>
						<Input
							id="password"
							name="password"
							type="password"
							autocomplete="current-password"
							required
						/>
					</div>
					<Button type="submit">Sign in</Button>
				</form>
			{:else if form?.message}
				<Alert.Root variant="destructive" role="alert">
					<Alert.Description>{form.message}</Alert.Description>
				</Alert.Root>
			{/if}
			{#if data.options.sso === 'button'}
				<!-- eslint-disable svelte/no-navigation-without-resolve -- a server route that leaves for the IdP -->
				<a
					href={data.ssoHref}
					class={buttonVariants({ variant: data.options.password ? 'outline' : 'default' })}
					data-sveltekit-reload>Sign in with SSO</a
				>
				<!-- eslint-enable svelte/no-navigation-without-resolve -->
			{:else if data.options.sso === 'not_configured'}
				<p class="text-sm text-muted-foreground">SSO is not configured</p>
			{/if}
		</Card.Content>
	</Card.Root>
</main>
