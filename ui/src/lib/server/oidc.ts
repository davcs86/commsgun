import * as client from 'openid-client';
import { AUTH_TIME_SKEW_MS, OIDC_TX_TTL_MS } from './constants';
import type { OidcConfig } from './env';
import type { Transaction } from './oidc-cookie';

export type OidcEnv = OidcConfig & { origin: string };

export type AuthResult =
	| { ok: true; iss: string; sub: string; preferredUsername: string | null }
	| { ok: false; reason: 'state' | 'token' | 'unavailable' };

// Discovery on every call, so an issuer outage is seen at once and each callback verifies on a key
// set fetched within it. Insecure requests are never enabled: openid-client refuses a non-https issuer.
async function discover(oidc: OidcEnv, fetchFn?: typeof fetch): Promise<client.Configuration> {
	const config = await client.discovery(
		new URL(oidc.issuer),
		oidc.clientId,
		{ id_token_signed_response_alg: 'RS256' },
		client.ClientSecretBasic(oidc.clientSecret),
		fetchFn ? { [client.customFetch]: fetchFn as client.CustomFetch } : undefined
	);
	client.enableNonRepudiationChecks(config);
	return config;
}

export async function beginAuth(
	oidc: OidcEnv,
	input: { purpose: Transaction['purpose']; next: string; binding?: string; username?: string },
	now: number,
	fetchFn?: typeof fetch
): Promise<{ url: URL; tx: Transaction }> {
	const config = await discover(oidc, fetchFn);
	const verifier = client.randomPKCECodeVerifier();
	const state = client.randomState();
	const nonce = client.randomNonce();
	const params: Record<string, string> = {
		redirect_uri: `${oidc.origin}/auth/callback`,
		scope: oidc.scopes,
		code_challenge: await client.calculatePKCECodeChallenge(verifier),
		code_challenge_method: 'S256',
		state,
		nonce
	};
	// Not max_age=0, which Authelia refuses (SSO spec S17).
	if (input.purpose === 'link') Object.assign(params, { prompt: 'login', max_age: '1' });
	const tx: Transaction = {
		purpose: input.purpose,
		state,
		nonce,
		verifier,
		next: input.next,
		started: now,
		exp: now + OIDC_TX_TTL_MS,
		...(input.binding === undefined ? {} : { binding: input.binding }),
		...(input.username === undefined ? {} : { username: input.username })
	};
	return { url: client.buildAuthorizationUrl(config, params), tx };
}

// checks.maxAge is never passed: it compares auth_time with this clock, not the flow's start.
export async function completeAuth(
	oidc: OidcEnv,
	tx: Transaction,
	callbackUrl: URL,
	fetchFn?: typeof fetch,
	deps?: { jwksCache?: client.ExportedJWKSCache }
): Promise<AuthResult> {
	if (callbackUrl.searchParams.get('state') !== tx.state) return { ok: false, reason: 'state' };
	let config: client.Configuration;
	try {
		config = await discover(oidc, fetchFn);
	} catch {
		return { ok: false, reason: 'unavailable' };
	}
	if (deps?.jwksCache) client.setJwksCache(config, deps.jwksCache);
	let claims: client.IDToken | undefined;
	try {
		const tokens = await client.authorizationCodeGrant(config, callbackUrl, {
			pkceCodeVerifier: tx.verifier,
			expectedState: tx.state,
			expectedNonce: tx.nonce,
			idTokenExpected: true
		});
		claims = tokens.claims();
	} catch {
		return { ok: false, reason: 'token' };
	}
	if (!claims) return { ok: false, reason: 'token' };
	if (tx.purpose === 'link') {
		const authTime = claims.auth_time;
		if (typeof authTime !== 'number' || authTime * 1000 < tx.started - AUTH_TIME_SKEW_MS) {
			return { ok: false, reason: 'token' };
		}
	}
	const name = claims.preferred_username;
	return {
		ok: true,
		iss: claims.iss,
		sub: claims.sub,
		preferredUsername: typeof name === 'string' ? name : null
	};
}

let notConfiguredLogged = false;

// Once per process (spec 12.2).
export function logSsoNotConfigured(): void {
	if (notConfiguredLogged) return;
	notConfiguredLogged = true;
	console.warn(JSON.stringify({ level: 'warn', msg: 'SSO is not configured' }));
}
