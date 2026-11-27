import { describe, expect, it } from 'vitest';
import {
	LOGIN_LIMITER_MAX_KEYS,
	LOGIN_MAX_ATTEMPTS,
	LOGIN_WINDOW_MS,
	SESSION_TOKEN
} from './constants';
import { LoginLimiter, safeNext } from './session';

const NOW = 1_800_000_000_000;

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

describe('LoginLimiter memory', () => {
	it('drops stale keys and never exceeds the key cap', () => {
		const limiter = new LoginLimiter();
		const size = () => (limiter as unknown as { attempts: Map<string, number[]> }).attempts.size;
		for (let i = 0; i < LOGIN_LIMITER_MAX_KEYS; i++) limiter.allow(`old-${i}`, NOW);
		expect(size()).toBe(LOGIN_LIMITER_MAX_KEYS);
		limiter.allow('fresh', NOW + LOGIN_WINDOW_MS);
		expect(size()).toBe(1);
		for (let i = 0; i < LOGIN_LIMITER_MAX_KEYS + 50; i++) {
			limiter.allow(`busy-${i}`, NOW + LOGIN_WINDOW_MS + 1);
			expect(size()).toBeLessThanOrEqual(LOGIN_LIMITER_MAX_KEYS);
		}
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
		[null, '/'],
		['/\t/evil.example', '/'],
		['/\n/evil.example', '/'],
		['/ /evil.example', '/'],
		['/\t\\evil.example', '/'],
		['/login', '/'],
		['/login?next=/x', '/'],
		['/logout', '/'],
		['/messages?range=7d', '/messages?range=7d'],
		['/.//evil.example', '/'],
		['/..//evil.example', '/'],
		['/a/..//evil.example', '/'],
		['/.///evil.example', '/'],
		['/login/', '/'],
		['/logout?x=1', '/'],
		['/a/../messages', '/messages'],
		['/login/sso', '/'],
		['/login/sso/', '/'],
		['/login/sso?next=/x', '/'],
		['/auth/callback', '/'],
		['/auth/callback?code=x&state=y', '/'],
		['/auth/link', '/'],
		['/auth/', '/'],
		['/a/../auth/callback', '/'],
		['/authors', '/authors']
	])('safeNext(%s) is %s', (input, want) => {
		expect(safeNext(input)).toBe(want);
	});
});

describe('SESSION_TOKEN', () => {
	it('matches only 43 base64url characters, the API token shape', () => {
		expect(SESSION_TOKEN.test('A'.repeat(42) + '_')).toBe(true);
		expect(SESSION_TOKEN.test('a-'.repeat(21) + 'z')).toBe(true);
		for (const bad of ['', 'a'.repeat(42), 'a'.repeat(44), '!'.repeat(43), 'a'.repeat(42) + '=']) {
			expect(SESSION_TOKEN.test(bad)).toBe(false);
		}
	});
});
