import { redirect } from '@sveltejs/kit';
import { createApi } from '#lib/server/api.js';
import { getEnv } from '#lib/server/env.js';
import { SESSION_COOKIE } from '#lib/server/constants.js';
import type { RequestHandler } from './$types';

// The cookie is cleared whatever the API answers.
export const POST: RequestHandler = async ({ cookies, fetch, locals }) => {
	if (locals.session) await createApi(getEnv(), fetch, locals.session.token).deleteSession();
	cookies.delete(SESSION_COOKIE, { path: '/' });
	redirect(303, '/login');
};
