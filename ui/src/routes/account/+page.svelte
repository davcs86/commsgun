<script lang="ts">
	import ApiError from '#lib/components/ApiError.svelte';
	import * as Alert from '#lib/components/ui/alert/index.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import * as Card from '#lib/components/ui/card/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import type { PageProps } from './$types';

	let { data, form }: PageProps = $props();
</script>

<svelte:head>
	<title>Account · commsgun</title>
</svelte:head>

<main class="flex justify-center p-4">
	<Card.Root class="w-full max-w-sm">
		<Card.Header>
			<Card.Title>Account</Card.Title>
			<Card.Description>Signed in as {data.username}</Card.Description>
		</Card.Header>
		<Card.Content class="flex flex-col gap-4">
			{#if data.outcome}
				<Alert.Root variant={data.outcome.ok ? 'default' : 'destructive'} role="status">
					<Alert.Description>{data.outcome.text}</Alert.Description>
				</Alert.Root>
			{/if}
			{#if data.apiError}
				<ApiError reason={data.apiError} />
			{/if}
			{#if data.showForm}
				<form method="POST" class="flex flex-col gap-4">
					{#if form?.message}
						<Alert.Root variant="destructive" role="alert">
							<Alert.Description>{form.message}</Alert.Description>
						</Alert.Root>
					{/if}
					<p class="text-sm text-muted-foreground">
						Confirm your password, then sign in with the SSO account to link.
					</p>
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
					<Button type="submit">Link SSO</Button>
				</form>
			{:else if data.reason}
				<p class="text-sm text-muted-foreground">{data.reason}</p>
			{/if}
		</Card.Content>
	</Card.Root>
</main>
