import { execFileSync } from 'node:child_process';
import { createHash, createHmac, generateKeyPairSync, randomBytes, sign } from 'node:crypto';
import { mkdirSync, readFileSync, rmSync } from 'node:fs';
import { createServer } from 'node:https';
import { resolve } from 'node:path';

// Another site than the UI (127.0.0.1), so the callback is a cross-site navigation.
export const ISSUER_PORT = 4182;
export const ISSUER_URL = `https://localhost:${ISSUER_PORT}`;
const TLS_DIR = resolve(import.meta.dirname, '.tls');
export const ISSUER_CA_FILE = resolve(TLS_DIR, 'ca.pem');
export const ISSUER_CLIENT_ID = 'commsgun';
export const ISSUER_CLIENT_SECRET = 'fake-oidc-client-secret-0000000000';
export const ISSUER_DEFAULT_SUB = 'fake-sub-0001';
export const ISSUER_DEFAULT_USERNAME = 'fake-person';
// The display name of whoever holds the live session (setLiveSession).
export const ISSUER_LIVE_USERNAME = 'fake-live-person';
const KID = 'fake-key-1';
const TOKEN_TTL_S = 300;

export type IssuerClaims = {
	sub: string;
	preferred_username: string | null;
	iss: string;
	aud: string;
	exp: number;
	nonce: string;
	alg: string;
	auth_time: number | null;
};
type Grant = { redirectUri: string; challenge: string; claims: IssuerClaims };

const b64 = (value: unknown) => Buffer.from(JSON.stringify(value)).toString('base64url');
const json = (status: number, body: unknown) =>
	new Response(JSON.stringify(body), {
		status,
		headers: { 'content-type': 'application/json', 'cache-control': 'no-store' }
	});

function basicCredentials(header: string | null): [string, string] | null {
	const m = /^Basic ([A-Za-z0-9+/=]+)$/.exec(header ?? '');
	const pair = m ? Buffer.from(m[1], 'base64').toString() : '';
	const i = pair.indexOf(':');
	if (i < 0) return null;
	const decode = (s: string) => decodeURIComponent(s.replace(/\+/g, ' '));
	return [decode(pair.slice(0, i)), decode(pair.slice(i + 1))];
}

export function createIssuer(issuerUrl: string) {
	const { privateKey, publicKey } = generateKeyPairSync('rsa', { modulusLength: 2048 });
	const jwk = { ...publicKey.export({ format: 'jwk' }), kid: KID, alg: 'RS256', use: 'sig' };
	const grants = new Map<string, Grant>();
	let pending: Partial<IssuerClaims> = {};
	let liveSession: string | null = null;
	let isDown = false;

	function idToken(claims: IssuerClaims): string {
		const { alg, auth_time: authTime, preferred_username: name, ...rest } = claims;
		const now = Math.floor(Date.now() / 1000);
		const payload = {
			...rest,
			iat: now,
			...(name === null ? {} : { preferred_username: name }),
			...(authTime === null ? {} : { auth_time: authTime })
		};
		const header = alg === 'none' ? { alg, typ: 'JWT' } : { alg, typ: 'JWT', kid: KID };
		const input = `${b64(header)}.${b64(payload)}`;
		const signature =
			alg === 'RS256'
				? sign('sha256', Buffer.from(input), privateKey)
				: alg === 'HS256'
					? createHmac('sha256', ISSUER_CLIENT_SECRET).update(input).digest()
					: Buffer.alloc(0);
		return `${input}.${signature.toString('base64url')}`;
	}

	function authorize(url: URL): Response {
		const q = url.searchParams;
		const redirectUri = q.get('redirect_uri');
		const ok =
			q.get('client_id') === ISSUER_CLIENT_ID &&
			q.get('response_type') === 'code' &&
			q.get('code_challenge_method') === 'S256' &&
			!!q.get('code_challenge') &&
			!!q.get('state') &&
			!!q.get('nonce') &&
			(q.get('scope') ?? '').split(' ').includes('openid') &&
			!!redirectUri &&
			URL.canParse(redirectUri);
		if (!ok || !redirectUri) return new Response('bad authorization request', { status: 400 });
		const now = Math.floor(Date.now() / 1000);
		// prompt=login makes the person sign in again, so a live session's account is not reused.
		const live = q.get('prompt') === 'login' ? null : liveSession;
		const claims: IssuerClaims = {
			sub: live ?? ISSUER_DEFAULT_SUB,
			preferred_username: live === null ? ISSUER_DEFAULT_USERNAME : ISSUER_LIVE_USERNAME,
			iss: issuerUrl,
			aud: ISSUER_CLIENT_ID,
			exp: now + TOKEN_TTL_S,
			nonce: q.get('nonce') ?? '',
			alg: 'RS256',
			auth_time: now,
			...pending
		};
		pending = {};
		const code = randomBytes(16).toString('base64url');
		grants.set(code, { redirectUri, challenge: q.get('code_challenge') ?? '', claims });
		const target = new URL(redirectUri);
		target.searchParams.set('code', code);
		target.searchParams.set('state', q.get('state') ?? '');
		return new Response(null, { status: 302, headers: { location: target.href } });
	}

	async function token(req: Request): Promise<Response> {
		const creds = basicCredentials(req.headers.get('authorization'));
		if (!creds || creds[0] !== ISSUER_CLIENT_ID || creds[1] !== ISSUER_CLIENT_SECRET) {
			return json(401, { error: 'invalid_client' });
		}
		const form = new URLSearchParams(await req.text());
		const code = form.get('code') ?? '';
		const grant = grants.get(code);
		grants.delete(code);
		const challenge = createHash('sha256')
			.update(form.get('code_verifier') ?? '')
			.digest('base64url');
		if (
			!grant ||
			form.get('grant_type') !== 'authorization_code' ||
			form.get('redirect_uri') !== grant.redirectUri ||
			challenge !== grant.challenge
		) {
			return json(400, { error: 'invalid_grant' });
		}
		return json(200, {
			access_token: randomBytes(16).toString('base64url'),
			token_type: 'Bearer',
			expires_in: TOKEN_TTL_S,
			id_token: idToken(grant.claims)
		});
	}

	async function control(name: string, req: Request): Promise<Response> {
		const body = (await req.json().catch(() => ({}))) as Record<string, unknown>;
		if (name === 'next') api.next(body as Partial<IssuerClaims>);
		else if (name === 'live-session' && (typeof body.sub === 'string' || body.sub === null)) {
			api.setLiveSession(body.sub);
		} else if (name === 'down' && typeof body.on === 'boolean') api.down(body.on);
		else return new Response(null, { status: 400 });
		return new Response(null, { status: 204 });
	}

	const api = {
		async handle(req: Request): Promise<Response> {
			const url = new URL(req.url);
			if (req.method === 'POST' && url.pathname.startsWith('/__issuer/')) {
				return control(url.pathname.slice('/__issuer/'.length), req);
			}
			if (isDown) return new Response('down', { status: 503 });
			if (req.method === 'GET' && url.pathname === '/.well-known/openid-configuration') {
				return json(200, {
					issuer: issuerUrl,
					authorization_endpoint: `${issuerUrl}/authorize`,
					token_endpoint: `${issuerUrl}/token`,
					jwks_uri: `${issuerUrl}/jwks`,
					response_types_supported: ['code'],
					subject_types_supported: ['public'],
					id_token_signing_alg_values_supported: ['RS256'],
					token_endpoint_auth_methods_supported: ['client_secret_basic'],
					code_challenge_methods_supported: ['S256']
				});
			}
			if (req.method === 'GET' && url.pathname === '/jwks') return json(200, { keys: [jwk] });
			if (req.method === 'GET' && url.pathname === '/authorize') return authorize(url);
			if (req.method === 'POST' && url.pathname === '/token') return token(req);
			return new Response(null, { status: 404 });
		},
		// Overrides for the next code only; `auth_time` or `preferred_username` null omits that claim.
		next(claims: Partial<IssuerClaims>): void {
			pending = { ...claims };
		},
		// null ends the live session.
		setLiveSession(sub: string | null): void {
			liveSession = sub;
		},
		down(on: boolean): void {
			isDown = on;
		}
	};
	return api;
}

// A fresh CA and localhost leaf per run (OpenSSL 3), so no key is ever committed.
function makeTls(): { key: Buffer; cert: Buffer } {
	rmSync(TLS_DIR, { recursive: true, force: true });
	mkdirSync(TLS_DIR, { recursive: true });
	const ec = ['-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256', '-nodes', '-days', '1'];
	const run = (args: string[]) => execFileSync('openssl', args, { cwd: TLS_DIR, stdio: 'pipe' });
	run([
		'req',
		'-x509',
		...ec,
		'-subj',
		'/CN=commsgun-e2e-ca',
		'-keyout',
		'ca-key.pem',
		'-out',
		'ca.pem'
	]);
	run([
		'req',
		'-x509',
		...ec,
		'-subj',
		'/CN=localhost',
		'-addext',
		'subjectAltName=DNS:localhost',
		'-addext',
		'basicConstraints=critical,CA:FALSE',
		'-CA',
		'ca.pem',
		'-CAkey',
		'ca-key.pem',
		'-keyout',
		'key.pem',
		'-out',
		'cert.pem'
	]);
	return {
		key: readFileSync(resolve(TLS_DIR, 'key.pem')),
		cert: readFileSync(resolve(TLS_DIR, 'cert.pem'))
	};
}

// prompt=login makes a real IdP show its own page, so the callback is a navigation this site starts:
// cross-site for the UI's Strict cookie. createIssuer keeps the bare 302 for the unit tests.
function signInPage(url: URL, r: Response): string | null {
	if (url.pathname !== '/authorize' || url.searchParams.get('prompt') !== 'login') return null;
	const location = r.headers.get('location');
	if (r.status !== 302 || !location) return null;
	const href = location.replace(/&/g, '&amp;').replace(/"/g, '&quot;');
	return `<!doctype html><meta http-equiv="refresh" content="0; url=${href}"><title>Sign in</title>`;
}

export function startIssuer() {
	const issuer = createIssuer(ISSUER_URL);
	const server = createServer(makeTls(), async (req, res) => {
		const chunks: Buffer[] = [];
		for await (const chunk of req) chunks.push(chunk as Buffer);
		const headers = Object.entries(req.headers).flatMap(([k, v]) =>
			typeof v === 'string' ? [[k, v] as [string, string]] : []
		);
		const method = req.method ?? 'GET';
		const r = await issuer.handle(
			new Request(new URL(req.url ?? '/', ISSUER_URL), {
				method,
				headers,
				body: method === 'GET' || method === 'HEAD' ? undefined : Buffer.concat(chunks)
			})
		);
		const page = signInPage(new URL(req.url ?? '/', ISSUER_URL), r);
		if (page) {
			res.writeHead(200, { 'content-type': 'text/html; charset=utf-8' });
			res.end(page);
			return;
		}
		res.writeHead(r.status, Object.fromEntries(r.headers));
		res.end(Buffer.from(await r.arrayBuffer()));
	});
	server.listen(ISSUER_PORT, 'localhost');
	return server;
}

if (process.argv[1] && resolve(process.argv[1]) === import.meta.filename) startIssuer();
