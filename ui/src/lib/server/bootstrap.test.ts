import { describe, expect, it, vi } from 'vitest';
import type { AccountResult } from './api.js';
import { importAdmin } from './bootstrap.js';
import { IMPORT_RETRY_MS } from './constants.js';

type ImportResult = AccountResult<{ result: 'imported' | 'skipped' }>;
const PASSWORD = 'fake-import-password-0000';

function apiOf(...results: ImportResult[]) {
	const bootstrapImport = vi.fn<(u: string, p: string) => Promise<ImportResult>>();
	for (const r of results) bootstrapImport.mockResolvedValueOnce(r);
	return { bootstrapImport };
}

describe('importAdmin', () => {
	it('retries a network error and a 503, then resolves imported', async () => {
		const api = apiOf(
			{ ok: false, status: 0, error: 'unavailable' },
			{ ok: false, status: 503, error: 'unavailable' },
			{ ok: true, data: { result: 'imported' } }
		);
		const sleep = vi.fn(async () => {});
		expect(await importAdmin(api, PASSWORD, sleep)).toBe('imported');
		expect(sleep.mock.calls).toEqual([[IMPORT_RETRY_MS], [IMPORT_RETRY_MS]]);
		expect(IMPORT_RETRY_MS).toBe(1000);
		expect(api.bootstrapImport).toHaveBeenCalledTimes(3);
		for (const call of api.bootstrapImport.mock.calls) expect(call).toEqual(['admin', PASSWORD]);
	});

	it('resolves skipped', async () => {
		const sleep = vi.fn(async () => {});
		const api = apiOf({ ok: true, data: { result: 'skipped' } });
		expect(await importAdmin(api, PASSWORD, sleep)).toBe('skipped');
		expect(sleep).not.toHaveBeenCalled();
	});

	it.each([
		[401, 'unauthorized'],
		[422, 'invalid_password'],
		[404, 'not_found'],
		[400, 'x']
	])('throws on %i %s, naming the code and never the password', async (status, error) => {
		const sleep = vi.fn(async () => {});
		const api = apiOf({ ok: false, status, error });
		const err = await importAdmin(api, PASSWORD, sleep).then(
			() => undefined,
			(e: unknown) => e
		);
		expect(err).toBeInstanceOf(Error);
		const message = (err as Error).message;
		expect(message).toContain(error);
		expect(message).toContain(String(status));
		expect(message).not.toContain(PASSWORD);
		expect(sleep).not.toHaveBeenCalled();
	});
});
