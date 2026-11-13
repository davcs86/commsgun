import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Env } from './env.js';
import { createApi } from './api.js';
import { parseFilters } from '../filters.js';

const TOKEN = 'tok-' + 'x'.repeat(40);
const env: Env = {
	adminPassword: 'p'.repeat(16),
	sessionSecret: 's'.repeat(32),
	adminApiToken: TOKEN,
	apiBaseUrl: 'http://api:9201',
	origin: 'https://x.test'
};
const filters = parseFilters(
	new URLSearchParams('range=1h&channel=email'),
	new Date('2026-11-13T12:00:00Z')
);
const json = (body: unknown, status = 200) =>
	new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });

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
		const fetchFn = vi.fn<(u: string, i?: RequestInit) => Promise<Response>>(async () => json({}));
		const api = createApi(env, fetchFn as unknown as typeof fetch);
		await api.stats(filters);
		await api.facets();
		await api.overview();
		await api.message('m1');
		expect(spy).toHaveBeenCalledTimes(4);
		expect(spy).toHaveBeenCalledWith(5000);
		for (const [, init] of fetchFn.mock.calls) expect(init?.signal).toBeInstanceOf(AbortSignal);
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
		try {
			vi.spyOn(AbortSignal, 'timeout').mockImplementation((ms) => {
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
});
