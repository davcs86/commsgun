import { defineEnvVars } from '@sveltejs/kit/env';

// Lenient on purpose: loadEnv() in lib/server/env.ts owns validation and its error messages.
const optional = { schema: (value: string | undefined) => value ?? '' };
// Keeps undefined, so loadEnv can refuse a variable that is merely present.
const presence = { schema: (value: string | undefined) => value };

export const variables = defineEnvVars({
	ADMIN_PASSWORD: optional,
	SESSION_SECRET: optional,
	ADMIN_API_TOKEN: optional,
	API_BASE_URL: optional,
	ORIGIN: optional,
	OIDC_ISSUER: optional,
	OIDC_CLIENT_ID: optional,
	OIDC_CLIENT_SECRET: optional,
	OIDC_SCOPES: optional,
	OIDC_AUTO_PROVISION: presence
});
