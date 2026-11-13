export const CHANNELS = ['email', 'sms'] as const;
export type Channel = (typeof CHANNELS)[number];
export const CHANNEL_LABELS: Record<Channel, string> = { email: 'Email', sms: 'SMS' };

export const STATES = [
	'queued',
	'sending',
	'sent',
	'delivered',
	'bounced',
	'complained',
	'failed',
	'expired'
] as const;
export type State = (typeof STATES)[number];

export type Totals = {
	enqueued: number;
	sent: number;
	delivered: number;
	bounced: number;
	complained: number;
	failed: number;
	expired: number;
	retries: number;
	queued_now: number;
};

export type SeriesPoint = Omit<Totals, 'queued_now'> & { t: string };

export type Stats = {
	from: string;
	to: string;
	bucket_seconds: number;
	totals: Totals;
	series: SeriesPoint[];
	by_error_type: { error_type: string; count: number }[];
	by_app: { app: string; count: number }[];
	by_channel: { channel: string; count: number }[];
};

export type Message = {
	id: string;
	request_id: string;
	app: string;
	channel: Channel;
	recipient: string;
	sender: string;
	state: string;
	hold_reason: string | null;
	attempts: number;
	error_type: string | null;
	provider: string | null;
	provider_message_id: string | null;
	body_bytes: number;
	created_at: string;
	sent_at: string | null;
	final_at: string | null;
	expires_at: string;
	next_attempt_at: string | null;
};

export type Attempt = {
	n: number;
	started_at: string;
	duration_ms: number;
	outcome: string;
	error_type: string | null;
	provider_status: number | null;
	provider_error: string | null;
	message: string;
};

export type MessagePage = { items: Message[]; next_cursor: string | null };
export type MessageDetail = { message: Message; attempts: Attempt[] };

export type Facets = {
	apps: string[];
	senders: string[];
	error_types: string[];
	channels: string[];
	states: string[];
};

export type ProviderStatus = {
	channel: string;
	name: string | null;
	minute: { used: number; limit: number };
	day: { used: number; limit: number; resets_at: string };
	paused_until: string | null;
	pause_reason: string | null;
	held: number;
};

export type Overview = {
	queue_depth: number;
	oldest_waiting_at: string | null;
	providers: ProviderStatus[];
};
