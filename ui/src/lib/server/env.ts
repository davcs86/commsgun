import { createHmac } from 'node:crypto';
import * as privateEnv from '$app/env/private';
import {
	DEFAULT_OIDC_SCOPES,
	MIN_ADMIN_API_TOKEN,
	MIN_ADMIN_PASSWORD,
	MIN_SESSION_SECRET,
	OIDC_KEY_LABEL
} from './constants';

export type OidcConfig = { issuer: string; clientId: string; clientSecret: string; scopes: string };

export type Env = {
	// Import-only (spec 12.6): unset or empty is null; counted in code points.
	adminPassword: string | null;
	sessionSecret: string;
	adminApiToken: string;
	apiBaseUrl: string;
	origin: string;
	// All three of OIDC_ISSUER, OIDC_CLIENT_ID, OIDC_CLIENT_SECRET, or null (spec 12.2).
	oidc: OidcConfig | null;
	oidcKey: Buffer;
};

function isHttpUrl(value: string): boolean {
	return URL.canParse(value) && /^https?:$/.test(new URL(value).protocol);
}

// Error messages name the variable only, never its value.
export function loadEnv(source: Record<string, string | undefined>): Env {
	const bad: string[] = [];
	const read = (key: string, ok: (value: string) => boolean): string => {
		const value = source[key] ?? '';
		if (!ok(value)) bad.push(key);
		return value;
	};

	const env = {
		adminPassword: read('ADMIN_PASSWORD', (v) => v === '' || [...v].length >= MIN_ADMIN_PASSWORD),
		sessionSecret: read('SESSION_SECRET', (v) => v.length >= MIN_SESSION_SECRET),
		adminApiToken: read('ADMIN_API_TOKEN', (v) => v.length >= MIN_ADMIN_API_TOKEN),
		apiBaseUrl: read('API_BASE_URL', isHttpUrl),
		origin: read('ORIGIN', isHttpUrl)
	};

	const issuer = source.OIDC_ISSUER ?? '';
	const clientId = source.OIDC_CLIENT_ID ?? '';
	const clientSecret = source.OIDC_CLIENT_SECRET ?? '';
	const oidcSet = [issuer, clientId, clientSecret].some((v) => v !== '');
	if (oidcSet) {
		// https only, loopback included (owner, 2026-11-27).
		const https = URL.canParse(issuer) && new URL(issuer).protocol === 'https:';
		if (!https) bad.push('OIDC_ISSUER');
		if (clientId === '') bad.push('OIDC_CLIENT_ID');
		if (clientSecret === '') bad.push('OIDC_CLIENT_SECRET');
	}
	// Not implemented (spec 12.2): refused even when empty.
	if (source.OIDC_AUTO_PROVISION !== undefined) bad.push('OIDC_AUTO_PROVISION');

	if (bad.length > 0) throw new Error(`invalid environment: ${bad.join(', ')}`);
	return {
		...env,
		adminPassword: env.adminPassword || null,
		origin: new URL(env.origin).origin,
		oidc: oidcSet
			? { issuer, clientId, clientSecret, scopes: source.OIDC_SCOPES || DEFAULT_OIDC_SCOPES }
			: null,
		oidcKey: createHmac('sha256', env.sessionSecret).update(OIDC_KEY_LABEL).digest()
	};
}

let cached: Env | undefined;

export function getEnv(): Env {
	cached ??= loadEnv(privateEnv);
	return cached;
}
