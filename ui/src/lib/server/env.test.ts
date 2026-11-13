import { describe, expect, it } from 'vitest';
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
		['ORIGIN', '']
	])('rejects %s = %s', (key, value) => {
		expect(() => loadEnv({ ...valid, [key]: value })).toThrow(key);
	});

	it.each(Object.keys(valid))('rejects a missing %s', (key) => {
		expect(() => loadEnv({ ...valid, [key]: undefined })).toThrow(key);
	});

	it('never puts a value in the error', () => {
		const bad = 'secret-looking-value';
		let message = '';
		try {
			loadEnv({ ...valid, API_BASE_URL: bad });
		} catch (e) {
			message = (e as Error).message;
		}
		expect(message).toContain('API_BASE_URL');
		expect(message).not.toContain(bad);
	});

	it('accepts a valid environment', () => {
		const env = loadEnv(valid);
		expect(env.apiBaseUrl).toBe('http://api:9201');
		expect(env.adminPassword).toBe(valid.ADMIN_PASSWORD);
		expect(env.sessionSecret).toBe(valid.SESSION_SECRET);
		expect(env.adminApiToken).toBe(valid.ADMIN_API_TOKEN);
		expect(env.origin).toBe(valid.ORIGIN);
	});
});
