import { describe, expect, it } from 'vitest';
import {
	DEFAULT_RANGE,
	RANGES,
	activeFilterCount,
	parseFilters,
	toApiParams,
	toSearchParams,
	type Filters
} from './filters.js';

const now = new Date('2026-11-13T12:00:00.000Z');
const parse = (qs: string) => parseFilters(new URLSearchParams(qs), now);
const DAY_S = 86400;

describe('parseFilters', () => {
	it('defaults to the last hour when the URL is empty', () => {
		const f = parse('');
		expect(f.range).toBe('1h');
		expect(DEFAULT_RANGE).toBe('1h');
		expect(f.to.getTime()).toBe(now.getTime());
		expect(f.from.getTime()).toBe(now.getTime() - 3600_000);
	});

	it.each([
		['1h', 3600],
		['3h', 3 * 3600],
		['6h', 6 * 3600],
		['12h', 12 * 3600],
		['24h', DAY_S],
		['3d', 3 * DAY_S],
		['7d', 7 * DAY_S],
		['14d', 14 * DAY_S],
		['30d', 30 * DAY_S]
	])('range %s resolves to the right span', (range, seconds) => {
		const f = parse(`range=${range}`);
		expect(f.range).toBe(range);
		expect((f.to.getTime() - f.from.getTime()) / 1000).toBe(seconds);
		expect(f.to.getTime()).toBe(now.getTime());
	});

	it('offers exactly the nine quick ranges', () => {
		expect([...RANGES]).toEqual(['1h', '3h', '6h', '12h', '24h', '3d', '7d', '14d', '30d']);
	});

	it('resolves 3d to 259200s and 30d to 2592000s', () => {
		expect((parse('range=3d').to.getTime() - parse('range=3d').from.getTime()) / 1000).toBe(259200);
		expect((parse('range=30d').to.getTime() - parse('range=30d').from.getTime()) / 1000).toBe(
			2592000
		);
	});

	it('falls back to 1h for range=bogus', () => {
		const f = parse('range=bogus');
		expect(f.range).toBe('1h');
		expect(f.from.getTime()).toBe(now.getTime() - 3600_000);
	});

	it.each([
		'from=2026-11-13T10:00:00Z&to=2026-11-13T09:00:00Z',
		'from=2026-11-13T10:00:00Z&to=2026-11-13T10:00:00Z',
		'from=nope&to=2026-11-13T10:00:00Z',
		'from=2026-11-13T10:00:00Z&to=nope',
		'from=2026-11-13T10:00:00Z',
		'from=&to='
	])('falls back to 1h when the custom range is invalid: %s', (qs) => {
		const f = parse(qs);
		expect(f.range).toBe('1h');
		expect(f.to.getTime()).toBe(now.getTime());
	});

	it('parses a valid custom range', () => {
		const f = parse('from=2026-11-12T00:00:00Z&to=2026-11-12T06:00:00Z');
		expect(f.range).toBe('custom');
		expect(f.from.toISOString()).toBe('2026-11-12T00:00:00.000Z');
		expect(f.to.toISOString()).toBe('2026-11-12T06:00:00.000Z');
	});

	it('clamps a custom range longer than 30 days to 30 days ending at `to`', () => {
		const f = parse('from=2026-01-01T00:00:00Z&to=2026-11-01T00:00:00Z');
		expect(f.range).toBe('custom');
		expect(f.to.toISOString()).toBe('2026-11-01T00:00:00.000Z');
		expect(f.to.getTime() - f.from.getTime()).toBe(30 * DAY_S * 1000);
	});

	it('drops channel/state values outside the known sets', () => {
		const f = parse('channel=push&state=lost');
		expect(f.channel).toBeUndefined();
		expect(f.state).toBeUndefined();
		const ok = parse('channel=sms&state=bounced');
		expect(ok.channel).toBe('sms');
		expect(ok.state).toBe('bounced');
	});

	it('trims and truncates text filters to 100 chars; empty means unset', () => {
		const f = parse(
			`app=${encodeURIComponent('  ' + 'a'.repeat(150) + '  ')}&sender=%20%20&recipient=${'r'.repeat(100)}x`
		);
		expect(f.app).toBe('a'.repeat(100));
		expect(f.sender).toBeUndefined();
		expect(f.recipient).toBe('r'.repeat(100));
	});
});

describe('toSearchParams', () => {
	it('a quick range serialises as ?range= only, so a bookmarked view stays relative', () => {
		const qs = toSearchParams(parse('range=6h'));
		expect(qs.get('range')).toBe('6h');
		expect(qs.has('from')).toBe(false);
		expect(qs.has('to')).toBe(false);
	});

	it('a custom range serialises as ISO UTC from/to without range', () => {
		const qs = toSearchParams(parse('from=2026-11-12T00:00:00Z&to=2026-11-12T06:00:00Z'));
		expect(qs.get('from')).toBe('2026-11-12T00:00:00.000Z');
		expect(qs.get('to')).toBe('2026-11-12T06:00:00.000Z');
		expect(qs.has('range')).toBe(false);
	});

	it('round-trips every field through toSearchParams/parseFilters', () => {
		const full: Filters = {
			range: 'custom',
			from: new Date('2026-11-10T01:02:03.000Z'),
			to: new Date('2026-11-12T04:05:06.000Z'),
			channel: 'email',
			app: 'xstockstrat',
			state: 'failed',
			errorType: 'timeout',
			sender: 'noreply@example.test',
			recipient: 'a+b@example.test'
		};
		expect(parseFilters(toSearchParams(full), now)).toEqual(full);
		const quick = parse('range=7d&channel=sms&app=x&state=sent&error_type=e&sender=s&recipient=r');
		expect(parseFilters(toSearchParams(quick), now)).toEqual(quick);
	});

	it('uses the URL parameter names', () => {
		const qs = toSearchParams(parse('error_type=boom&sender=s'));
		expect(qs.get('error_type')).toBe('boom');
		expect(qs.get('sender')).toBe('s');
	});
});

describe('toApiParams', () => {
	it('always sends from/to instants plus the set filters, no range', () => {
		const qs = toApiParams(parse('range=1h&channel=email&error_type=timeout'));
		expect(qs.get('from')).toBe('2026-11-13T11:00:00.000Z');
		expect(qs.get('to')).toBe('2026-11-13T12:00:00.000Z');
		expect(qs.get('channel')).toBe('email');
		expect(qs.get('error_type')).toBe('timeout');
		expect(qs.has('range')).toBe(false);
		expect(qs.has('app')).toBe(false);
	});
});

describe('activeFilterCount', () => {
	it('excludes the range and counts set filters', () => {
		expect(activeFilterCount(parse('range=7d'))).toBe(0);
		expect(activeFilterCount(parse('channel=sms&app=x&recipient=r'))).toBe(3);
	});
});
