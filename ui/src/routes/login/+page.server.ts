import { fail, redirect } from '@sveltejs/kit';
import { createApi } from '#lib/server/api.js';
import { getEnv } from '#lib/server/env.js';
import { SESSION_COOKIE } from '#lib/server/constants.js';
import { passwordLimiter } from '#lib/server/limiter.js';
import { safeNext } from '#lib/server/session.js';
import type { Actions, PageServerLoad } from './$types';

export const load: PageServerLoad = ({ locals, url }) => {
	if (locals.authed) redirect(303, safeNext(url.searchParams.get('next')));
};

export const actions: Actions = {
	default: async ({ request, cookies, url, getClientAddress, fetch, locals }) => {
		if (!passwordLimiter.allow(getClientAddress(), Date.now())) {
			return fail(429, { message: 'Too many attempts. Try again in a minute.' });
		}
		const form = await request.formData();
		const username = String(form.get('username') ?? '');
		const password = String(form.get('password') ?? '');
		const r = await createApi(getEnv(), fetch, locals.session?.token).createLocalSession(
			username,
			password
		);
		if (!r.ok) {
			if (r.error === 'invalid_credentials') {
				return fail(400, { message: 'Incorrect username or password' });
			}
			if (r.error === 'mode') return fail(403, { message: 'Password sign-in is turned off' });
			if (r.error === 'unavailable') {
				return fail(503, { message: 'The delivery API is not responding' });
			}
			return fail(502, { message: 'The delivery API returned an error' });
		}
		const maxAge = Math.floor((Date.parse(r.data.expires_at) - Date.now()) / 1000);
		if (!(maxAge > 0)) return fail(502, { message: 'The delivery API returned an error' });
		cookies.set(SESSION_COOKIE, r.data.token, {
			path: '/',
			httpOnly: true,
			secure: true,
			sameSite: 'strict',
			maxAge
		});
		redirect(303, safeNext(url.searchParams.get('next')));
	}
};
