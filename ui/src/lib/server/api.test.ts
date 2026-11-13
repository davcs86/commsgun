import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Env } from './env.js';
import { createApi } from './api.js';
import { parseFilters } from '../filters.js';

const TOKEN = 'tok-' + 'x'.repeat(40);
const env: Env = {
	adminPassword: 'p'.repeat(16),
	sessionSecret: 's'.repeat(32),
	adminApiToken: TOKEN,
	apiBaseUrl: 'http://api:9201',
	origin: 'https://x.test',
	sessionKey: Buffer.alloc(32)
};
const filters = parseFilters(
	new URLSearchParams('range=1h&channel=email'),
	new Date('2026-11-13T12:00:00Z')
);
const json = (body: unknown, status = 200) =>
	new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });

beforeEach(() => vi.spyOn(console, 'error').mockImplementation(() => {}));
afterEach(() => vi.restoreAllMocks());

describe('createApi', () => {
	it('sends Authorization: Bearer <token> and the API params', async () => {
		const fetchFn = vi.fn<(u: string, i?: RequestInit) => Promise<Response>>(async () =>
			json({ items: [], next_cursor: null })
		);
		await createApi(env, fetchFn as unknown as typeof fetch).messages(filters, 'cur1');
		const [url, init] = fetchFn.mock.calls[0];
		const u = new URL(String(url));
		expect(u.origin + u.pathname).toBe('http://api:9201/admin/v1/messages');
		expect(u.searchParams.get('channel')).toBe('email');
		expect(u.searchParams.get('from')).toBe('2026-11-13T11:00:00.000Z');
		expect(u.searchParams.get('cursor')).toBe('cur1');
		expect(new Headers(init?.headers).get('authorization')).toBe(`Bearer ${TOKEN}`);
	});

	it('applies a 5s abort signal to every call', async () => {
		const spy = vi.spyOn(AbortSignal, 'timeout');
		try {
			const fetchFn = vi.fn<(u: string, i?: RequestInit) => Promise<Response>>(async () =>
				json({})
			);
			const api = createApi(env, fetchFn as unknown as typeof fetch);
			await api.stats(filters);
			await api.facets();
			await api.overview();
			await api.message('m1');
			expect(spy).toHaveBeenCalledTimes(4);
			expect(spy).toHaveBeenCalledWith(5000);
			for (const [, init] of fetchFn.mock.calls) expect(init?.signal).toBeInstanceOf(AbortSignal);
		} finally {
			spy.mockRestore();
		}
	});

	it('maps a network error and a timeout to unavailable', async () => {
		const net = createApi(env, (async () => {
			throw new TypeError('fetch failed');
		}) as unknown as typeof fetch);
		expect(await net.overview()).toEqual({ ok: false, status: 0, reason: 'unavailable' });
		const timeout = createApi(env, (async () => {
			throw new DOMException('timed out', 'TimeoutError');
		}) as unknown as typeof fetch);
		expect(await timeout.facets()).toEqual({ ok: false, status: 0, reason: 'unavailable' });
	});

	it('maps 503 to unavailable, 404 to not_found and other non-2xx (incl. 401) to error', async () => {
		const mk = (status: number) =>
			createApi(env, (async () =>
				json({ error: 'nope', field: 'from' }, status)) as unknown as typeof fetch);
		expect(await mk(503).overview()).toEqual({ ok: false, status: 503, reason: 'unavailable' });
		expect(await mk(404).message('x')).toEqual({ ok: false, status: 404, reason: 'not_found' });
		expect(await mk(404).stats(filters)).toEqual({ ok: false, status: 404, reason: 'error' });
		expect(await mk(404).overview()).toEqual({ ok: false, status: 404, reason: 'error' });
		expect(await mk(401).stats(filters)).toEqual({ ok: false, status: 401, reason: 'error' });
		expect(await mk(400).stats(filters)).toEqual({ ok: false, status: 400, reason: 'error' });
		expect(await mk(500).facets()).toEqual({ ok: false, status: 500, reason: 'error' });
	});

	it('maps an unparsable 2xx body to error', async () => {
		const api = createApi(
			env,
			(async () => new Response('<html>', { status: 200 })) as unknown as typeof fetch
		);
		expect(await api.overview()).toEqual({ ok: false, status: 200, reason: 'error' });
	});

	it('requests /admin/v1/messages/<uuid> for a valid id', async () => {
		const id = '3f2b8c1e-5d4a-4e6f-9a1b-0c2d3e4f5a6b';
		const fetchFn = vi.fn<(u: string, i?: RequestInit) => Promise<Response>>(async () =>
			json({ message: { id }, attempts: [] })
		);
		const r = await createApi(env, fetchFn as unknown as typeof fetch).message(id);
		expect(r).toEqual({ ok: true, data: { message: { id }, attempts: [] } });
		expect(String(fetchFn.mock.calls[0][0])).toBe(`http://api:9201/admin/v1/messages/${id}`);
	});

	it.each(['..', '.', '', 'a/b', 'a'.repeat(65)])(
		'message(%j) is not_found without a request',
		async (id) => {
			const fetchFn = vi.fn();
			const r = await createApi(env, fetchFn as unknown as typeof fetch).message(id);
			expect(r).toEqual({ ok: false, status: 404, reason: 'not_found' });
			expect(fetchFn).not.toHaveBeenCalled();
		}
	);

	it('aborts after exactly 5s: still pending at 4999 ms, unavailable at 5000 ms', async () => {
		vi.useFakeTimers();
		const spy = vi.spyOn(AbortSignal, 'timeout');
		try {
			spy.mockImplementation((ms) => {
				const c = new AbortController();
				setTimeout(() => c.abort(new DOMException('timed out', 'TimeoutError')), ms);
				return c.signal;
			});
			const hang = ((_u: string, init?: RequestInit) =>
				new Promise((_resolve, reject) => {
					init?.signal?.addEventListener('abort', () => reject(init.signal?.reason));
				})) as unknown as typeof fetch;
			let settled: unknown;
			const p = createApi(env, hang)
				.overview()
				.then((r) => (settled = r));
			await vi.advanceTimersByTimeAsync(4999);
			expect(settled).toBeUndefined();
			await vi.advanceTimersByTimeAsync(1);
			await p;
			expect(settled).toEqual({ ok: false, status: 0, reason: 'unavailable' });
		} finally {
			spy.mockRestore();
			vi.useRealTimers();
		}
	});

	it.each(['AbortError', 'TimeoutError'])(
		'a body read failing with %s is unavailable',
		async (name) => {
			const res = {
				ok: true,
				status: 200,
				json: async () => Promise.reject(new DOMException('x', name))
			};
			const api = createApi(env, (async () => res) as unknown as typeof fetch);
			expect(await api.overview()).toEqual({ ok: false, status: 0, reason: 'unavailable' });
		}
	);

	it('never includes the token in a thrown or returned message', async () => {
		const leaky = createApi(env, (async () => {
			throw new Error(`boom ${TOKEN}`);
		}) as unknown as typeof fetch);
		const failures = [
			await leaky.overview(),
			await createApi(env, (async () =>
				json({ error: TOKEN }, 500)) as unknown as typeof fetch).overview()
		];
		for (const r of failures) expect(JSON.stringify(r)).not.toContain(TOKEN);
	});

	describe('failure log', () => {
		const logged = (spy: { mock: { calls: unknown[][] } }) =>
			spy.mock.calls.map((c) => JSON.parse(String(c[0])) as Record<string, unknown>);

		it.each([
			['a non-2xx status', async () => json({ error: 'x' }, 401), 401],
			['a 503', async () => json({}, 503), 503],
			['a 404 outside message()', async () => json({}, 404), 404],
			['an unparsable body', async () => new Response('<html>', { status: 200 }), 200],
			[
				'a network error',
				async () => {
					throw new TypeError('fetch failed');
				},
				'network'
			],
			[
				'a timeout',
				async () => {
					throw new DOMException('timed out', 'TimeoutError');
				},
				'timeout'
			]
		])('logs one fixed-shape line to stderr for %s', async (_name, impl, status) => {
			const spy = vi.mocked(console.error);
			await createApi(env, impl as unknown as typeof fetch).stats(filters);
			expect(logged(spy)).toEqual([
				{ level: 'error', msg: 'admin api call failed', endpoint: 'stats', status }
			]);
			const line = String(spy.mock.calls[0][0]);
			for (const secret of [TOKEN, 'channel', 'email', '<html>', 'fetch failed']) {
				expect(line).not.toContain(secret);
			}
		});

		it('logs nothing for a success or a missing message', async () => {
			const spy = vi.mocked(console.error);
			await createApi(env, (async () => json({})) as unknown as typeof fetch).overview();
			await createApi(env, (async () => json({}, 404)) as unknown as typeof fetch).message('m1');
			expect(spy).not.toHaveBeenCalled();
		});
	});

	describe('sendProbe', () => {
		const input = {
			channel: 'email',
			to: 'me@example.com',
			provider: 'resend',
			key: 'key-1'
		} as const;
		const ID = '3f2b8c1e-5d4a-4e6f-9a1b-0c2d3e4f5a6b';
		const send = (res: () => Response | Promise<Response>) =>
			createApi(env, (async () => res()) as unknown as typeof fetch).sendProbe(
				input,
				'203.0.113.7'
			);

		it('POSTs the probe with the token, the key and the client address', async () => {
			const fetchFn = vi.fn<(u: string, i?: RequestInit) => Promise<Response>>(async () =>
				json({ request_id: 'r', message_ids: [ID], duplicate: false, held: false }, 202)
			);
			const r = await createApi(env, fetchFn as unknown as typeof fetch).sendProbe(
				input,
				'203.0.113.7'
			);
			expect(r).toEqual({ kind: 'queued', messageId: ID, held: false });
			const [url, init] = fetchFn.mock.calls[0];
			expect(String(url)).toBe('http://api:9201/admin/v1/probes');
			expect(init?.method).toBe('POST');
			expect(init?.signal).toBeInstanceOf(AbortSignal);
			const headers = new Headers(init?.headers);
			expect(headers.get('authorization')).toBe(`Bearer ${TOKEN}`);
			expect(headers.get('idempotency-key')).toBe('key-1');
			expect(headers.get('x-forwarded-for')).toBe('203.0.113.7');
			expect(headers.get('content-type')).toBe('application/json');
			expect(JSON.parse(String(init?.body))).toEqual({
				channel: 'email',
				to: 'me@example.com',
				provider: 'resend'
			});
		});

		it('maps an idempotent repeat and a held message to queued', async () => {
			const body = { request_id: 'r', message_ids: [ID], duplicate: true, held: true };
			expect(await send(() => json(body, 200))).toEqual({
				kind: 'queued',
				messageId: ID,
				held: true
			});
		});

		it.each([
			[422, { error: 'invalid_recipient', field: 'to' }, { kind: 'invalid', field: 'to' }],
			[422, { error: 'sender_required', field: 'from' }, { kind: 'invalid', field: 'from' }],
			[422, { error: 'invalid_json', field: '' }, { kind: 'invalid', field: 'other' }],
			[409, { error: 'provider_mismatch' }, { kind: 'stale' }],
			[409, { error: 'probe_app_missing' }, { kind: 'stale' }],
			[403, { error: 'channel_not_allowed' }, { kind: 'stale' }],
			[409, { error: 'idempotency_conflict' }, { kind: 'error' }],
			[500, { error: 'internal' }, { kind: 'error' }],
			[401, { error: 'unauthorized' }, { kind: 'error' }],
			[503, { error: 'unavailable' }, { kind: 'unavailable' }],
			[202, { request_id: 'r', message_ids: ['../x'] }, { kind: 'error' }],
			[202, { request_id: 'r', message_ids: [] }, { kind: 'error' }]
		])('maps %i %j', async (status, body, want) => {
			expect(await send(() => json(body, status))).toEqual(want);
		});

		it('maps 429 with its window and Retry-After', async () => {
			const limited = (window: string, retry?: string) =>
				send(
					() =>
						new Response(JSON.stringify({ error: 'rate_limited', window }), {
							status: 429,
							headers: retry === undefined ? {} : { 'retry-after': retry }
						})
				);
			expect(await limited('day', '42')).toEqual({
				kind: 'rate_limited',
				window: 'day',
				retryAfterSeconds: 42
			});
			expect(await limited('minute', 'soon')).toEqual({
				kind: 'rate_limited',
				window: 'minute',
				retryAfterSeconds: 1
			});
			expect(await limited('minute')).toMatchObject({ retryAfterSeconds: 1 });
		});

		it('maps a network error, a timeout and an aborted body read to unavailable', async () => {
			const fail = (e: unknown) =>
				send(() => {
					throw e;
				});
			expect(await fail(new TypeError('fetch failed'))).toEqual({ kind: 'unavailable' });
			expect(await fail(new DOMException('timed out', 'TimeoutError'))).toEqual({
				kind: 'unavailable'
			});
			const res = json({}, 202);
			vi.spyOn(res, 'json').mockRejectedValue(new DOMException('aborted', 'AbortError'));
			expect(await send(() => res)).toEqual({ kind: 'unavailable' });
		});

		it('logs a fixed-shape line that never carries the destination, key or token', async () => {
			await send(() => json({ error: 'internal', to: input.to }, 500));
			await send(() => {
				throw new TypeError(`fetch failed for ${input.to}`);
			});
			const lines = vi.mocked(console.error).mock.calls.map((c) => String(c[0]));
			expect(lines.map((l) => JSON.parse(l))).toEqual([
				{ level: 'error', msg: 'admin api call failed', endpoint: 'probe', status: 500 },
				{ level: 'error', msg: 'admin api call failed', endpoint: 'probe', status: 'network' }
			]);
			for (const banned of [input.to, input.key, TOKEN])
				expect(lines.join('\n')).not.toContain(banned);
		});
	});

	it('probeOptions GETs /admin/v1/probes', async () => {
		const options = { app: 'ops', available: false, channels: [] };
		const fetchFn = vi.fn<(u: string, i?: RequestInit) => Promise<Response>>(async () =>
			json(options)
		);
		const r = await createApi(env, fetchFn as unknown as typeof fetch).probeOptions();
		expect(r).toEqual({ ok: true, data: options });
		expect(String(fetchFn.mock.calls[0][0])).toBe('http://api:9201/admin/v1/probes');
		expect(fetchFn.mock.calls[0][1]?.method).toBeUndefined();
	});
});
