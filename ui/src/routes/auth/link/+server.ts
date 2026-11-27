import { redirect, text } from '@sveltejs/kit';
import { createApi } from '#lib/server/api.js';
import { getEnv } from '#lib/server/env.js';
import { OIDC_COOKIE } from '#lib/server/constants.js';
import { bindingMatches, openTransaction } from '#lib/server/oidc-cookie.js';
import type { RequestHandler } from './$types';

const OUTCOMES = new Map([
	['already_linked', 'already_linked'],
	['reauth_required', 'reauth_required']
]);

async function decide(request: Request): Promise<string> {
	try {
		return String((await request.formData()).get('decision') ?? '');
	} catch {
		return '';
	}
}

// Not a public path: the hook has resolved the session. The Origin check is the probe's (UI-01);
// past it the transaction cookie is cleared whatever the outcome.
export const POST: RequestHandler = async ({ request, cookies, fetch, locals }) => {
	const env = getEnv();
	if (request.headers.get('origin') !== env.origin) {
		return text('Bad origin', { status: 403, headers: { 'cache-control': 'no-store' } });
	}
	const tx = openTransaction(env.oidcKey, cookies.get(OIDC_COOKIE), Date.now());
	cookies.delete(OIDC_COOKIE, { path: '/auth' });
	const decision = await decide(request);
	if (decision === 'cancel') redirect(303, '/account?link=cancelled');

	const session = locals.session;
	const { iss, sub, binding } = tx?.purpose === 'link' ? tx : {};
	if (
		decision !== 'confirm' ||
		!session ||
		!iss ||
		!sub ||
		!binding ||
		!bindingMatches(session.token, binding)
	) {
		console.warn(JSON.stringify({ level: 'warn', msg: 'sso link refused', reason: 'binding' }));
		redirect(303, '/account?link=error');
	}
	const r = await createApi(env, fetch, session.token).linkIdentity(iss, sub);
	const outcome = r.ok ? 'linked' : (OUTCOMES.get(r.error) ?? 'error');
	redirect(303, `/account?link=${outcome}`);
};
