import { describe, expect, it } from 'vitest';
import { formatBytes } from './format.js';

describe('formatBytes', () => {
	it.each([
		[0, '0 B'],
		[999, '999 B'],
		[1000, '1.0 kB'],
		[1234, '1.2 kB'],
		[262_144, '262.1 kB'],
		[5_000_000, '5.0 MB']
	])('%d -> %s', (bytes, text) => expect(formatBytes(bytes)).toBe(text));

	it('does not print NaN or negatives', () => {
		expect(formatBytes(NaN)).toBe('unknown');
		expect(formatBytes(-1)).toBe('unknown');
	});
});
