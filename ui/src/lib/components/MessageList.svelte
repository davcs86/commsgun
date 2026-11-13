<script lang="ts">
	import { resolve } from '$app/paths';
	import LocalTime from '#lib/components/LocalTime.svelte';
	import StateBadge from '#lib/components/StateBadge.svelte';
	import * as Table from '#lib/components/ui/table/index.js';
	import { CHANNEL_LABELS, type Message } from '#lib/types.js';

	let { items }: { items: Message[] } = $props();

	const channel = (m: Message) => CHANNEL_LABELS[m.channel] ?? m.channel;
	const href = (m: Message) => resolve('/messages/[id]', { id: m.id });
</script>

{#if items.length === 0}
	<p class="rounded-xl p-6 text-center text-muted-foreground ring-1 ring-foreground/10">
		No messages match these filters
	</p>
{:else}
	<ul class="flex flex-col gap-2 md:hidden">
		{#each items as m (m.id)}
			<li>
				<a
					href={href(m)}
					data-testid="message-card"
					class="flex flex-col gap-1 rounded-xl bg-card p-3 text-card-foreground ring-1 ring-foreground/10 hover:bg-muted"
				>
					<span class="flex items-center justify-between gap-2">
						<StateBadge state={m.state} />
						<span class="text-sm text-muted-foreground">{channel(m)}</span>
					</span>
					<span class="font-medium break-all">{m.recipient}</span>
					<span class="flex items-baseline justify-between gap-2 text-sm text-muted-foreground">
						<span class="min-w-0 break-all">{m.app}</span>
						<LocalTime iso={m.created_at} compact />
					</span>
				</a>
			</li>
		{/each}
	</ul>

	<div class="hidden md:block">
		<Table.Root class="table-fixed">
			<Table.Header>
				<Table.Row>
					<Table.Head>State</Table.Head>
					<Table.Head>Channel</Table.Head>
					<Table.Head>Recipient</Table.Head>
					<Table.Head>App</Table.Head>
					<Table.Head>Sender</Table.Head>
					<Table.Head>Attempts</Table.Head>
					<Table.Head>Error type</Table.Head>
					<Table.Head>Created</Table.Head>
				</Table.Row>
			</Table.Header>
			<Table.Body>
				{#each items as m (m.id)}
					<Table.Row data-testid="message-row">
						<Table.Cell><StateBadge state={m.state} /></Table.Cell>
						<Table.Cell>{channel(m)}</Table.Cell>
						<Table.Cell class="break-all whitespace-normal">
							<a href={href(m)} class="underline underline-offset-4">{m.recipient}</a>
						</Table.Cell>
						<Table.Cell class="break-all whitespace-normal">{m.app}</Table.Cell>
						<Table.Cell class="break-all whitespace-normal">{m.sender}</Table.Cell>
						<Table.Cell class="tabular-nums">{m.attempts}</Table.Cell>
						<Table.Cell class="break-all whitespace-normal">{m.error_type ?? ''}</Table.Cell>
						<Table.Cell><LocalTime iso={m.created_at} compact /></Table.Cell>
					</Table.Row>
				{/each}
			</Table.Body>
		</Table.Root>
	</div>
{/if}
