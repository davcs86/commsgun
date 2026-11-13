import { fail, redirect } from '@sveltejs/kit';
import { getEnv } from '#lib/server/env.js';
import { ADMIN_USERNAME, SESSION_COOKIE, SESSION_TTL_MS } from '#lib/server/constants.js';
import { LoginLimiter, checkPassword, safeNext, signSession } from '#lib/server/session.js';
import type { Actions, PageServerLoad } from './$types';

const limiter = new LoginLimiter();

export const load: PageServerLoad = ({ locals, url }) => {
	if (locals.authed) redirect(303, safeNext(url.searchParams.get('next')));
};

export const actions: Actions = {
	default: async ({ request, cookies, url, getClientAddress }) => {
		if (!limiter.allow(getClientAddress(), Date.now())) {
			return fail(429, { message: 'Too many attempts. Try again in a minute.' });
		}
		const form = await request.formData();
		const username = String(form.get('username') ?? '');
		const password = String(form.get('password') ?? '');
		const { adminPassword, sessionSecret } = getEnv();
		const userOk = checkPassword(username, ADMIN_USERNAME);
		const passwordOk = checkPassword(password, adminPassword);
		if (!(userOk && passwordOk)) {
			return fail(400, { message: 'Incorrect username or password' });
		}
		cookies.set(SESSION_COOKIE, signSession(sessionSecret, Date.now()), {
			path: '/',
			httpOnly: true,
			secure: true,
			sameSite: 'strict',
			maxAge: SESSION_TTL_MS / 1000
		});
		redirect(303, safeNext(url.searchParams.get('next')));
	}
};
