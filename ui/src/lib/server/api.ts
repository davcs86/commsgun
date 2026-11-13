import { API_TIMEOUT_MS } from '../constants.js';
import { toApiParams, type Filters } from '../filters.js';
import type { Facets, MessageDetail, MessagePage, Overview, Stats } from '../types.js';
import type { Env } from './env.js';

export type ApiResult<T> =
	| { ok: true; data: T }
	| { ok: false; status: number; reason: 'unavailable' | 'error' | 'not_found' };

const MESSAGE_ID = /^[A-Za-z0-9-]{1,64}$/;

type Endpoint = 'stats' | 'messages' | 'message' | 'facets' | 'overview';

const isAbort = (e: unknown) =>
	e instanceof Error && (e.name === 'AbortError' || e.name === 'TimeoutError');

// One fixed-shape line per failure: never the token, the query or a response body.
function logFailure(endpoint: Endpoint, status: number | 'network' | 'timeout'): void {
	console.error(JSON.stringify({ level: 'error', msg: 'admin api call failed', endpoint, status }));
}

export function createApi(env: Env, fetchFn: typeof fetch) {
	const base = env.apiBaseUrl.replace(/\/+$/, '');

	async function call<T>(
		endpoint: Endpoint,
		path: string,
		params?: URLSearchParams
	): Promise<ApiResult<T>> {
		const query = params && params.size > 0 ? `?${params}` : '';
		let res: Response;
		try {
			res = await fetchFn(`${base}/admin/v1/${path}${query}`, {
				headers: { authorization: `Bearer ${env.adminApiToken}`, accept: 'application/json' },
				signal: AbortSignal.timeout(API_TIMEOUT_MS)
			});
		} catch (e) {
			logFailure(endpoint, isAbort(e) ? 'timeout' : 'network');
			return { ok: false, status: 0, reason: 'unavailable' };
		}
		if (res.status === 404 && endpoint === 'message') {
			return { ok: false, status: 404, reason: 'not_found' };
		}
		if (!res.ok) {
			logFailure(endpoint, res.status);
			return {
				ok: false,
				status: res.status,
				reason: res.status === 503 ? 'unavailable' : 'error'
			};
		}
		try {
			return { ok: true, data: (await res.json()) as T };
		} catch (e) {
			const aborted = isAbort(e);
			logFailure(endpoint, aborted ? 'timeout' : res.status);
			return {
				ok: false,
				status: aborted ? 0 : res.status,
				reason: aborted ? 'unavailable' : 'error'
			};
		}
	}

	return {
		stats: (f: Filters) => call<Stats>('stats', 'stats', toApiParams(f)),
		messages(f: Filters, cursor?: string) {
			const params = toApiParams(f);
			if (cursor) params.set('cursor', cursor);
			return call<MessagePage>('messages', 'messages', params);
		},
		message(id: string): Promise<ApiResult<MessageDetail>> {
			if (!MESSAGE_ID.test(id))
				return Promise.resolve({ ok: false, status: 404, reason: 'not_found' });
			return call<MessageDetail>('message', `messages/${id}`);
		},
		facets: () => call<Facets>('facets', 'facets'),
		overview: () => call<Overview>('overview', 'overview')
	};
}
