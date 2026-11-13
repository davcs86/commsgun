import { API_TIMEOUT_MS } from '../constants.js';
import { toApiParams, type Filters } from '../filters.js';
import type { Facets, MessageDetail, MessagePage, Overview, Stats } from '../types.js';
import type { Env } from './env.js';

export type ApiResult<T> =
	| { ok: true; data: T }
	| { ok: false; status: number; reason: 'unavailable' | 'error' | 'not_found' };

export function createApi(env: Env, fetchFn: typeof fetch) {
	const base = env.apiBaseUrl.replace(/\/+$/, '');

	async function call<T>(path: string, params?: URLSearchParams): Promise<ApiResult<T>> {
		const query = params && params.size > 0 ? `?${params}` : '';
		let res: Response;
		try {
			res = await fetchFn(`${base}/admin/v1/${path}${query}`, {
				headers: { authorization: `Bearer ${env.adminApiToken}`, accept: 'application/json' },
				signal: AbortSignal.timeout(API_TIMEOUT_MS)
			});
		} catch {
			return { ok: false, status: 0, reason: 'unavailable' };
		}
		if (res.status === 503) return { ok: false, status: 503, reason: 'unavailable' };
		if (res.status === 404) return { ok: false, status: 404, reason: 'not_found' };
		if (!res.ok) return { ok: false, status: res.status, reason: 'error' };
		try {
			return { ok: true, data: (await res.json()) as T };
		} catch {
			return { ok: false, status: res.status, reason: 'error' };
		}
	}

	return {
		stats: (f: Filters) => call<Stats>('stats', toApiParams(f)),
		messages(f: Filters, cursor?: string) {
			const params = toApiParams(f);
			if (cursor) params.set('cursor', cursor);
			return call<MessagePage>('messages', params);
		},
		message: (id: string) => call<MessageDetail>(`messages/${encodeURIComponent(id)}`),
		facets: () => call<Facets>('facets'),
		overview: () => call<Overview>('overview')
	};
}
