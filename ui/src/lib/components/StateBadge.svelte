<script lang="ts">
	import { Badge } from '#lib/components/ui/badge/index.js';

	let { state }: { state: string } = $props();

	const TOKENS = ['sent', 'delivered', 'bounced', 'complained', 'failed', 'expired'];
	const color = $derived(
		TOKENS.includes(state) ? `var(--outcome-${state})` : 'var(--muted-foreground)'
	);
</script>

<Badge variant="outline" data-testid="state-badge">
	<span aria-hidden="true" class="size-2 shrink-0 rounded-full" style:background-color={color}
	></span>
	{state}
</Badge>
