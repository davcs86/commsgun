import { redirect } from '@sveltejs/kit';
import { getEnv } from '#lib/server/env.js';
import { OIDC_COOKIE, OIDC_TX_TTL_MS } from '#lib/server/constants.js';
import { beginAuth, logSsoNotConfigured } from '#lib/server/oidc.js';
import { sealTransaction } from '#lib/server/oidc-cookie.js';
import { safeNext } from '#lib/server/session.js';
import type { RequestHandler } from './$types';

// `next` travels only inside the sealed cookie, never to the IdP.
export const GET: RequestHandler = async ({ url, cookies }) => {
	const env = getEnv();
	if (!env.oidc) {
		logSsoNotConfigured();
		redirect(303, '/login?sso=not_configured');
	}
	let started: Awaited<ReturnType<typeof beginAuth>>;
	try {
		started = await beginAuth(
			{ ...env.oidc, origin: env.origin },
			{ purpose: 'login', next: safeNext(url.searchParams.get('next')) },
			Date.now()
		);
	} catch {
		console.error(JSON.stringify({ level: 'error', msg: 'sso start failed' }));
		redirect(303, '/login?sso=unavailable');
	}
	cookies.set(OIDC_COOKIE, sealTransaction(env.oidcKey, started.tx), {
		path: '/auth',
		httpOnly: true,
		secure: true,
		sameSite: 'lax',
		maxAge: OIDC_TX_TTL_MS / 1000
	});
	// Kit 3 refuses an external redirect unless asked; the URL is the issuer's https endpoint.
	redirect(303, started.url.href, { external: true });
};
