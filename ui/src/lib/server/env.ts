import * as privateEnv from '$app/env/private';
import { MIN_ADMIN_API_TOKEN, MIN_ADMIN_PASSWORD, MIN_SESSION_SECRET } from './constants';

export type Env = {
	// Import-only (spec 12.6): unset or empty is null; counted in code points.
	adminPassword: string | null;
	sessionSecret: string;
	adminApiToken: string;
	apiBaseUrl: string;
	origin: string;
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

	if (bad.length > 0) throw new Error(`invalid environment: ${bad.join(', ')}`);
	return { ...env, adminPassword: env.adminPassword || null, origin: new URL(env.origin).origin };
}

let cached: Env | undefined;

export function getEnv(): Env {
	cached ??= loadEnv(privateEnv);
	return cached;
}
