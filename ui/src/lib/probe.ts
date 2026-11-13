import { PROBE_DESTINATION_MAX } from './constants.js';
import { CHANNELS, type Channel } from './types.js';

export type ProbeChannel = {
	channel: Channel;
	enabled: boolean;
	provider: string | null;
	sender: string | null;
};
export type ProbeOptions = { app: string; available: boolean; channels: ProbeChannel[] };
export type ProbeInput = { channel: Channel; to: string; provider: string | null; key: string };
export type ProbeResult =
	| { kind: 'queued'; messageId: string; held: boolean }
	| { kind: 'invalid'; field: 'to' | 'from' | 'other' }
	| { kind: 'rate_limited'; window: 'minute' | 'day'; retryAfterSeconds: number }
	| { kind: 'stale' }
	| { kind: 'unavailable' }
	| { kind: 'error' };

const PROBE_KEY = /^[A-Za-z0-9-]{1,64}$/;
const PROVIDER_NAME = /^[a-z0-9-]{1,32}$/;
// One bare address: no display name, whitespace or list separators. The API has the final say.
const EMAIL = /^[^\s<>"(),;:\\@]+@[^\s<>"(),;:\\@]+$/;
const E164 = /^\+[1-9][0-9]{1,14}$/;

export function validDestination(channel: Channel, to: string): boolean {
	if (to.length > PROBE_DESTINATION_MAX) return false;
	return (channel === 'sms' ? E164 : EMAIL).test(to);
}

export function parseProbeInput(body: unknown): ProbeInput | null {
	if (typeof body !== 'object' || body === null) return null;
	const { channel, to, provider, key } = body as Record<string, unknown>;
	if (!CHANNELS.some((c) => c === channel)) return null;
	if (typeof to !== 'string' || typeof key !== 'string' || !PROBE_KEY.test(key)) return null;
	if (provider !== null && (typeof provider !== 'string' || !PROVIDER_NAME.test(provider)))
		return null;
	return { channel: channel as Channel, to: to.trim(), provider, key };
}

// Rounds up, so the wait shown is never shorter than the real one.
export function formatWait(seconds: number): string {
	if (seconds < 60) return `${Math.max(1, Math.ceil(seconds))} s`;
	const minutes = Math.ceil(seconds / 60);
	if (minutes < 60) return `${minutes} min`;
	const rest = minutes % 60;
	return rest === 0 ? `${minutes / 60} h` : `${Math.floor(minutes / 60)} h ${rest} min`;
}
