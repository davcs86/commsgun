import { createApi } from '#lib/server/api.js';
import { getEnv } from '#lib/server/env.js';
import { OIDC_COOKIE, SESSION_COOKIE } from '#lib/server/constants.js';
import { completeAuth } from '#lib/server/oidc.js';
import { openTransaction } from '#lib/server/oidc-cookie.js';
import type { PageServerLoad } from './$types';

const EXPIRED = 'This sign-in has expired. Start again.';

function refused(reason: string, message: string) {
	console.warn(JSON.stringify({ level: 'warn', msg: 'sso callback refused', reason }));
	return { next: null, message };
}

// Reads no session: the SameSite=Strict cookie is absent after a cross-site IdP.
// A login clears the transaction cookie whatever the outcome.
export const load: PageServerLoad = async ({ url, cookies, fetch }) => {
	const env = getEnv();
	const tx = openTransaction(env.oidcKey, cookies.get(OIDC_COOKIE), Date.now());
	cookies.delete(OIDC_COOKIE, { path: '/auth' });
	if (!tx || tx.purpose !== 'login') return refused('state', EXPIRED);
	if (!env.oidc) return refused('not_configured', 'SSO is not configured');

	const auth = await completeAuth({ ...env.oidc, origin: env.origin }, tx, url);
	if (!auth.ok) {
		if (auth.reason === 'state') return refused('state', EXPIRED);
		if (auth.reason === 'unavailable') {
			return refused('unavailable', 'SSO is unavailable. Try again later.');
		}
		return refused('token', 'SSO sign-in failed. Start again.');
	}

	const r = await createApi(env, fetch).createOidcSession(auth.iss, auth.sub);
	if (!r.ok) {
		if (r.error === 'not_linked') {
			return refused('not_linked', 'This SSO account is not linked to a commsgun user');
		}
		if (r.error === 'mode') return refused('mode', 'SSO sign-in is turned off');
		if (r.error === 'unavailable')
			return { next: null, message: 'The delivery API is not responding' };
		return { next: null, message: 'The delivery API returned an error' };
	}
	const maxAge = Math.floor((Date.parse(r.data.expires_at) - Date.now()) / 1000);
	if (!(maxAge > 0)) return { next: null, message: 'The delivery API returned an error' };
	cookies.set(SESSION_COOKIE, r.data.token, {
		path: '/',
		httpOnly: true,
		secure: true,
		sameSite: 'strict',
		maxAge
	});
	// A 200, never a redirect: the next step is same-site, so it carries the Strict cookie.
	return { next: tx.next, message: null };
};
