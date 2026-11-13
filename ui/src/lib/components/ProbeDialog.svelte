<script lang="ts">
	import SendIcon from '@lucide/svelte/icons/send';
	import { afterNavigate, invalidateAll } from '$app/navigation';
	import * as Alert from '#lib/components/ui/alert/index.js';
	import { Button, buttonVariants } from '#lib/components/ui/button/index.js';
	import * as Dialog from '#lib/components/ui/dialog/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import * as Select from '#lib/components/ui/select/index.js';
	import {
		formatWait,
		validDestination,
		type ProbeInput,
		type ProbeOptions,
		type ProbeResult
	} from '#lib/probe.js';
	import { CHANNEL_LABELS, type Channel } from '#lib/types.js';

	const NO_PROVIDER = 'None configured';
	const id = $props.id();

	let open = $state(false);
	let options = $state<ProbeOptions | 'loading' | 'unavailable'>('loading');
	let channel = $state<Channel>('email');
	let to = $state('');
	let key = $state('');
	let sending = $state(false);
	let result = $state<ProbeResult | null>(null);
	let toInput = $state<HTMLInputElement | null>(null);

	const loaded = $derived(typeof options === 'object' ? options : null);
	const current = $derived(loaded?.channels.find((c) => c.channel === channel) ?? null);
	const ready = $derived(loaded !== null && loaded.available && current !== null);
	const invalidTo = $derived(result?.kind === 'invalid' && result.field === 'to');

	afterNavigate(() => {
		open = false;
	});

	function reset() {
		result = null;
		key = crypto.randomUUID();
	}

	// A quiet reload keeps the form mounted, so focus and the status region survive it.
	async function load(quiet = false) {
		if (!quiet) options = 'loading';
		try {
			const res = await fetch('/probe', { headers: { accept: 'application/json' } });
			if (res.redirected) return location.reload();
			if (!res.ok) throw new Error('unavailable');
			const data = (await res.json()) as ProbeOptions;
			options = data;
			const usable = data.channels.filter((c) => c.enabled);
			if (!usable.some((c) => c.channel === channel) && usable.length > 0) {
				channel = usable[0].channel;
			}
		} catch {
			options = 'unavailable';
		}
	}

	function onOpenChange(next: boolean) {
		if (!next) return;
		to = '';
		reset();
		load();
	}

	function setChannel(next: string) {
		if (next === channel) return;
		channel = next as Channel;
		to = '';
		reset();
	}

	async function send(event: SubmitEvent) {
		event.preventDefault();
		if (sending || !ready || !current) return;
		const input: ProbeInput = { channel, to: to.trim(), provider: current.provider, key };
		if (!validDestination(channel, input.to)) {
			result = { kind: 'invalid', field: 'to' };
			toInput?.focus();
			return;
		}
		sending = true;
		try {
			const res = await fetch('/probe', {
				method: 'POST',
				headers: { 'content-type': 'application/json', accept: 'application/json' },
				body: JSON.stringify(input)
			});
			if (res.redirected) return location.reload();
			result = res.ok ? ((await res.json()) as ProbeResult) : { kind: 'error' };
		} catch {
			result = { kind: 'unavailable' };
		} finally {
			sending = false;
		}
		if (result?.kind === 'queued') {
			key = crypto.randomUUID();
			invalidateAll();
		} else if (result?.kind === 'stale') {
			await load(true);
		} else if (result?.kind === 'invalid' && result.field === 'to') {
			toInput?.focus();
		}
	}
</script>

<Dialog.Root bind:open {onOpenChange}>
	<Dialog.Trigger class={buttonVariants({ variant: 'ghost', size: 'sm' })}>
		<SendIcon />
		<span class="sr-only sm:not-sr-only">Send probe</span>
	</Dialog.Trigger>
	<Dialog.Content class="max-h-[90dvh] overflow-y-auto">
		<Dialog.Header>
			<Dialog.Title>Send a hello-world probe</Dialog.Title>
			<Dialog.Description>
				Sends a fixed test message as app {loaded?.app ?? 'ops'}. The subject and body cannot be
				changed.
			</Dialog.Description>
		</Dialog.Header>

		{#if options === 'loading'}
			<p role="status" class="text-muted-foreground">Loading options…</p>
		{:else if options === 'unavailable'}
			<Alert.Root variant="destructive" role="alert">
				<Alert.Title>The API is unavailable.</Alert.Title>
				<Alert.Description>
					<Button variant="outline" size="sm" onclick={() => load()}>Retry</Button>
				</Alert.Description>
			</Alert.Root>
		{:else if !options.available}
			<Alert.Root variant="destructive" role="alert">
				<Alert.Title>
					App {options.app} is not configured, so probes cannot be sent.
				</Alert.Title>
			</Alert.Root>
		{:else}
			<form class="flex flex-col gap-4" onsubmit={send} novalidate>
				<div class="flex flex-col gap-2">
					<Label for="{id}-channel">Message type</Label>
					<Select.Root type="single" value={channel} onValueChange={setChannel}>
						<Select.Trigger id="{id}-channel" class="w-full">
							<span class="truncate">{CHANNEL_LABELS[channel]}</span>
						</Select.Trigger>
						<Select.Content>
							{#each options.channels as c (c.channel)}
								<Select.Item
									value={c.channel}
									label={CHANNEL_LABELS[c.channel]}
									disabled={!c.enabled}
								/>
							{/each}
						</Select.Content>
					</Select.Root>
				</div>

				<div class="flex flex-col gap-2">
					<Label for="{id}-provider">Provider</Label>
					<Select.Root type="single" value={current?.provider ?? ''} disabled={!current?.provider}>
						<Select.Trigger id="{id}-provider" class="w-full">
							<span class="truncate">{current?.provider ?? NO_PROVIDER}</span>
						</Select.Trigger>
						<Select.Content>
							{#if current?.provider}
								<Select.Item value={current.provider} label={current.provider} />
							{/if}
						</Select.Content>
					</Select.Root>
					{#if current && !current.provider}
						<p id="{id}-held" class="text-muted-foreground">
							No {CHANNEL_LABELS[channel]} provider is configured. The message will be accepted and held,
							not delivered.
						</p>
					{/if}
				</div>

				<div class="flex flex-col gap-2">
					<Label for="{id}-to">
						{channel === 'sms' ? 'Destination phone number' : 'Destination email'}
					</Label>
					<Input
						id="{id}-to"
						type={channel === 'sms' ? 'tel' : 'email'}
						inputmode={channel === 'sms' ? 'tel' : 'email'}
						autocomplete="off"
						autocapitalize="off"
						spellcheck={false}
						placeholder={channel === 'sms' ? '+15551234567' : 'you@example.com'}
						bind:value={to}
						bind:ref={toInput}
						oninput={reset}
						aria-invalid={invalidTo}
						aria-describedby={invalidTo ? `${id}-to-error` : undefined}
					/>
					{#if invalidTo}
						<p id="{id}-to-error" role="alert" class="text-destructive">
							{channel === 'sms'
								? 'Enter a number in international format, for example +15551234567.'
								: 'Enter one valid email address.'}
						</p>
					{/if}
					{#if current?.sender}
						<p class="break-words text-muted-foreground">From: {current.sender}</p>
					{/if}
				</div>

				<!-- Always rendered: a live region that appears together with its text is not announced. -->
				<div role="status" aria-live="polite" class="empty:-mt-4">
					{#if result?.kind === 'queued'}
						<p>
							{result.held ? 'Probe accepted and held.' : 'Probe queued.'}
							<!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- id is validated server-side -->
							<a class="underline" href="/messages/{result.messageId}">View message</a>
						</p>
					{:else if result?.kind === 'rate_limited'}
						<p class="text-destructive">
							{result.window === 'day'
								? `The ${options.app} daily limit is reached.`
								: `The ${options.app} per-minute limit is reached.`}
							Try again in {formatWait(result.retryAfterSeconds)}.
						</p>
					{:else if result?.kind === 'invalid' && result.field === 'from'}
						<p class="text-destructive">
							App {options.app} has no default sender configured.
						</p>
					{:else if result?.kind === 'stale'}
						<p class="text-destructive">
							The configuration changed. Options were reloaded; check them and send again.
						</p>
					{:else if result?.kind === 'unavailable'}
						<p class="text-destructive">The API is unavailable. Try again.</p>
					{:else if result && !invalidTo}
						<p class="text-destructive">The probe could not be sent.</p>
					{/if}
				</div>

				<Dialog.Footer>
					<Dialog.Close class={buttonVariants({ variant: 'outline' })}>Cancel</Dialog.Close>
					<Button type="submit" disabled={sending || !current?.enabled || to.trim() === ''}>
						{sending ? 'Sending…' : 'Send'}
					</Button>
				</Dialog.Footer>
			</form>
		{/if}
	</Dialog.Content>
</Dialog.Root>
