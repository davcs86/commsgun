import { redirect, text } from '@sveltejs/kit';
import type { Handle, ServerInit } from '@sveltejs/kit/hooks';
import { getEnv } from './lib/server/env';
import { SESSION_COOKIE } from './lib/server/constants';
import { verifySession } from './lib/server/session';

const PUBLIC_PATHS = new Set(['/login', '/logout', '/healthz']);

export const init: ServerInit = () => {
	getEnv();
};

export const handle: Handle = async ({ event, resolve }) => {
	const { sessionKey, origin } = getEnv();
	if (event.url.pathname !== '/healthz' && event.url.origin !== origin) {
		return text('Bad origin', { status: 400 });
	}
	event.locals.authed = verifySession(sessionKey, event.cookies.get(SESSION_COOKIE), Date.now());
	if (!event.locals.authed && !PUBLIC_PATHS.has(event.url.pathname)) {
		const next = encodeURIComponent(event.url.pathname + event.url.search);
		redirect(303, `/login?next=${next}`);
	}
	return resolve(event);
};
