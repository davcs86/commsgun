import { describe, expect, it } from 'vitest';
import { OIDC_TX_TTL_MS } from './constants';
import {
	bindingMatches,
	bindingOf,
	openTransaction,
	sealTransaction,
	type Transaction
} from './oidc-cookie';

const NOW = 1_800_000_000_000;
const KEY = Buffer.alloc(32, 7);
const TX: Transaction = {
	purpose: 'login',
	state: 'fake-state',
	nonce: 'fake-nonce',
	verifier: 'fake-verifier',
	next: '/messages?app=x',
	started: NOW,
	exp: NOW + OIDC_TX_TTL_MS
};

describe('transaction cookie', () => {
	it('round trips', () => {
		const sealed = sealTransaction(KEY, TX);
		expect(sealed).toMatch(/^[A-Za-z0-9_-]+$/);
		expect(openTransaction(KEY, sealed, NOW)).toEqual(TX);
		const link: Transaction = { ...TX, purpose: 'link', binding: 'ab', username: 'admin' };
		expect(openTransaction(KEY, sealTransaction(KEY, link), NOW)).toEqual(link);
	});

	it('seals with a fresh IV each time', () => {
		expect(sealTransaction(KEY, TX)).not.toBe(sealTransaction(KEY, TX));
	});

	it('one flipped byte → null', () => {
		const raw = Buffer.from(sealTransaction(KEY, TX), 'base64url');
		for (const i of [0, 12, raw.length - 1]) {
			const copy = Buffer.from(raw);
			copy[i] ^= 1;
			expect(openTransaction(KEY, copy.toString('base64url'), NOW)).toBeNull();
		}
	});

	it('at exp → null', () => {
		const sealed = sealTransaction(KEY, TX);
		expect(openTransaction(KEY, sealed, TX.exp - 1)).not.toBeNull();
		expect(openTransaction(KEY, sealed, TX.exp)).toBeNull();
	});

	it('other key → null', () => {
		expect(openTransaction(Buffer.alloc(32, 8), sealTransaction(KEY, TX), NOW)).toBeNull();
	});

	it.each([
		['undefined', undefined],
		['empty', ''],
		['x', 'x'],
		['10 KB', 'a'.repeat(10240)],
		['not base64url', '!'.repeat(64)]
	])('%s → null', (_, value) => {
		expect(openTransaction(KEY, value, NOW)).toBeNull();
	});
});

describe('session binding', () => {
	const TOKEN = 'fake-session-token-000000000000000000000000';
	const OTHER = 'fake-session-token-000000000000000000000002';

	it('bindingOf is the SHA-256 hex of the token', () => {
		expect(bindingOf(TOKEN)).toMatch(/^[0-9a-f]{64}$/);
		expect(bindingOf(TOKEN)).not.toBe(bindingOf(OTHER));
	});

	it('bindingMatches is true only for the bound token', () => {
		const binding = bindingOf(TOKEN);
		expect(bindingMatches(TOKEN, binding)).toBe(true);
		expect(bindingMatches(OTHER, binding)).toBe(false);
		for (const junk of ['', binding.slice(1), binding + '0', binding.toUpperCase(), TOKEN]) {
			expect(bindingMatches(TOKEN, junk)).toBe(false);
		}
	});
});
