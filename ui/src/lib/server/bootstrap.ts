import type { createApi } from './api.js';
import { ADMIN_USERNAME, IMPORT_RETRY_MS } from './constants.js';

type ImportApi = Pick<ReturnType<typeof createApi>, 'bootstrapImport'>;

const wait = (ms: number) => new Promise<void>((done) => setTimeout(done, ms));

// Retries without limit while the API is unreachable or answers 5xx; the error never names the password.
export async function importAdmin(
	api: ImportApi,
	password: string,
	sleep: (ms: number) => Promise<void> = wait
): Promise<'imported' | 'skipped'> {
	for (;;) {
		const r = await api.bootstrapImport(ADMIN_USERNAME, password);
		if (r.ok) return r.data.result;
		if (r.error !== 'unavailable' && r.status < 500) {
			throw new Error(`admin import refused: ${r.status} ${r.error}`);
		}
		await sleep(IMPORT_RETRY_MS);
	}
}
