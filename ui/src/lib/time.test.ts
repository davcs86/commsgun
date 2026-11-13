import { describe, expect, it } from 'vitest';
import { bucketLabel, formatLocal, formatUtc } from './time.js';

const d = new Date('2026-11-13T23:05:09.000Z');

describe('time', () => {
	it('formats UTC with the zone shown', () => {
		expect(formatUtc(d)).toBe('2026-11-13 23:05:09 UTC');
	});

	it('formats in a given zone with the zone name shown', () => {
		const s = formatLocal(d, 'Asia/Tokyo');
		expect(s).toContain('2026-11-14');
		expect(s).toContain('08:05:09');
		expect(s).toMatch(/GMT\+9|JST/);
	});

	it('labels sub-day buckets with the time and day-or-longer buckets with the date', () => {
		expect(bucketLabel(d, 300, 'UTC')).toBe('23:05');
		expect(bucketLabel(d, 86400, 'UTC')).toBe('11-13');
	});
});
