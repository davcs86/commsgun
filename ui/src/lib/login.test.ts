import { describe, expect, it } from 'vitest';
import { loginOptions } from './login';

describe('loginOptions', () => {
	it('local → password only', () => {
		expect(loginOptions('local', true)).toEqual({ password: true, sso: 'hidden' });
		expect(loginOptions('local', false)).toEqual({ password: true, sso: 'hidden' });
	});

	it('oidc + configured → sso only', () => {
		expect(loginOptions('oidc', true)).toEqual({ password: false, sso: 'button' });
	});

	it('oidc unconfigured → not_configured', () => {
		expect(loginOptions('oidc', false)).toEqual({ password: false, sso: 'not_configured' });
	});

	it('both → both', () => {
		expect(loginOptions('both', true)).toEqual({ password: true, sso: 'button' });
		expect(loginOptions('both', false)).toEqual({ password: true, sso: 'not_configured' });
	});

	it('null (API unreachable) → password', () => {
		expect(loginOptions(null, true)).toEqual({ password: true, sso: 'hidden' });
		expect(loginOptions(null, false)).toEqual({ password: true, sso: 'hidden' });
	});
});
