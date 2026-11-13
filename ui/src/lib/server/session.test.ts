import { createHmac } from 'node:crypto';
import { describe, expect, it } from 'vitest';
import { LOGIN_MAX_ATTEMPTS, LOGIN_WINDOW_MS, SESSION_TTL_MS } from './constants';
import { LoginLimiter, checkPassword, safeNext, signSession, verifySession } from './session';

const SECRET = 'test-secret-0000000000000000000000000';
const NOW = 1_800_000_000_000;
const validCookie = signSession(SECRET, NOW);
const b64url = (s: string) => Buffer.from(s).toString('base64url');

describe('session cookie', () => {
	it('accepts a fresh cookie and rejects it after 12h', () => {
		expect(verifySession(SECRET, validCookie, NOW)).toBe(true);
		expect(verifySession(SECRET, validCookie, NOW + SESSION_TTL_MS - 1)).toBe(true);
		expect(verifySession(SECRET, validCookie, NOW + SESSION_TTL_MS)).toBe(false);
	});

	it('rejects a cookie signed with another secret', () => {
		expect(verifySession('other-secret-00000000000000000000000', validCookie, NOW)).toBe(false);
	});

	it('rejects a validly signed payload that lacks a numeric exp', () => {
		const payload = b64url(JSON.stringify({ exp: 'x' }));
		const sig = createHmac('sha256', SECRET).update(payload).digest('base64url');
		expect(verifySession(SECRET, `${payload}.${sig}`, NOW)).toBe(false);
	});

	const forgedSig = createHmac('sha256', 'wrong').update('eyJleHAiOiJ4In0').digest('base64url');
	it.each([
		['empty', ''],
		['undefined', undefined],
		['garbage', 'garbage'],
		['three parts', 'a.b.c'],
		['truncated signature', validCookie.slice(0, -2)],
		['trailing char', validCookie + 'x'],
		['bad signature', 'eyJleHAiOiJ4In0.' + forgedSig],
		['non-base64 chars', '!!!.???']
	])('verifySession(%s) is false and does not throw', (_name, value) => {
		expect(() => verifySession(SECRET, value, NOW)).not.toThrow();
		expect(verifySession(SECRET, value, NOW)).toBe(false);
	});
});

describe('checkPassword', () => {
	it('is true only for an exact match', () => {
		expect(checkPassword('correct-horse', 'correct-horse')).toBe(true);
		expect(checkPassword('correct', 'correct-horse')).toBe(false);
		expect(checkPassword('correct-horse-x', 'correct-horse')).toBe(false);
		expect(checkPassword('', 'correct-horse')).toBe(false);
	});
});

describe('LoginLimiter', () => {
	it('allows 5 then blocks until 60s have passed; keys are independent', () => {
		const limiter = new LoginLimiter();
		for (let i = 0; i < LOGIN_MAX_ATTEMPTS; i++) expect(limiter.allow('a', NOW + i)).toBe(true);
		expect(limiter.allow('a', NOW + 10)).toBe(false);
		expect(limiter.allow('b', NOW + 10)).toBe(true);
		expect(limiter.allow('a', NOW + LOGIN_WINDOW_MS - 1)).toBe(false);
		expect(limiter.allow('a', NOW + LOGIN_WINDOW_MS)).toBe(true);
	});

	it('does not count a blocked attempt toward the window', () => {
		const limiter = new LoginLimiter();
		for (let i = 0; i < LOGIN_MAX_ATTEMPTS; i++) limiter.allow('a', NOW);
		for (let i = 0; i < 20; i++) limiter.allow('a', NOW + 30_000);
		expect(limiter.allow('a', NOW + LOGIN_WINDOW_MS)).toBe(true);
	});
});

describe('safeNext', () => {
	it.each([
		['/messages?x=1', '/messages?x=1'],
		['/', '/'],
		['//evil.example', '/'],
		['/\\evil.example', '/'],
		['https://evil.example', '/'],
		['messages', '/'],
		['', '/'],
		[null, '/']
	])('safeNext(%s) is %s', (input, want) => {
		expect(safeNext(input)).toBe(want);
	});
});
