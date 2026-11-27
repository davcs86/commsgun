import { error, redirect, text } from '@sveltejs/kit';
import type { Handle, ServerInit } from '@sveltejs/kit/hooks';
import { createApi } from './lib/server/api';
import { importAdmin } from './lib/server/bootstrap';
import { getEnv } from './lib/server/env';
import { SESSION_COOKIE, SESSION_TOKEN } from './lib/server/constants';

const PUBLIC_PATHS = new Set(['/login', '/logout', '/healthz', '/login/sso', '/auth/callback']);

// adapter-node awaits init before it listens, so /healthz waits for the import and a throw stops the process.
export const init: ServerInit = async () => {
	const env = getEnv();
	if (!env.adminPassword) return;
	if ((await importAdmin(createApi(env, fetch), env.adminPassword)) === 'skipped') {
		console.warn(JSON.stringify({ level: 'warn', msg: 'ADMIN_PASSWORD is ignored: users exist' }));
	}
};

// Only a well-formed cookie reaches the API. A refused session deletes the cookie;
// an unreachable API is a 503 that keeps it (never a sign-out).
export const handle: Handle = async ({ event, resolve }) => {
	const env = getEnv();
	const { pathname } = event.url;
	if (pathname !== '/healthz' && event.url.origin !== env.origin) {
		return text('Bad origin', { status: 400 });
	}
	event.locals.session = null;
	const isPublic = PUBLIC_PATHS.has(pathname);
	const token = pathname === '/healthz' ? undefined : event.cookies.get(SESSION_COOKIE);
	if (token !== undefined && !SESSION_TOKEN.test(token)) {
		event.cookies.delete(SESSION_COOKIE, { path: '/' });
	} else if (token !== undefined) {
		const r = await createApi(env, event.fetch, token).session();
		if (r.ok) {
			const { kind, expires_at: expiresAt, user } = r.data;
			event.locals.session = { token, kind, expiresAt, user };
		} else if (r.error === 'session') {
			event.cookies.delete(SESSION_COOKIE, { path: '/' });
		} else if (!isPublic) {
			error(503, 'The delivery API is not responding');
		}
	}
	event.locals.authed = event.locals.session !== null;
	if (!event.locals.authed && !isPublic) {
		const next = encodeURIComponent(pathname + event.url.search);
		redirect(303, `/login?next=${next}`);
	}
	return resolve(event);
};
