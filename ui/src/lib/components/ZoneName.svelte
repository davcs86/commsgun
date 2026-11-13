<script lang="ts">
	import { onMount } from 'svelte';
	import { zoneName } from '#lib/time.js';

	let zone = $state('UTC');

	onMount(() => {
		zone = zoneName();
	});
</script>

<span data-testid="zone-name">{zone}</span>
