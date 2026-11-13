import { API_TIMEOUT_MS } from '../constants.js';
import { toApiParams, type Filters } from '../filters.js';
import type { ProbeInput, ProbeOptions, ProbeResult } from '../probe.js';
import type { Facets, MessageDetail, MessagePage, Overview, Stats } from '../types.js';
import type { Env } from './env.js';

export type ApiResult<T> =
	| { ok: true; data: T }
	| { ok: false; status: number; reason: 'unavailable' | 'error' | 'not_found' };

const MESSAGE_ID = /^[A-Za-z0-9-]{1,64}$/;

type Endpoint = 'stats' | 'messages' | 'message' | 'facets' | 'overview' | 'probe';

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

	// Maps the API's status and error name; no part of the request is echoed or logged.
	async function sendProbe(input: ProbeInput, clientAddress: string): Promise<ProbeResult> {
		let res: Response;
		let body: Record<string, unknown> = {};
		try {
			res = await fetchFn(`${base}/admin/v1/probes`, {
				method: 'POST',
				headers: {
					authorization: `Bearer ${env.adminApiToken}`,
					accept: 'application/json',
					'content-type': 'application/json',
					'idempotency-key': input.key,
					'x-forwarded-for': clientAddress
				},
				body: JSON.stringify({ channel: input.channel, to: input.to, provider: input.provider }),
				signal: AbortSignal.timeout(API_TIMEOUT_MS)
			});
			const parsed: unknown = await res.json().catch((e: unknown) => {
				if (isAbort(e)) throw e;
				return {};
			});
			if (typeof parsed === 'object' && parsed !== null) body = parsed as Record<string, unknown>;
		} catch (e) {
			logFailure('probe', isAbort(e) ? 'timeout' : 'network');
			return { kind: 'unavailable' };
		}
		const error = typeof body.error === 'string' ? body.error : '';
		const ids = Array.isArray(body.message_ids) ? body.message_ids : [];
		if (res.ok && typeof ids[0] === 'string' && MESSAGE_ID.test(ids[0])) {
			return { kind: 'queued', messageId: ids[0], held: body.held === true };
		}
		if (res.status === 422) {
			const field = body.field === 'to' ? 'to' : error === 'sender_required' ? 'from' : 'other';
			return { kind: 'invalid', field };
		}
		if (res.status === 429) {
			const seconds = Number.parseInt(res.headers.get('retry-after') ?? '', 10);
			return {
				kind: 'rate_limited',
				window: body.window === 'day' ? 'day' : 'minute',
				retryAfterSeconds: Number.isFinite(seconds) && seconds > 0 ? seconds : 1
			};
		}
		if (res.status === 403 || error === 'provider_mismatch' || error === 'probe_app_missing') {
			return { kind: 'stale' };
		}
		logFailure('probe', res.status);
		return { kind: res.status === 503 ? 'unavailable' : 'error' };
	}

	return {
		probeOptions: () => call<ProbeOptions>('probe', 'probes'),
		sendProbe,
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
