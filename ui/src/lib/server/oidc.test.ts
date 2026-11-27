import { createHash, generateKeyPairSync, sign } from 'node:crypto';
import { describe, expect, it, vi } from 'vitest';
import {
	createIssuer,
	ISSUER_CLIENT_ID,
	ISSUER_CLIENT_SECRET,
	ISSUER_DEFAULT_SUB,
	ISSUER_DEFAULT_USERNAME
} from '../../../e2e/stub-issuer';
import { AUTH_TIME_SKEW_MS, OIDC_TX_TTL_MS } from './constants';
import { beginAuth, completeAuth, type OidcEnv } from './oidc';
import type { Transaction } from './oidc-cookie';

// Conformance cases of the SSO spec § 3, against the stub issuer through fetchFn.
const ISSUER = 'https://issuer.test';
const ORIGIN = 'https://commsgun.test';
const OIDC: OidcEnv = {
	issuer: ISSUER,
	clientId: ISSUER_CLIENT_ID,
	clientSecret: ISSUER_CLIENT_SECRET,
	scopes: 'openid profile email',
	origin: ORIGIN
};
const OTHER_KEY = generateKeyPairSync('rsa', { modulusLength: 2048 }).privateKey;

type Issuer = ReturnType<typeof createIssuer>;
type Intercept = (req: Request, next: () => Promise<Response>) => Promise<Response>;

// Whole seconds, so auth_time comparisons at the boundary are exact.
const nowMs = () => Math.floor(Date.now() / 1000) * 1000;

function setup(intercept?: Intercept) {
	const issuer = createIssuer(ISSUER);
	const requests: Request[] = [];
	const fetchFn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
		const req = new Request(input, init);
		requests.push(req.clone());
		return intercept ? intercept(req, () => issuer.handle(req)) : issuer.handle(req);
	});
	return { issuer, requests, fetchFn: fetchFn as unknown as typeof fetch, spy: fetchFn };
}

async function authorize(issuer: Issuer, url: URL): Promise<URL> {
	const res = await issuer.handle(new Request(url));
	expect(res.status).toBe(302);
	return new URL(res.headers.get('location') ?? '');
}

async function flow(
	{ issuer, fetchFn }: { issuer: Issuer; fetchFn: typeof fetch },
	opts: {
		purpose?: Transaction['purpose'];
		now?: number;
		mutate?: (callback: URL) => void;
		jwksCache?: { jwks: { keys: Record<string, unknown>[] }; uat: number };
	} = {}
) {
	const now = opts.now ?? nowMs();
	const { url, tx } = await beginAuth(
		OIDC,
		{ purpose: opts.purpose ?? 'login', next: '/' },
		now,
		fetchFn
	);
	const callback = await authorize(issuer, url);
	opts.mutate?.(callback);
	const deps = opts.jwksCache ? { jwksCache: opts.jwksCache } : undefined;
	const result = await completeAuth(OIDC, tx, callback, fetchFn, deps);
	return { url, tx, callback, result };
}

async function resign(res: Response): Promise<Response> {
	const body = (await res.json()) as { id_token: string };
	const [header, payload] = body.id_token.split('.');
	const signature = sign('sha256', Buffer.from(`${header}.${payload}`), OTHER_KEY);
	return Response.json({
		...body,
		id_token: `${header}.${payload}.${signature.toString('base64url')}`
	});
}

const isJwks = (req: Request) => new URL(req.url).pathname === '/jwks';
const isDiscovery = (req: Request) =>
	new URL(req.url).pathname === '/.well-known/openid-configuration';

describe('beginAuth', () => {
	it('authorization URL: response_type=code, code_challenge_method=S256, state, nonce, scope, redirect_uri; no prompt or max_age for login', async () => {
		const { fetchFn } = setup();
		const now = nowMs();
		const { url, tx } = await beginAuth(
			OIDC,
			{ purpose: 'login', next: '/messages' },
			now,
			fetchFn
		);
		expect(`${url.origin}${url.pathname}`).toBe(`${ISSUER}/authorize`);
		const q = url.searchParams;
		expect(q.get('client_id')).toBe(ISSUER_CLIENT_ID);
		expect(q.get('response_type')).toBe('code');
		expect(q.get('code_challenge_method')).toBe('S256');
		expect(q.get('code_challenge')).toBe(
			createHash('sha256').update(tx.verifier).digest('base64url')
		);
		expect(q.get('state')).toBe(tx.state);
		expect(q.get('nonce')).toBe(tx.nonce);
		expect(q.get('scope')).toBe('openid profile email');
		expect(q.get('redirect_uri')).toBe(`${ORIGIN}/auth/callback`);
		expect(q.has('prompt')).toBe(false);
		expect(q.has('max_age')).toBe(false);
		expect([...q.keys()].sort()).toEqual(
			[
				'client_id',
				'code_challenge',
				'code_challenge_method',
				'nonce',
				'redirect_uri',
				'response_type',
				'scope',
				'state'
			].sort()
		);
		expect(tx).toEqual({
			purpose: 'login',
			state: tx.state,
			nonce: tx.nonce,
			verifier: tx.verifier,
			next: '/messages',
			started: now,
			exp: now + OIDC_TX_TTL_MS
		});
		expect(tx.state).not.toBe(tx.nonce);
	});

	it('link purpose adds prompt=login and max_age=1; tx.started == now', async () => {
		const { fetchFn } = setup();
		const now = nowMs();
		const { url, tx } = await beginAuth(
			OIDC,
			{ purpose: 'link', next: '/account', binding: 'ab12', username: 'admin' },
			now,
			fetchFn
		);
		expect(url.searchParams.get('prompt')).toBe('login');
		expect(url.searchParams.get('max_age')).toBe('1');
		expect(tx).toMatchObject({ purpose: 'link', started: now, binding: 'ab12', username: 'admin' });
	});

	it('an http: issuer passed to beginAuth directly is refused by openid-client', async () => {
		const { fetchFn, spy } = setup();
		await expect(
			beginAuth(
				{ ...OIDC, issuer: 'http://issuer.test' },
				{ purpose: 'login', next: '/' },
				nowMs(),
				fetchFn
			)
		).rejects.toThrow();
		expect(spy).not.toHaveBeenCalled();
	});
});

describe('completeAuth', () => {
	it('success returns iss, sub and preferred_username; the token request used HTTP Basic', async () => {
		const s = setup();
		const { result } = await flow(s);
		expect(result).toEqual({
			ok: true,
			iss: ISSUER,
			sub: ISSUER_DEFAULT_SUB,
			preferredUsername: ISSUER_DEFAULT_USERNAME
		});
		const tokenRequest = s.requests.find((r) => new URL(r.url).pathname === '/token');
		// RFC 6749 2.3.1: each half form-urlencoded before base64.
		const header = tokenRequest?.headers.get('authorization') ?? '';
		expect(header).toMatch(/^Basic [A-Za-z0-9+/=]+$/);
		const pair = Buffer.from(header.slice('Basic '.length), 'base64').toString().split(':');
		expect(pair.map(decodeURIComponent)).toEqual([ISSUER_CLIENT_ID, ISSUER_CLIENT_SECRET]);
		expect(await tokenRequest?.text()).not.toContain(ISSUER_CLIENT_SECRET);

		s.issuer.next({ preferred_username: undefined });
		expect((await flow(s)).result).toMatchObject({ ok: true, preferredUsername: null });
	});

	it('link: auth_time absent → { ok: false, reason: "token" }', async () => {
		const s = setup();
		s.issuer.next({ auth_time: null });
		expect((await flow(s, { purpose: 'link' })).result).toEqual({ ok: false, reason: 'token' });
	});

	it('link: auth_time at started - 31 s → token; at started - 30 s → ok', async () => {
		const s = setup();
		const now = nowMs();
		expect(AUTH_TIME_SKEW_MS).toBe(30_000);
		s.issuer.next({ auth_time: now / 1000 - 31 });
		expect((await flow(s, { purpose: 'link', now })).result).toEqual({
			ok: false,
			reason: 'token'
		});
		s.issuer.next({ auth_time: now / 1000 - 30 });
		expect((await flow(s, { purpose: 'link', now })).result).toMatchObject({ ok: true });
	});

	it('login: an absent or old auth_time is not checked', async () => {
		const s = setup();
		s.issuer.next({ auth_time: null });
		expect((await flow(s)).result).toMatchObject({ ok: true });
		s.issuer.next({ auth_time: Math.floor(Date.now() / 1000) - 86_400 });
		expect((await flow(s)).result).toMatchObject({ ok: true });
	});

	const failures: [string, (s: ReturnType<typeof setup>) => void, 'state' | 'token'][] = [
		['bad state', () => {}, 'state'],
		['wrong iss', (s) => s.issuer.next({ iss: 'https://evil.test' }), 'token'],
		['wrong aud', (s) => s.issuer.next({ aud: 'other-client' }), 'token'],
		['expired', (s) => s.issuer.next({ exp: Math.floor(Date.now() / 1000) - 3600 }), 'token'],
		['wrong nonce', (s) => s.issuer.next({ nonce: 'other-nonce' }), 'token'],
		['HS256', (s) => s.issuer.next({ alg: 'HS256' }), 'token'],
		['none', (s) => s.issuer.next({ alg: 'none' }), 'token'],
		['signed by another key', () => {}, 'token']
	];
	it.each(failures)('%s → { ok: false }', async (name, arrange, reason) => {
		const s = setup(async (req, next) =>
			name === 'signed by another key' && new URL(req.url).pathname === '/token'
				? resign(await next())
				: next()
		);
		arrange(s);
		const mutate =
			name === 'bad state' ? (cb: URL) => cb.searchParams.set('state', 'x') : undefined;
		expect((await flow(s, { mutate })).result).toEqual({ ok: false, reason });
	});

	it('a replayed code fails the second time', async () => {
		const s = setup();
		const { tx, callback, result } = await flow(s);
		expect(result).toMatchObject({ ok: true });
		expect(await completeAuth(OIDC, tx, callback, s.fetchFn)).toEqual({
			ok: false,
			reason: 'token'
		});
	});

	it('the jwks_uri fetch fails (fetchFn rejects that URL only) → { ok: false }', async () => {
		const s = setup((req, next) =>
			isJwks(req) ? Promise.reject(new TypeError('fetch failed')) : next()
		);
		expect((await flow(s)).result).toMatchObject({ ok: false });
		expect(s.requests.some(isJwks)).toBe(true);
	});

	it('deps.jwksCache with uat = now - 301 s and the jwks_uri fetch failing → { ok: false }; uat = now - 10 s → ok', async () => {
		const s = setup((req, next) =>
			isJwks(req) ? Promise.reject(new TypeError('fetch failed')) : next()
		);
		const jwks = (await (await s.issuer.handle(new Request(`${ISSUER}/jwks`))).json()) as {
			keys: Record<string, unknown>[];
		};
		const epoch = Math.floor(Date.now() / 1000);
		const aged = await flow(s, { jwksCache: { jwks, uat: epoch - 301 } });
		expect(aged.result).toMatchObject({ ok: false });
		const fresh = await flow(s, { jwksCache: { jwks, uat: epoch - 10 } });
		expect(fresh.result).toMatchObject({ ok: true });
	});

	it('discovery failure → unavailable, and the next beginAuth fetches discovery again', async () => {
		const s = setup();
		const { url, tx } = await beginAuth(OIDC, { purpose: 'login', next: '/' }, nowMs(), s.fetchFn);
		const callback = await authorize(s.issuer, url);
		s.issuer.down(true);
		expect(await completeAuth(OIDC, tx, callback, s.fetchFn)).toEqual({
			ok: false,
			reason: 'unavailable'
		});
		await expect(
			beginAuth(OIDC, { purpose: 'login', next: '/' }, nowMs(), s.fetchFn)
		).rejects.toThrow();
		s.issuer.down(false);
		await beginAuth(OIDC, { purpose: 'login', next: '/' }, nowMs(), s.fetchFn);
		expect(s.requests.filter(isDiscovery)).toHaveLength(4);
	});
});
