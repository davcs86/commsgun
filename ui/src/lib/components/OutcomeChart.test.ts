import { describe, expect, it } from 'vitest';
import type { SeriesPoint, Stats } from '#lib/types.js';
import { buckets } from './OutcomeChart.svelte';

const zero: Omit<SeriesPoint, 't'> = {
	enqueued: 0,
	sent: 0,
	delivered: 0,
	bounced: 0,
	complained: 0,
	failed: 0,
	expired: 0,
	retries: 0
};

const stats = (over: Partial<Stats>): Stats => ({
	from: '2026-11-13T10:00:00Z',
	to: '2026-11-13T10:05:00Z',
	bucket_seconds: 60,
	totals: { ...zero, queued_now: 0 },
	series: [],
	by_error_type: [],
	by_app: [],
	by_channel: [],
	...over
});

describe('buckets', () => {
	it('spans from..to in bucket steps and zero-fills missing points', () => {
		const rows = buckets(
			stats({ series: [{ ...zero, t: '2026-11-13T10:02:00.000Z', delivered: 4, failed: 1 }] })
		);
		expect(rows.map((r) => new Date(r.t).toISOString().slice(11, 16))).toEqual([
			'10:00',
			'10:01',
			'10:02',
			'10:03',
			'10:04'
		]);
		expect(rows[2]).toMatchObject({ delivered: 4, failed: 1, sent: 0 });
		expect(rows[0]).toMatchObject({ delivered: 0, expired: 0 });
	});

	it('returns no buckets for an unusable window instead of looping', () => {
		expect(buckets(stats({ bucket_seconds: 0 }))).toEqual([]);
		expect(buckets(stats({ from: 'nope' }))).toEqual([]);
	});
});
