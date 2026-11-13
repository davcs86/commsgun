<script lang="ts">
	import * as Alert from '#lib/components/ui/alert/index.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import * as Card from '#lib/components/ui/card/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import type { PageProps } from './$types';

	let { form }: PageProps = $props();
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
		<Card.Content>
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
		</Card.Content>
	</Card.Root>
</main>
