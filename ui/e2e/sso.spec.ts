import { expect, test, type APIRequestContext, type BrowserContext } from '@playwright/test';
import { OIDC_COOKIE, OIDC_NEXT_MAX, SESSION_COOKIE } from '../src/lib/server/constants.js';
import {
	oidcSessions,
	setAuth,
	setScenario,
	STUB_LINKED_SUB,
	STUB_SESSION_TOKEN,
	TEST_ADMIN_PASSWORD
} from './stub-api.js';
import { ISSUER_URL, type IssuerClaims } from './stub-issuer.js';

async function issuer(request: APIRequestContext, name: string, data: unknown): Promise<void> {
	const res = await request.post(`${ISSUER_URL}/__issuer/${name}`, { data });
	expect(res.status()).toBe(204);
}
const nextClaims = (request: APIRequestContext, claims: Partial<IssuerClaims>) =>
	issuer(request, 'next', claims);

const cookie = async (context: BrowserContext, name: string) =>
	(await context.cookies()).find((c) => c.name === name);

const AUTHORIZE_KEYS = [
	'client_id',
	'code_challenge',
	'code_challenge_method',
	'nonce',
	'redirect_uri',
	'response_type',
	'scope',
	'state'
];

// The browser drives the IdP round trip; only Review Focus 2 drives the password form, from its own address.
test.beforeEach(async ({ context, request }, testInfo) => {
	await setScenario('normal');
	await setAuth({ mode: 'both' });
	await issuer(request, 'down', { on: false });
	await nextClaims(request, { sub: STUB_LINKED_SUB });
	await context.setExtraHTTPHeaders({
		'x-forwarded-proto': 'http',
		'x-forwarded-for': `test-${testInfo.testId}`
	});
});

test('mode both shows the password form and "Sign in with SSO"; mode oidc has no password field', async ({
	page
}) => {
	await page.goto('/login');
	await expect(page.getByLabel('Password')).toBeVisible();
	await expect(page.getByRole('link', { name: 'Sign in with SSO' })).toBeVisible();

	await setAuth({ mode: 'oidc' });
	await page.goto('/login');
	await expect(page.getByRole('link', { name: 'Sign in with SSO' })).toBeVisible();
	await expect(page.getByLabel('Password')).toHaveCount(0);

	await setAuth({ mode: 'local' });
	await page.goto('/login');
	await expect(page.getByLabel('Password')).toBeVisible();
	await expect(page.getByRole('link', { name: 'Sign in with SSO' })).toHaveCount(0);
});

test('SSO success lands on next signed in through a same-site step', async ({ page, context }) => {
	await page.goto('/login?next=%2Fmessages');
	const start = page.waitForResponse((r) => new URL(r.url()).pathname === '/login/sso');
	await page.getByRole('link', { name: 'Sign in with SSO' }).click();
	const res = await start;
	expect(res.status()).toBe(303);
	expect(res.headers()['location']).toMatch(new RegExp(`^${ISSUER_URL}/authorize\\?`));
	const set = (await res.headerValue('set-cookie')) ?? '';
	expect(set).toMatch(new RegExp(`^${OIDC_COOKIE}=[A-Za-z0-9_-]+;`));
	for (const flag of [/HttpOnly/i, /Secure/i, /SameSite=Lax/i, /Path=\/auth(;|$)/, /Max-Age=600/]) {
		expect(set).toMatch(flag);
	}

	await expect(page).toHaveURL(/127\.0\.0\.1:\d+\/messages$/);
	await expect(page.getByRole('button', { name: 'Sign out' })).toBeVisible();
	expect(await cookie(context, SESSION_COOKIE)).toMatchObject({
		value: STUB_SESSION_TOKEN,
		httpOnly: true,
		secure: true,
		sameSite: 'Strict',
		path: '/'
	});
	expect(await cookie(context, OIDC_COOKIE)).toBeUndefined();
	expect(await oidcSessions()).toEqual([{ iss: ISSUER_URL, sub: STUB_LINKED_SUB }]);
});

test('the callback answers 200 with a meta refresh and a Continue link to next', async ({
	page,
	context
}) => {
	const start = await context.request.get('/login/sso?next=%2Fmessages', { maxRedirects: 0 });
	expect(start.status()).toBe(303);
	const idp = await context.request.get(start.headers()['location'], { maxRedirects: 0 });
	expect(idp.status()).toBe(302);
	const callback = await context.request.get(idp.headers()['location'], { maxRedirects: 0 });
	expect(callback.status()).toBe(200);
	const html = await callback.text();
	expect(html).toMatch(/<meta http-equiv="refresh" content="0; url=\/messages"/);
	expect(html).toMatch(/<a [^>]*href="\/messages"[^>]*>Continue<\/a>/);
	expect(await cookie(context, OIDC_COOKIE)).toBeUndefined();
	expect((await cookie(context, SESSION_COOKIE))?.value).toBe(STUB_SESSION_TOKEN);

	await page.goto('/messages');
	await expect(page).toHaveURL(/\/messages$/);
	await expect(page.getByRole('button', { name: 'Sign out' })).toBeVisible();
});

test('an unlinked sub is refused: no session cookie, transaction cookie cleared', async ({
	page,
	context,
	request
}) => {
	await nextClaims(request, { sub: 'fake-sub-unlinked' });
	await page.goto('/login');
	await page.getByRole('link', { name: 'Sign in with SSO' }).click();
	await expect(page.getByText('This SSO account is not linked to a commsgun user')).toBeVisible();
	expect(await cookie(context, SESSION_COOKIE)).toBeUndefined();
	expect(await cookie(context, OIDC_COOKIE)).toBeUndefined();
});

test('a mode that refuses SSO sessions says SSO sign-in is off', async ({ page, context }) => {
	await setAuth({ mode: 'local' });
	await page.goto('/login/sso');
	await expect(page.getByText('SSO sign-in is turned off')).toBeVisible();
	expect(await cookie(context, SESSION_COOKIE)).toBeUndefined();
	expect(await cookie(context, OIDC_COOKIE)).toBeUndefined();
});

test('a tampered state, or no transaction cookie, has expired', async ({ page, context }) => {
	const start = await context.request.get('/login/sso', { maxRedirects: 0 });
	const idp = await context.request.get(start.headers()['location'], { maxRedirects: 0 });
	const tampered = new URL(idp.headers()['location']);
	tampered.searchParams.set('state', 'tampered');
	expect(await cookie(context, OIDC_COOKIE)).toBeDefined();
	await page.goto(tampered.href);
	await expect(page.getByText('This sign-in has expired. Start again.')).toBeVisible();
	expect(await cookie(context, OIDC_COOKIE)).toBeUndefined();

	await context.clearCookies();
	await page.goto('/auth/callback?code=fake-code&state=fake-state');
	await expect(page.getByText('This sign-in has expired. Start again.')).toBeVisible();
	expect(await oidcSessions()).toEqual([]);
	expect(await cookie(context, SESSION_COOKIE)).toBeUndefined();
});

// Review Focus 2: an unreachable issuer in `both` never blocks the password login.
test('with the issuer down /login renders, SSO says unavailable and the password still works', async ({
	page,
	request
}) => {
	await issuer(request, 'down', { on: true });
	const login = await page.goto('/login');
	expect(login?.status()).toBe(200);
	await page.getByRole('link', { name: 'Sign in with SSO' }).click();
	await expect(page).toHaveURL(/\/login\?sso=unavailable$/);
	await expect(page.getByText('SSO is unavailable. Try again later.')).toBeVisible();

	await page.getByLabel('Username').fill('admin');
	await page.getByLabel('Password').fill(TEST_ADMIN_PASSWORD);
	await page.getByRole('button', { name: 'Sign in' }).click();
	await expect(page.getByRole('button', { name: 'Sign out' })).toBeVisible();
	await expect(page).toHaveURL(/127\.0\.0\.1:\d+\/$/);
});

// Review Focus 3: a hostile next never reaches the IdP and lands on "/".
for (const [name, next] of [
	['//evil.example', '//evil.example'],
	['/\\evil', '/\\evil'],
	['/login', '/login'],
	['a 3000-character path', '/' + 'a'.repeat(2999)]
]) {
	test(`a hostile next (${name}) lands on "/"`, async ({ page }) => {
		const authorize = page.waitForRequest((r) => r.url().startsWith(`${ISSUER_URL}/authorize`));
		await page.goto(`/login/sso?next=${encodeURIComponent(next)}`);
		const url = new URL((await authorize).url());
		expect([...url.searchParams.keys()].sort()).toEqual(AUTHORIZE_KEYS);
		expect(url.searchParams.get('redirect_uri')).toMatch(
			/^http:\/\/127\.0\.0\.1:\d+\/auth\/callback$/
		);
		expect(url.href).not.toContain('evil');
		expect(url.href).not.toContain('a'.repeat(20));
		await expect(page).toHaveURL(/127\.0\.0\.1:\d+\/$/);
		await expect(page.getByRole('button', { name: 'Sign out' })).toBeVisible();
	});
}

test('a next of exactly OIDC_NEXT_MAX characters survives the round trip', async ({
	page,
	context
}) => {
	const next = '/' + 'a'.repeat(OIDC_NEXT_MAX - 1);
	expect(next).toHaveLength(OIDC_NEXT_MAX);
	await page.goto(`/login/sso?next=${encodeURIComponent(next)}`);
	await expect(page).toHaveURL(new RegExp(`127\\.0\\.0\\.1:\\d+${next}$`));
	expect((await cookie(context, SESSION_COOKIE))?.value).toBe(STUB_SESSION_TOKEN);
	expect(await cookie(context, OIDC_COOKIE)).toBeUndefined();
});
