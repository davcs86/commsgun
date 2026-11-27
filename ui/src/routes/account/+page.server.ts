import { fail, redirect } from '@sveltejs/kit';
import { createApi } from '#lib/server/api.js';
import { getEnv } from '#lib/server/env.js';
import { OIDC_COOKIE, OIDC_TX_TTL_MS, SESSION_COOKIE } from '#lib/server/constants.js';
import { passwordLimiter } from '#lib/server/limiter.js';
import { beginAuth, logSsoNotConfigured } from '#lib/server/oidc.js';
import { bindingOf, sealTransaction } from '#lib/server/oidc-cookie.js';
import type { Actions, PageServerLoad } from './$types';

const NEEDS_BOTH = 'Linking SSO needs both password and SSO sign-in turned on';
const NEEDS_LOCAL = 'Linking SSO needs a password sign-in: sign out and sign in with your password';
const NOT_CONFIGURED = 'SSO is not configured';

const OUTCOMES = new Map([
	['linked', { ok: true, text: 'SSO account linked' }],
	['cancelled', { ok: true, text: 'Linking cancelled' }],
	['error', { ok: false, text: 'Linking failed. Try again.' }],
	['already_linked', { ok: false, text: 'That SSO account is already linked' }],
	[
		'reauth_required',
		{ ok: false, text: 'Confirm your password again: the 5-minute window passed' }
	]
]);

// The API enforces mode and session kind too (spec 12.5); this only decides whether to show the form.
export const load: PageServerLoad = async ({ locals, url, fetch }) => {
	const env = getEnv();
	const session = locals.session;
	const r = await createApi(env, fetch, session?.token).authMode();
	let reason: string | null = null;
	if (!env.oidc) {
		logSsoNotConfigured();
		reason = NOT_CONFIGURED;
	} else if (r.ok && r.data.mode !== 'both') reason = NEEDS_BOTH;
	else if (session?.kind !== 'local') reason = NEEDS_LOCAL;
	return {
		username: session?.user.username ?? '',
		showForm: r.ok && reason === null,
		reason,
		apiError: r.ok
			? null
			: r.error === 'unavailable'
				? ('unavailable' as const)
				: ('error' as const),
		outcome: OUTCOMES.get(url.searchParams.get('link') ?? '') ?? null
	};
};

export const actions: Actions = {
	default: async ({ request, cookies, getClientAddress, fetch, locals }) => {
		if (!passwordLimiter.allow(getClientAddress(), Date.now())) {
			return fail(429, { message: 'Too many attempts. Try again in a minute.' });
		}
		const env = getEnv();
		const session = locals.session;
		if (!session) redirect(303, '/login?next=%2Faccount');
		if (!env.oidc) return fail(400, { message: NOT_CONFIGURED });
		const form = await request.formData();
		const r = await createApi(env, fetch, session.token).reauth(String(form.get('password') ?? ''));
		if (!r.ok) {
			if (r.error === 'invalid_credentials') return fail(400, { message: 'Incorrect password' });
			if (r.error === 'session') {
				cookies.delete(SESSION_COOKIE, { path: '/' });
				redirect(303, '/login?next=%2Faccount');
			}
			if (r.error === 'mode') return fail(403, { message: NEEDS_BOTH });
			if (r.error === 'session_kind') return fail(403, { message: NEEDS_LOCAL });
			if (r.error === 'unavailable') {
				return fail(503, { message: 'The delivery API is not responding' });
			}
			return fail(502, { message: 'The delivery API returned an error' });
		}

		let started: Awaited<ReturnType<typeof beginAuth>>;
		try {
			started = await beginAuth(
				{ ...env.oidc, origin: env.origin },
				{
					purpose: 'link',
					next: '/account',
					binding: bindingOf(session.token),
					username: session.user.username
				},
				Date.now()
			);
		} catch {
			console.error(JSON.stringify({ level: 'error', msg: 'sso link start failed' }));
			return fail(503, { message: 'SSO is unavailable. Try again later.' });
		}
		cookies.set(OIDC_COOKIE, sealTransaction(env.oidcKey, started.tx), {
			path: '/auth',
			httpOnly: true,
			secure: true,
			sameSite: 'lax',
			maxAge: OIDC_TX_TTL_MS / 1000
		});
		redirect(303, started.url.href, { external: true });
	}
};
