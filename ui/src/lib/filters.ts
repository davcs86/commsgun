import { MAX_CUSTOM_RANGE_MS, RANGE_SECONDS, TEXT_FILTER_MAX } from './constants.js';
import { CHANNELS, STATES, type Channel, type State } from './types.js';

export const RANGES = ['1h', '3h', '6h', '12h', '24h', '3d', '7d', '14d', '30d'] as const;
export type RangeKey = (typeof RANGES)[number];
const MIN_YEAR = 1970;
const MAX_YEAR = 9999;
const ZONELESS_DATETIME = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2}(\.\d+)?)?$/;

export const DEFAULT_RANGE: RangeKey = '1h';

export { CHANNELS, STATES };

export type Filters = {
	range: RangeKey | 'custom';
	from: Date;
	to: Date;
	channel?: Channel;
	app?: string;
	state?: State;
	errorType?: string;
	sender?: string;
	recipient?: string;
};

const TEXT_FIELDS = [
	['app', 'app'],
	['errorType', 'error_type'],
	['sender', 'sender'],
	['recipient', 'recipient']
] as const;

function text(value: string | null): string | undefined {
	const t = (value ?? '').trim().slice(0, TEXT_FILTER_MAX);
	return t === '' ? undefined : t;
}

function instant(value: string | null): Date | undefined {
	if (!value) return undefined;
	const d = new Date(ZONELESS_DATETIME.test(value) ? `${value}Z` : value);
	if (Number.isNaN(d.getTime())) return undefined;
	const year = d.getUTCFullYear();
	return year < MIN_YEAR || year > MAX_YEAR ? undefined : d;
}

function quick(range: RangeKey, now: Date): Pick<Filters, 'range' | 'from' | 'to'> {
	return { range, from: new Date(now.getTime() - RANGE_SECONDS[range] * 1000), to: new Date(now) };
}

function resolveRange(url: URLSearchParams, now: Date): Pick<Filters, 'range' | 'from' | 'to'> {
	const range = url.get('range');
	if (range && (RANGES as readonly string[]).includes(range)) return quick(range as RangeKey, now);
	const from = instant(url.get('from'));
	const to = instant(url.get('to'));
	if (!from || !to || from.getTime() >= to.getTime()) return quick(DEFAULT_RANGE, now);
	const floor = to.getTime() - MAX_CUSTOM_RANGE_MS;
	return { range: 'custom', from: new Date(Math.max(from.getTime(), floor)), to };
}

export function parseFilters(url: URLSearchParams, now: Date): Filters {
	const f: Filters = resolveRange(url, now);
	const channel = url.get('channel');
	if (channel && (CHANNELS as readonly string[]).includes(channel)) f.channel = channel as Channel;
	const state = url.get('state');
	if (state && (STATES as readonly string[]).includes(state)) f.state = state as State;
	for (const [key, param] of TEXT_FIELDS) {
		const v = text(url.get(param));
		if (v !== undefined) f[key] = v;
	}
	return f;
}

function filterParams(f: Filters, out: URLSearchParams): URLSearchParams {
	if (f.channel) out.set('channel', f.channel);
	if (f.state) out.set('state', f.state);
	for (const [key, param] of TEXT_FIELDS) {
		const v = f[key];
		if (v) out.set(param, v);
	}
	return out;
}

export function toSearchParams(f: Filters): URLSearchParams {
	const out = new URLSearchParams();
	if (f.range === 'custom') {
		out.set('from', f.from.toISOString());
		out.set('to', f.to.toISOString());
	} else {
		out.set('range', f.range);
	}
	return filterParams(f, out);
}

export function toApiParams(f: Filters): URLSearchParams {
	const out = new URLSearchParams({ from: f.from.toISOString(), to: f.to.toISOString() });
	return filterParams(f, out);
}

export function activeFilterCount(f: Filters): number {
	return filterParams(f, new URLSearchParams()).size;
}
