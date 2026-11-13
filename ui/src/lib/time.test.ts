import { describe, expect, it } from 'vitest';
import { bucketLabel, formatCompact, formatLocal, formatUtc, zoneName } from './time.js';

const d = new Date('2026-11-13T23:05:09.000Z');

describe('time', () => {
	it('formats UTC with the zone shown', () => {
		expect(formatUtc(d)).toBe('2026-11-13 23:05:09 UTC');
	});

	it('formats in a given zone with a zone label present', () => {
		const s = formatLocal(d, 'Asia/Tokyo');
		expect(s).toMatch(/^2026-11-14 08:05:09 \S+/);
	});

	it('labels sub-day buckets with the time and day-or-longer buckets with the date', () => {
		expect(bucketLabel(d, 300, 'UTC')).toBe('23:05');
		expect(bucketLabel(d, 86400, 'UTC')).toBe('11-13');
	});

	it('compact times show time and zone, and the date only when it is not today', () => {
		const now = new Date('2026-11-13T10:00:00.000Z');
		expect(formatCompact(d, now, 'UTC')).toBe('23:05 UTC');
		expect(formatCompact(new Date('2026-11-14T00:00:00Z'), now, 'UTC')).toBe(
			'2026-11-14 00:00 UTC'
		);
		expect(formatCompact(new Date('2026-11-14T00:00:00Z'), now, 'America/Mexico_City')).toBe(
			'18:00 CST'
		);
	});

	it('names a zone in its short form', () => {
		expect(zoneName('UTC')).toBe('UTC');
		expect(zoneName('America/Mexico_City', d)).toBe('CST');
	});
});
