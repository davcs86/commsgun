import { describe, expect, it } from 'vitest';
import { formatWait, parseProbeInput, validDestination } from './probe.js';

const KEY = '3f2b8c1e-5d4a-4e6f-9a1b-0c2d3e4f5a6b';

describe('validDestination', () => {
	it('accepts one bare email address', () => {
		expect(validDestination('email', 'me@example.com')).toBe(true);
		expect(validDestination('email', 'first.last+tag@sub.example.co')).toBe(true);
	});

	it.each([
		'',
		'no-at',
		'Me <me@example.com>',
		'a@b.io\r\nBcc: x@y.io',
		'a@b.io, c@d.io',
		'a b@c.io',
		'<script>alert(1)</script>',
		`${'a'.repeat(400)}@example.com`
	])('rejects the email destination %j', (to) => {
		expect(validDestination('email', to)).toBe(false);
	});

	it('accepts an E.164 number only', () => {
		expect(validDestination('sms', '+15551234567')).toBe(true);
		for (const to of ['5551234567', '+0123', '+1555 123 4567', '+1', '+1234567890123456', ''])
			expect(validDestination('sms', to)).toBe(false);
		expect(validDestination('sms', 'me@example.com')).toBe(false);
	});
});

describe('parseProbeInput', () => {
	const ok = { channel: 'email', to: ' me@example.com ', provider: 'resend', key: KEY };

	it('returns the input with the destination trimmed', () => {
		expect(parseProbeInput(ok)).toEqual({ ...ok, to: 'me@example.com' });
		expect(parseProbeInput({ ...ok, channel: 'sms', provider: null })?.provider).toBeNull();
	});

	it.each([
		null,
		'x',
		[],
		{ ...ok, channel: 'fax' },
		{ ...ok, to: 7 },
		{ ...ok, to: undefined },
		{ ...ok, provider: 7 },
		{ ...ok, provider: undefined },
		{ ...ok, provider: 'Re send' },
		{ ...ok, key: '' },
		{ ...ok, key: 'a b' },
		{ ...ok, key: 'k'.repeat(65) },
		{ ...ok, key: undefined }
	])('returns null for %j', (body) => {
		expect(parseProbeInput(body)).toBeNull();
	});
});

describe('formatWait', () => {
	it.each([
		[0, '1 s'],
		[42, '42 s'],
		[59.2, '60 s'],
		[60, '1 min'],
		[61, '2 min'],
		[3600, '1 h'],
		[3601, '1 h 1 min'],
		[43_200, '12 h']
	])('formatWait(%d) is %j', (seconds, want) => {
		expect(formatWait(seconds)).toBe(want);
	});
});
