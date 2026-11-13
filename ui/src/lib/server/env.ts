import * as privateEnv from '$app/env/private';

export type Env = {
	adminPassword: string;
	sessionSecret: string;
	adminApiToken: string;
	apiBaseUrl: string;
	origin: string;
};

const MIN_ADMIN_PASSWORD = 16;
const MIN_SESSION_SECRET = 32;
const MIN_ADMIN_API_TOKEN = 32;

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

	const env: Env = {
		adminPassword: read('ADMIN_PASSWORD', (v) => v.length >= MIN_ADMIN_PASSWORD),
		sessionSecret: read('SESSION_SECRET', (v) => v.length >= MIN_SESSION_SECRET),
		adminApiToken: read('ADMIN_API_TOKEN', (v) => v.length >= MIN_ADMIN_API_TOKEN),
		apiBaseUrl: read('API_BASE_URL', isHttpUrl),
		origin: read('ORIGIN', isHttpUrl)
	};

	if (bad.length > 0) throw new Error(`invalid environment: ${bad.join(', ')}`);
	return env;
}

let cached: Env | undefined;

export function getEnv(): Env {
	cached ??= loadEnv(privateEnv);
	return cached;
}
