import type { AuthMode } from './types.js';

export type LoginOptions = { password: boolean; sso: 'button' | 'not_configured' | 'hidden' };

// A null mode means the API did not answer: the password form is still offered.
export function loginOptions(mode: AuthMode | null, ssoConfigured: boolean): LoginOptions {
	const sso =
		mode === 'oidc' || mode === 'both' ? (ssoConfigured ? 'button' : 'not_configured') : 'hidden';
	return { password: mode !== 'oidc', sso };
}
