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
		['API_BASE_URL', 'api:9201'],
		['API_BASE_URL', 'ftp://api:9201'],
		['ORIGIN', ''],
		['ORIGIN', 'commsgun.example.test']
	])('rejects %s = %s', (key, value) => {
		expect(() => loadEnv({ ...valid, [key]: value })).toThrow(key);
	});

	it.each(Object.keys(valid))('rejects a missing %s', (key) => {
		expect(() => loadEnv({ ...valid, [key]: undefined })).toThrow(key);
	});

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

	it('accepts a valid environment', () => {
		const env = loadEnv(valid);
		expect(env.apiBaseUrl).toBe('http://api:9201');
		expect(env.adminPassword).toBe(valid.ADMIN_PASSWORD);
		expect(env.sessionSecret).toBe(valid.SESSION_SECRET);
		expect(env.adminApiToken).toBe(valid.ADMIN_API_TOKEN);
		expect(env.origin).toBe(valid.ORIGIN);
	});
});
