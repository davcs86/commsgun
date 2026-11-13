import { defineEnvVars } from '@sveltejs/kit/env';

// Lenient on purpose: loadEnv() in lib/server/env.ts owns validation and its error messages.
const optional = { schema: (value: string | undefined) => value ?? '' };

export const variables = defineEnvVars({
	ADMIN_PASSWORD: optional,
	SESSION_SECRET: optional,
	ADMIN_API_TOKEN: optional,
	API_BASE_URL: optional,
	ORIGIN: optional
});
