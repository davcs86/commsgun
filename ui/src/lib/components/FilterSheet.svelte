<script lang="ts">
	import { afterNavigate, goto } from '$app/navigation';
	import { page } from '$app/state';
	import ApiError from '#lib/components/ApiError.svelte';
	import ZoneName from '#lib/components/ZoneName.svelte';
	import { Button, buttonVariants } from '#lib/components/ui/button/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import * as Select from '#lib/components/ui/select/index.js';
	import * as Sheet from '#lib/components/ui/sheet/index.js';
	import {
		CHANNELS,
		STATES,
		activeFilterCount,
		parseFilters,
		toSearchParams,
		type Filters
	} from '#lib/filters.js';
	import type { ApiResult } from '#lib/server/api.js';
	import { CHANNEL_LABELS, type Facets } from '#lib/types.js';

	let {
		filters,
		facets,
		editing = $bindable(false)
	}: { filters: Filters; facets: ApiResult<Facets>; editing?: boolean } = $props();

	const RANGE_KEYS = ['range', 'from', 'to'];
	const id = $props.id();

	let open = $state(false);
	let customOpen = $state(false);
	let rangeError = $state('');
	// Seeded from the URL on navigation only: a refresh replaces `filters` and must not reset edits.
	let fields = $state(seed(false));

	$effect(() => {
		editing = open || customOpen;
	});

	const path = $derived(page.url.pathname);
	const params = $derived([...toSearchParams(filters)]);
	const rangeParams = $derived(params.filter(([k]) => RANGE_KEYS.includes(k)));
	const otherParams = $derived(params.filter(([k]) => !RANGE_KEYS.includes(k)));
	const count = $derived(activeFilterCount(filters));
	const clearHref = $derived(`${path}?${new URLSearchParams(rangeParams)}`);

	afterNavigate(() => {
		open = false;
		customOpen = false;
		fields = seed(true);
	});

	function seed(local: boolean) {
		const f = filters;
		const input = (d: Date) =>
			f.range !== 'custom'
				? ''
				: local
					? new Date(d.getTime() - d.getTimezoneOffset() * 60_000).toISOString().slice(0, 16)
					: d.toISOString().slice(0, 16);
		return {
			channel: f.channel ?? '',
			app: f.app ?? '',
			state: f.state ?? '',
			errorType: f.errorType ?? '',
			sender: f.sender ?? '',
			recipient: f.recipient ?? '',
			from: input(f.from),
			to: input(f.to)
		};
	}

	function navigate(entries: [string, string][]) {
		const query = toSearchParams(parseFilters(new URLSearchParams(entries), new Date()));
		// eslint-disable-next-line svelte/no-navigation-without-resolve -- same path, new query
		goto(`${path}?${query}`);
	}

	function applyFilters(event: SubmitEvent) {
		event.preventDefault();
		const entries: [string, string][] = [];
		for (const [k, v] of new FormData(event.currentTarget as HTMLFormElement)) {
			if (typeof v === 'string' && v.trim() !== '') entries.push([k, v.trim()]);
		}
		navigate(entries);
	}

	function applyCustom(event: SubmitEvent) {
		event.preventDefault();
		const from = new Date(fields.from);
		const to = new Date(fields.to);
		if (Number.isNaN(from.getTime()) || Number.isNaN(to.getTime()) || from >= to) {
			rangeError = 'From must be before To.';
			return;
		}
		rangeError = '';
		navigate([...otherParams, ['from', from.toISOString()], ['to', to.toISOString()]]);
	}
</script>

{#snippet hidden(entries: [string, string][])}
	{#each entries as [name, value] (name)}
		<input type="hidden" {name} {value} />
	{/each}
{/snippet}

{#snippet choice(
	name: string,
	label: string,
	options: readonly string[],
	value: string,
	set: (v: string) => void,
	text: (v: string) => string
)}
	<div class="flex flex-col gap-2">
		<Label for="{id}-{name}">{label}</Label>
		<Select.Root type="single" {name} {value} onValueChange={set}>
			<Select.Trigger id="{id}-{name}" class="w-full">
				<span class="truncate">{value ? text(value) : 'Any'}</span>
			</Select.Trigger>
			<Select.Content>
				<Select.Item value="" label="Any" />
				{#each options as option (option)}
					<Select.Item value={option} label={text(option)} />
				{/each}
			</Select.Content>
		</Select.Root>
	</div>
{/snippet}

<div class="relative flex flex-wrap gap-2">
	<Sheet.Root bind:open>
		<Sheet.Trigger class={buttonVariants({ variant: 'outline' })}>
			Filters{count > 0 ? ` · ${count}` : ''}
		</Sheet.Trigger>
		<Sheet.Content side="bottom" class="max-h-[90dvh] overflow-y-auto">
			<Sheet.Header>
				<Sheet.Title>Filters</Sheet.Title>
			</Sheet.Header>
			<form
				method="GET"
				action={path}
				onsubmit={applyFilters}
				class="flex flex-col gap-4 px-4 pb-4"
			>
				{@render hidden(rangeParams)}
				{@render choice(
					'channel',
					'Message type',
					CHANNELS,
					fields.channel,
					(v) => (fields.channel = v),
					(v) => CHANNEL_LABELS[v as keyof typeof CHANNEL_LABELS] ?? v
				)}
				{#if facets.ok}
					{@render choice(
						'app',
						'Subscriber app',
						facets.data.apps,
						fields.app,
						(v) => (fields.app = v),
						(v) => v
					)}
				{:else}
					<ApiError reason={facets.reason} />
					<div class="flex flex-col gap-2">
						<Label for="{id}-app">Subscriber app</Label>
						<Input id="{id}-app" name="app" bind:value={fields.app} />
					</div>
				{/if}
				{@render choice(
					'state',
					'State',
					STATES,
					fields.state,
					(v) => (fields.state = v),
					(v) => v
				)}
				{#if facets.ok}
					{@render choice(
						'error_type',
						'Error type',
						facets.data.error_types,
						fields.errorType,
						(v) => (fields.errorType = v),
						(v) => v
					)}
				{:else}
					<div class="flex flex-col gap-2">
						<Label for="{id}-error-type">Error type</Label>
						<Input id="{id}-error-type" name="error_type" bind:value={fields.errorType} />
					</div>
				{/if}
				<div class="flex flex-col gap-2">
					<Label for="{id}-sender">Sender</Label>
					<Input id="{id}-sender" name="sender" bind:value={fields.sender} />
				</div>
				<div class="flex flex-col gap-2">
					<Label for="{id}-recipient">Recipient</Label>
					<Input id="{id}-recipient" name="recipient" bind:value={fields.recipient} />
				</div>
				<div class="flex gap-2">
					<Button type="submit" class="flex-1">Apply</Button>
					<!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- same path, new query -->
					<a href={clearHref} class={buttonVariants({ variant: 'outline' })}>Clear</a>
				</div>
			</form>
		</Sheet.Content>
	</Sheet.Root>

	<details bind:open={customOpen}>
		<summary
			class="{buttonVariants({
				variant: 'outline'
			})} cursor-pointer list-none [&::-webkit-details-marker]:hidden"
		>
			Custom range
		</summary>
		<form
			method="GET"
			action={path}
			onsubmit={applyCustom}
			class="absolute inset-x-0 top-full z-10 mt-2 flex flex-col gap-3 rounded-lg border bg-popover p-4 text-popover-foreground shadow-md"
		>
			{@render hidden(otherParams)}
			<div class="flex flex-col gap-2">
				<Label for="{id}-from">From</Label>
				<Input id="{id}-from" name="from" type="datetime-local" required bind:value={fields.from} />
			</div>
			<div class="flex flex-col gap-2">
				<Label for="{id}-to">To</Label>
				<Input id="{id}-to" name="to" type="datetime-local" required bind:value={fields.to} />
			</div>
			<p class="text-xs text-muted-foreground">Times in <ZoneName /></p>
			{#if rangeError}
				<p role="alert" class="text-sm text-destructive">{rangeError}</p>
			{/if}
			<Button type="submit">Show range</Button>
		</form>
	</details>
</div>
