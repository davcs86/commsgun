import { createHmac } from 'node:crypto';
import { describe, expect, it } from 'vitest';
import { OIDC_KEY_LABEL } from './constants';
import { loadEnv } from './env';

const valid = {
	ADMIN_PASSWORD: 'p'.repeat(16),
	SESSION_SECRET: 's'.repeat(32),
	ADMIN_API_TOKEN: 't'.repeat(32),
	API_BASE_URL: 'http://api:9201',
	ORIGIN: 'https://commsgun.example.test'
};

describe('loadEnv', () => {
	it.each([
		['ADMIN_PASSWORD', 'x'.repeat(15)],
		['SESSION_SECRET', 'x'.repeat(31)],
		['ADMIN_API_TOKEN', 'x'.repeat(31)],
		['API_BASE_URL', 'not a url'],
		['API_BASE_URL', 'api:9201'],
		['API_BASE_URL', 'ftp://api:9201'],
		['ORIGIN', ''],
		['ORIGIN', 'commsgun.example.test']
	])('rejects %s = %s', (key, value) => {
		expect(() => loadEnv({ ...valid, [key]: value })).toThrow(key);
	});

	it.each(Object.keys(valid).filter((k) => k !== 'ADMIN_PASSWORD'))(
		'rejects a missing %s',
		(key) => {
			expect(() => loadEnv({ ...valid, [key]: undefined })).toThrow(key);
		}
	);

	it.each([
		['ADMIN_PASSWORD', 'x'.repeat(15)],
		['SESSION_SECRET', 'y'.repeat(31)],
		['ADMIN_API_TOKEN', 'z'.repeat(31)],
		['API_BASE_URL', 'bad value one'],
		['ORIGIN', 'bad value two']
	])('never puts the value of %s in the error', (key, value) => {
		let message = '';
		try {
			loadEnv({ ...valid, [key]: value });
		} catch (e) {
			message = (e as Error).message;
		}
		expect(message).toContain(key);
		expect(message).not.toContain(value);
	});

	it('accepts an unset or empty ADMIN_PASSWORD as null; rejects 15 code points and "😀".repeat(15) (30 UTF-16 units); accepts "😀".repeat(16)', () => {
		expect(loadEnv({ ...valid, ADMIN_PASSWORD: undefined }).adminPassword).toBeNull();
		expect(loadEnv({ ...valid, ADMIN_PASSWORD: '' }).adminPassword).toBeNull();
		expect(() => loadEnv({ ...valid, ADMIN_PASSWORD: 'x'.repeat(15) })).toThrow('ADMIN_PASSWORD');
		expect('😀'.repeat(15).length).toBe(30);
		expect(() => loadEnv({ ...valid, ADMIN_PASSWORD: '😀'.repeat(15) })).toThrow('ADMIN_PASSWORD');
		expect(loadEnv({ ...valid, ADMIN_PASSWORD: '😀'.repeat(16) }).adminPassword).toBe(
			'😀'.repeat(16)
		);
	});

	it('accepts a valid environment', () => {
		const env = loadEnv(valid);
		expect(env.apiBaseUrl).toBe('http://api:9201');
		expect(env.adminPassword).toBe(valid.ADMIN_PASSWORD);
		expect(env.sessionSecret).toBe(valid.SESSION_SECRET);
		expect(env.adminApiToken).toBe(valid.ADMIN_API_TOKEN);
		expect(env.origin).toBe(valid.ORIGIN);
		expect(env).not.toHaveProperty('sessionKey');
	});

	it.each(['https://x.test/', 'https://x.test:443', 'https://x.test/some/path'])(
		'stores the normalised origin for ORIGIN = %s',
		(origin) => {
			expect(loadEnv({ ...valid, ORIGIN: origin }).origin).toBe('https://x.test');
		}
	);

	it('has no OIDC block when the OIDC variables are unset or empty; oidcKey is derived from SESSION_SECRET', () => {
		expect(loadEnv(valid).oidc).toBeNull();
		expect(
			loadEnv({ ...valid, OIDC_ISSUER: '', OIDC_CLIENT_ID: '', OIDC_CLIENT_SECRET: '' }).oidc
		).toBeNull();
		const key = createHmac('sha256', valid.SESSION_SECRET).update(OIDC_KEY_LABEL).digest();
		expect(loadEnv(valid).oidcKey.equals(key)).toBe(true);
	});
});

describe('loadEnv OIDC', () => {
	const oidc: Record<string, string> = {
		OIDC_ISSUER: 'https://issuer.example',
		OIDC_CLIENT_ID: 'commsgun',
		OIDC_CLIENT_SECRET: 'fake-oidc-client-secret-0000000000'
	};

	it.each(['OIDC_ISSUER', 'OIDC_CLIENT_ID', 'OIDC_CLIENT_SECRET'])(
		'rejects %s alone (all or none)',
		(key) => {
			let message = '';
			try {
				loadEnv({ ...valid, [key]: oidc[key] });
			} catch (e) {
				message = (e as Error).message;
			}
			for (const other of Object.keys(oidc).filter((k) => k !== key)) {
				expect(message).toContain(other);
			}
			expect(message).not.toContain(oidc[key]);
		}
	);

	// No loopback exception.
	it.each(['http://issuer.example', 'http://localhost:4182', 'http://127.0.0.1:4182'])(
		'rejects OIDC_ISSUER=%s',
		(issuer) => {
			expect(() => loadEnv({ ...valid, ...oidc, OIDC_ISSUER: issuer })).toThrow('OIDC_ISSUER');
		}
	);

	it.each(['false', '', 'true'])('rejects OIDC_AUTO_PROVISION=%s', (value) => {
		expect(() => loadEnv({ ...valid, OIDC_AUTO_PROVISION: value })).toThrow('OIDC_AUTO_PROVISION');
		expect(() => loadEnv({ ...valid, ...oidc, OIDC_AUTO_PROVISION: value })).toThrow(
			'OIDC_AUTO_PROVISION'
		);
	});

	it('defaults OIDC_SCOPES to "openid profile email"', () => {
		expect(loadEnv({ ...valid, ...oidc }).oidc).toEqual({
			issuer: oidc.OIDC_ISSUER,
			clientId: oidc.OIDC_CLIENT_ID,
			clientSecret: oidc.OIDC_CLIENT_SECRET,
			scopes: 'openid profile email'
		});
		expect(loadEnv({ ...valid, ...oidc, OIDC_SCOPES: 'openid' }).oidc?.scopes).toBe('openid');
	});
});
