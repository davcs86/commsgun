<script lang="ts" module>
	import type { Stats, Totals } from '#lib/types.js';

	// Final outcomes only (`sent` overlaps them); the order keeps neighbours apart in lightness.
	export const OUTCOMES = ['delivered', 'complained', 'bounced', 'failed', 'expired'] as const;
	type Outcome = (typeof OUTCOMES)[number];
	export type Bucket = { t: number } & Record<Outcome, number>;

	export function emptyText(totals: Totals): string | null {
		if (OUTCOMES.some((k) => totals[k] > 0)) return null;
		return totals.enqueued === 0 && totals.sent === 0
			? 'No messages in this range'
			: 'No outcomes yet in this range';
	}

	const MAX_BUCKETS = 1000;

	// The response's from/to/bucket_seconds define the axis; missing points are zero buckets.
	export function buckets(stats: Stats): Bucket[] {
		const from = Date.parse(stats.from);
		const to = Date.parse(stats.to);
		const step = stats.bucket_seconds * 1000;
		if (!Number.isFinite(from) || !Number.isFinite(to) || !(step > 0)) return [];
		const byTime = new Map(stats.series.map((p) => [Date.parse(p.t), p]));
		const out: Bucket[] = [];
		for (let t = from; t < to && out.length < MAX_BUCKETS; t += step) {
			const p = byTime.get(t);
			const row = { t } as Bucket;
			for (const k of OUTCOMES) row[k] = p?.[k] ?? 0;
			out.push(row);
		}
		return out;
	}
</script>

<script lang="ts">
	import { BarChart } from 'layerchart';
	import * as Card from '#lib/components/ui/card/index.js';
	import ZoneName from '#lib/components/ZoneName.svelte';
	import * as Chart from '#lib/components/ui/chart/index.js';
	import { bucketLabel } from '#lib/time.js';

	let { stats }: { stats: Stats } = $props();
	const id = $props.id();

	const config = {
		delivered: { label: 'Delivered', color: 'var(--outcome-delivered)' },
		complained: { label: 'Complained', color: 'var(--outcome-complained)' },
		bounced: { label: 'Bounced', color: 'var(--outcome-bounced)' },
		failed: { label: 'Failed', color: 'var(--outcome-failed)' },
		expired: { label: 'Expired', color: 'var(--outcome-expired)' }
	} satisfies Chart.ChartConfig;

	const series = OUTCOMES.map((key) => ({
		key,
		label: config[key].label,
		color: config[key].color
	}));

	const data = $derived(buckets(stats));
	const empty = $derived(emptyText(stats.totals));
	const summary = $derived(
		`Outcomes per bucket: ${series.map((s) => `${s.label} ${stats.totals[s.key]}`).join(', ')}`
	);
	const label = (t: number) => bucketLabel(new Date(t), stats.bucket_seconds);
</script>

<Card.Root role="region" aria-labelledby={id}>
	<Card.Header>
		<h2 {id} class="text-base font-medium">Outcomes</h2>
	</Card.Header>
	<Card.Content data-testid="outcome-chart">
		{#if empty || data.length === 0}
			<p class="py-8 text-center text-muted-foreground">{empty ?? 'No messages in this range'}</p>
		{:else}
			<Chart.Container {config} class="aspect-auto h-56 w-full" role="img" aria-label={summary}>
				<BarChart
					{data}
					x="t"
					{series}
					seriesLayout="stack"
					bandPadding={0.2}
					props={{
						xAxis: { format: (t: number) => label(t), tickOcclusion: { padding: 8 } },
						bars: { stroke: 'none' }
					}}
				>
					{#snippet tooltip()}
						<Chart.Tooltip labelFormatter={(t: number) => label(t)} />
					{/snippet}
				</BarChart>
			</Chart.Container>
			<p class="mt-1 text-xs text-muted-foreground">Times in <ZoneName /></p>
			<ul
				aria-label="Totals"
				class="mt-3 flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground"
			>
				{#each series as s (s.key)}
					<li class="flex items-center gap-1.5">
						<span class="size-2.5 rounded-[2px]" style:background-color={s.color}></span>
						{s.label}
						<span class="font-medium text-foreground tabular-nums">{stats.totals[s.key]}</span>
					</li>
				{/each}
			</ul>
		{/if}
	</Card.Content>
</Card.Root>
