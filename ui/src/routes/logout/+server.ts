import { redirect } from '@sveltejs/kit';
import { createApi } from '#lib/server/api.js';
import { getEnv } from '#lib/server/env.js';
import { SESSION_COOKIE, SESSION_TOKEN } from '#lib/server/constants.js';
import type { RequestHandler } from './$types';

// Best effort: a well-formed cookie the hook could not validate is revoked too.
// The cookie is cleared whatever the API answers.
export const POST: RequestHandler = async ({ cookies, fetch, locals }) => {
	const raw = cookies.get(SESSION_COOKIE);
	const token = locals.session?.token ?? (raw && SESSION_TOKEN.test(raw) ? raw : undefined);
	if (token) await createApi(getEnv(), fetch, token).deleteSession();
	cookies.delete(SESSION_COOKIE, { path: '/' });
	redirect(303, '/login');
};
