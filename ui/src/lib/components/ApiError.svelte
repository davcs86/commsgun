<script lang="ts">
	import * as Alert from '#lib/components/ui/alert/index.js';

	type Reason = 'unavailable' | 'error' | 'not_found';
	let { reason, retryHref }: { reason: Reason; retryHref?: string } = $props();

	const MESSAGES: Record<Reason, string> = {
		unavailable: 'The delivery API is not responding',
		error: 'The delivery API returned an error',
		not_found: 'Not found'
	};
</script>

<Alert.Root variant="destructive">
	<Alert.Title>{MESSAGES[reason]}</Alert.Title>
	{#if retryHref}
		<Alert.Description>
			<!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- caller passes the current page URL -->
			<a class="underline" href={retryHref}>Retry</a>
		</Alert.Description>
	{/if}
</Alert.Root>
