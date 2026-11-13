<script lang="ts">
	import LocalTime from '#lib/components/LocalTime.svelte';
	import * as Card from '#lib/components/ui/card/index.js';
	import { CHANNEL_LABELS, type ProviderStatus } from '#lib/types.js';

	let { providers }: { providers: ProviderStatus[] } = $props();
	const id = $props.id();

	const PAUSE_REASONS: Record<string, string> = {
		daily_quota: 'Daily quota reached',
		monthly_quota: 'Monthly quota reached',
		rate_limit: 'Rate limited',
		auth: 'Provider rejected the API key'
	};

	const channelLabel = (c: string) => CHANNEL_LABELS[c as keyof typeof CHANNEL_LABELS] ?? c;
	const percent = (used: number, limit: number) => Math.min(100, Math.max(0, (used / limit) * 100));
</script>

<Card.Root role="region" aria-labelledby={id}>
	<Card.Header>
		<h2 {id} class="text-base font-medium">Provider usage</h2>
	</Card.Header>
	<Card.Content>
		<ul class="flex flex-col gap-4">
			{#each providers as p (p.channel)}
				<li class="flex flex-col gap-1">
					<div class="flex flex-wrap items-baseline justify-between gap-x-3">
						<span class="font-medium">{channelLabel(p.channel)}</span>
						{#if p.name === null}
							<span>No provider · {p.held} held</span>
						{:else}
							<span class="min-w-0 break-all">{p.name}</span>
						{/if}
					</div>
					{#if p.name !== null}
						{#if p.day.limit > 0}
							<p class="tabular-nums">{p.day.used} / {p.day.limit} today</p>
							<div
								role="progressbar"
								aria-label="{p.name} daily usage"
								aria-valuemin={0}
								aria-valuemax={p.day.limit}
								aria-valuenow={p.day.used}
								class="h-2 overflow-hidden rounded-full bg-muted"
							>
								<div
									class="h-full rounded-full bg-primary"
									style:width="{percent(p.day.used, p.day.limit)}%"
								></div>
							</div>
						{:else}
							<p class="tabular-nums">{p.day.used} today</p>
						{/if}
						<p class="text-muted-foreground">resets <LocalTime iso={p.day.resets_at} compact /></p>
						<p class="text-xs text-muted-foreground tabular-nums">
							{p.minute.used} / {p.minute.limit} this minute
						</p>
						{#if p.pause_reason}
							<p class="font-medium">
								<span>{PAUSE_REASONS[p.pause_reason] ?? p.pause_reason}</span>
								{#if p.paused_until}
									· <span>resumes <LocalTime iso={p.paused_until} compact /></span>
								{/if}
							</p>
						{/if}
						{#if p.held > 0}
							<p>{p.held} held</p>
						{/if}
					{/if}
				</li>
			{/each}
		</ul>
	</Card.Content>
</Card.Root>
