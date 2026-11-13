<script lang="ts">
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import ApiError from '#lib/components/ApiError.svelte';
	import LocalTime from '#lib/components/LocalTime.svelte';
	import StateBadge from '#lib/components/StateBadge.svelte';
	import { formatBytes } from '#lib/format.js';
	import { CHANNEL_LABELS, type Message } from '#lib/types.js';
	import type { PageProps } from './$types';

	let { data }: PageProps = $props();

	const retryHref = $derived(page.url.pathname + page.url.search);
	const capitalize = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);
	const HELD_CHANNEL: Record<string, string> = { email: 'email', sms: 'SMS' };

	function timeline(m: Message) {
		const steps: { label: string; iso: string }[] = [{ label: 'Enqueued', iso: m.created_at }];
		if (m.sent_at) steps.push({ label: 'Sent', iso: m.sent_at });
		if (m.final_at) steps.push({ label: capitalize(m.state), iso: m.final_at });
		return steps;
	}

	function fields(m: Message) {
		return [
			{ label: 'ID', text: m.id },
			{ label: 'Request ID', text: m.request_id },
			{ label: 'App', text: m.app },
			{ label: 'Recipient', text: m.recipient },
			{ label: 'Sender', text: m.sender },
			{
				label: 'Provider',
				text: m.provider
					? m.provider_message_id
						? `${m.provider} · ${m.provider_message_id}`
						: m.provider
					: '—'
			},
			{ label: 'Attempts', text: String(m.attempts) },
			{ label: 'Error type', text: m.error_type ?? '—' },
			{ label: 'Body size', text: formatBytes(m.body_bytes) }
		];
	}

	function times(m: Message) {
		return [
			{ label: 'Created', iso: m.created_at },
			{ label: 'Sent', iso: m.sent_at },
			{ label: 'Final', iso: m.final_at },
			{ label: 'Expires', iso: m.expires_at },
			{ label: 'Next attempt', iso: m.next_attempt_at }
		].filter((t): t is { label: string; iso: string } => t.iso !== null);
	}
</script>

<svelte:head>
	<title>Message · commsgun</title>
</svelte:head>

<main class="mx-auto flex w-full max-w-4xl flex-col gap-4 p-4">
	<a href={resolve('/messages')} class="self-start text-sm underline underline-offset-4"
		>All messages</a
	>

	{#if !data.detail.ok}
		<ApiError reason={data.detail.reason} {retryHref} />
	{:else}
		{@const m = data.detail.data.message}
		{@const attempts = data.detail.data.attempts}
		<div class="flex items-center gap-2">
			<h2 class="text-lg font-semibold">Message</h2>
			<StateBadge state={m.state} />
			<span class="text-sm text-muted-foreground">{CHANNEL_LABELS[m.channel] ?? m.channel}</span>
		</div>

		{#if m.hold_reason === 'no_provider'}
			<div data-testid="held" class="rounded-xl bg-muted p-3 ring-1 ring-foreground/10">
				<p class="font-medium">
					Held: no {HELD_CHANNEL[m.channel] ?? m.channel} provider configured
				</p>
				<p class="text-sm">Expires <LocalTime iso={m.expires_at} /></p>
			</div>
		{/if}

		<dl data-testid="metadata" class="grid gap-3 sm:grid-cols-2">
			{#each fields(m) as f (f.label)}
				<div class="min-w-0">
					<dt class="text-xs text-muted-foreground">{f.label}</dt>
					<dd class="break-all">{f.text}</dd>
				</div>
			{/each}
			{#each times(m) as t (t.label)}
				<div class="min-w-0">
					<dt class="text-xs text-muted-foreground">{t.label}</dt>
					<dd><LocalTime iso={t.iso} /></dd>
				</div>
			{/each}
		</dl>

		<section aria-labelledby="timeline-h" class="flex flex-col gap-2">
			<h3 id="timeline-h" class="font-medium">Timeline</h3>
			<ol data-testid="timeline" class="flex flex-col gap-1">
				{#each timeline(m) as step (step.label)}
					<li class="flex flex-wrap items-baseline gap-x-2">
						<span class="font-medium">{step.label}</span>
						<span class="text-sm text-muted-foreground"><LocalTime iso={step.iso} /></span>
					</li>
				{/each}
			</ol>
		</section>

		<section aria-labelledby="attempts-h" class="flex flex-col gap-2">
			<h3 id="attempts-h" class="font-medium">Attempts</h3>
			{#if attempts.length === 0}
				<p class="text-muted-foreground">No attempts yet</p>
			{:else}
				<ol class="flex flex-col gap-2">
					{#each attempts as a (a.n)}
						<li
							data-testid="attempt"
							class="flex min-w-0 flex-col gap-1 rounded-xl bg-card p-3 text-card-foreground ring-1 ring-foreground/10"
						>
							<div class="flex flex-wrap items-baseline gap-x-3 gap-y-1">
								<span class="font-medium">Attempt {a.n}</span>
								<span class="text-sm text-muted-foreground"><LocalTime iso={a.started_at} /></span>
								<span class="text-sm tabular-nums">{a.duration_ms} ms</span>
							</div>
							<p class="text-sm">
								<span class="font-medium">{a.outcome}</span>
								{#if a.error_type}<span class="break-all"> · {a.error_type}</span>{/if}
								{#if a.provider_status !== null}<span> · HTTP {a.provider_status}</span>{/if}
								{#if a.provider_error}<span class="break-all"> · {a.provider_error}</span>{/if}
							</p>
							{#if a.message}
								<p class="text-sm break-all text-muted-foreground">{a.message}</p>
							{/if}
						</li>
					{/each}
				</ol>
			{/if}
		</section>
	{/if}
</main>
