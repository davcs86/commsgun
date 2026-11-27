import {
	expect,
	test,
	type APIRequestContext,
	type BrowserContext,
	type Page
} from '@playwright/test';
import { OIDC_COOKIE, SESSION_COOKIE } from '../src/lib/server/constants.js';
import {
	identityCalls,
	oidcSessions,
	setAuth,
	setScenario,
	STUB_SESSION_TOKEN,
	STUB_SESSION_TOKEN_2,
	TEST_ADMIN_PASSWORD as PASSWORD,
	type AuthSettings
} from './stub-api.js';
import {
	ISSUER_DEFAULT_SUB,
	ISSUER_DEFAULT_USERNAME,
	ISSUER_LIVE_USERNAME,
	ISSUER_URL,
	type IssuerClaims
} from './stub-issuer.js';

const LIVE_SUB = 'fake-sub-live-0001';
const CONFIRMATION = `You are linking ${ISSUER_DEFAULT_USERNAME} to admin`;

async function issuer(request: APIRequestContext, name: string, data: unknown): Promise<void> {
	const res = await request.post(`${ISSUER_URL}/__issuer/${name}`, { data });
	expect(res.status()).toBe(204);
}
const nextClaims = (request: APIRequestContext, claims: Partial<IssuerClaims>) =>
	issuer(request, 'next', claims);

const cookie = async (context: BrowserContext, name: string) =>
	(await context.cookies()).find((c) => c.name === name);

const isPost = (path: string) => (r: { request(): { method(): string }; url(): string }) =>
	r.request().method() === 'POST' && new URL(r.url()).pathname === path;

async function reauth(page: Page, password: string) {
	await page.getByLabel('Password').fill(password);
	const response = page.waitForResponse(isPost('/account'));
	await page.getByRole('button', { name: 'Link SSO' }).click();
	return response;
}

// As the UI sets it: Strict, so a cross-site navigation does not carry it.
const sessionCookie = (value: string, url: string | undefined) => ({
	name: SESSION_COOKIE,
	value,
	url,
	httpOnly: true,
	secure: true,
	sameSite: 'Strict' as const
});

// Re-auth on /account and follow the IdP round trip to the confirmation page.
async function startLink(page: Page) {
	await page.goto('/account');
	await reauth(page, PASSWORD);
	await expect(page).toHaveURL(/127\.0\.0\.1:\d+\/auth\/callback\?/);
	await expect(page.getByText(CONFIRMATION)).toBeVisible();
}

// Every spec starts signed in with the stub's session (UI-05); each test has its own limiter budget.
test.beforeEach(async ({ context, request, baseURL }, testInfo) => {
	await setScenario('normal');
	await setAuth({ mode: 'both' });
	await issuer(request, 'down', { on: false });
	await issuer(request, 'live-session', { sub: null });
	await nextClaims(request, {});
	await context.setExtraHTTPHeaders({
		'x-forwarded-proto': 'http',
		'x-forwarded-for': `test-${testInfo.testId}`
	});
	await context.addCookies([sessionCookie(STUB_SESSION_TOKEN, baseURL)]);
});

// The issuer is shared with sso.spec.ts: leave no live session or pending claims behind.
test.afterEach(async ({ request }) => {
	await issuer(request, 'live-session', { sub: null });
	await nextClaims(request, {});
});

test('mode both with a local session shows the form, reached from the header', async ({ page }) => {
	await page.goto('/');
	await page.getByRole('link', { name: 'Account' }).click();
	await expect(page).toHaveURL(/127\.0\.0\.1:\d+\/account$/);
	await expect(page.getByLabel('Password')).toBeVisible();
	await expect(page.getByRole('button', { name: 'Link SSO' })).toBeVisible();
	const overflow = await page.evaluate(
		() => document.documentElement.scrollWidth - document.documentElement.clientWidth
	);
	expect(overflow).toBe(0);
});

const noForm: [string, AuthSettings, string][] = [
	['mode local', { mode: 'local' }, 'Linking SSO needs both password and SSO sign-in turned on'],
	['mode oidc', { mode: 'oidc' }, 'Linking SSO needs both password and SSO sign-in turned on'],
	[
		'an oidc-kind session',
		{ mode: 'both', sessionKind: 'oidc' },
		'Linking SSO needs a password sign-in'
	]
];
for (const [name, auth, reason] of noForm) {
	test(`${name}: no form and a one-line reason`, async ({ page }) => {
		await setAuth(auth);
		await page.goto('/account');
		await expect(page.getByText(reason)).toBeVisible();
		await expect(page.getByLabel('Password')).toHaveCount(0);
		await expect(page.getByRole('button', { name: 'Link SSO' })).toHaveCount(0);
	});
}

test('a wrong password says "Incorrect password" and starts no link', async ({ page, context }) => {
	await page.goto('/account');
	const response = await reauth(page, 'not-the-password');
	expect(response.status()).toBe(400);
	await expect(page.getByText('Incorrect password')).toBeVisible();
	await expect(page).toHaveURL(/\/account$/);
	expect(await cookie(context, OIDC_COOKIE)).toBeUndefined();
});

test("5 failures share /login's budget: a 6th POST to /login or /account is 429", async ({
	page,
	request,
	baseURL
}, testInfo) => {
	const login = (password: string) =>
		request.post('/login', {
			form: { username: 'admin', password },
			headers: { origin: String(baseURL), 'x-forwarded-for': `test-${testInfo.testId}` },
			maxRedirects: 0
		});
	for (let i = 0; i < 3; i++) expect((await login('not-the-password')).status()).toBe(400);
	await page.goto('/account');
	for (let i = 0; i < 2; i++) expect((await reauth(page, 'not-the-password')).status()).toBe(400);

	expect((await reauth(page, PASSWORD)).status()).toBe(429);
	await expect(page.getByText('Too many attempts. Try again in a minute.')).toBeVisible();
	const sixth = await login(PASSWORD);
	expect(sixth.status()).toBe(429);
	expect(await sixth.text()).toContain('Too many attempts. Try again in a minute.');
});

// Conformance (SSO spec § 3): link while another IdP session is live.
test('the right password goes to the IdP with prompt=login and max_age=1; a live IdP session is not reused', async ({
	page,
	context,
	request
}) => {
	await issuer(request, 'live-session', { sub: LIVE_SUB });
	// Control: without prompt=login the stub answers with the live session's account.
	const start = await context.request.get('/login/sso', { maxRedirects: 0 });
	const idp = await context.request.get(start.headers()['location'], { maxRedirects: 0 });
	await context.request.get(idp.headers()['location'], { maxRedirects: 0 });
	expect(await oidcSessions()).toEqual([{ iss: ISSUER_URL, sub: LIVE_SUB }]);

	await page.goto('/account');
	const authorize = page.waitForRequest((r) => r.url().startsWith(`${ISSUER_URL}/authorize`));
	const response = await reauth(page, PASSWORD);
	expect(response.status()).toBe(303);
	const set = (await response.headerValue('set-cookie')) ?? '';
	expect(set).toMatch(new RegExp(`^${OIDC_COOKIE}=[A-Za-z0-9_-]+;`));
	for (const flag of [/HttpOnly/i, /Secure/i, /SameSite=Lax/i, /Path=\/auth(;|$)/, /Max-Age=600/]) {
		expect(set).toMatch(flag);
	}
	const url = new URL((await authorize).url());
	expect(url.searchParams.get('prompt')).toBe('login');
	expect(url.searchParams.get('max_age')).toBe('1');

	await expect(page.getByText(CONFIRMATION)).toBeVisible();
	await expect(page.getByText(ISSUER_LIVE_USERNAME)).toHaveCount(0);
	await page.getByRole('button', { name: 'Confirm' }).click();
	await expect(page.getByText('SSO account linked')).toBeVisible();
	expect((await identityCalls()).map((c) => c.body.sub)).toEqual([ISSUER_DEFAULT_SUB]);
});

test('the callback request carried no commsgun_session (cross-site) and still rendered the confirmation', async ({
	page
}) => {
	await page.goto('/account');
	const callback = page.waitForRequest((r) => new URL(r.url()).pathname === '/auth/callback');
	await reauth(page, PASSWORD);
	const sent = (await (await callback).allHeaders())['cookie'] ?? '';
	expect(sent).toContain(`${OIDC_COOKIE}=`);
	expect(sent).not.toContain(`${SESSION_COOKIE}=`);
	await expect(page.getByText(CONFIRMATION)).toBeVisible();
	await expect(page.getByRole('button', { name: 'Confirm' })).toBeVisible();
	await expect(page.getByRole('button', { name: 'Cancel' })).toBeVisible();
});

test('without preferred_username the confirmation says "your SSO account"', async ({
	page,
	request
}) => {
	await nextClaims(request, { preferred_username: null });
	await page.goto('/account');
	await reauth(page, PASSWORD);
	await expect(page.getByText('You are linking your SSO account to admin')).toBeVisible();
});

// Conformance (SSO spec § 3): a missing or stale auth_time on a link.
for (const [name, authTime] of [
	['an absent auth_time', () => null],
	['an auth_time 2 minutes before the re-auth', () => Math.floor((Date.now() - 120_000) / 1000)]
] as const) {
	test(`${name} is refused at the callback: no confirmation, cookie cleared, no identities call`, async ({
		page,
		context,
		request
	}) => {
		await page.goto('/account');
		await nextClaims(request, { auth_time: authTime() });
		await reauth(page, PASSWORD);
		await expect(page).toHaveURL(/\/auth\/callback\?/);
		await expect(page.getByText('Linking failed. Try again.')).toBeVisible();
		await expect(page.getByText('You are linking')).toHaveCount(0);
		await expect(page.getByRole('button', { name: 'Confirm' })).toHaveCount(0);
		expect(await cookie(context, OIDC_COOKIE)).toBeUndefined();
		expect(await identityCalls()).toEqual([]);
	});
}

test('Confirm links with the session and clears the transaction cookie', async ({
	page,
	context
}) => {
	await startLink(page);
	await page.getByRole('button', { name: 'Confirm' }).click();
	await expect(page).toHaveURL(/\/account\?link=linked$/);
	await expect(page.getByText('SSO account linked')).toBeVisible();
	expect(await identityCalls()).toEqual([
		{ session: STUB_SESSION_TOKEN, body: { iss: ISSUER_URL, sub: ISSUER_DEFAULT_SUB } }
	]);
	expect(await cookie(context, OIDC_COOKIE)).toBeUndefined();
});

test('Cancel calls nothing and clears the transaction cookie', async ({ page, context }) => {
	await startLink(page);
	await page.getByRole('button', { name: 'Cancel' }).click();
	await expect(page).toHaveURL(/\/account\?link=cancelled$/);
	await expect(page.getByText('Linking cancelled')).toBeVisible();
	expect(await identityCalls()).toEqual([]);
	expect(await cookie(context, OIDC_COOKIE)).toBeUndefined();
});

test('Confirm from another session is refused by the binding', async ({
	page,
	context,
	baseURL
}) => {
	await startLink(page);
	await context.addCookies([sessionCookie(STUB_SESSION_TOKEN_2, baseURL)]);
	await page.getByRole('button', { name: 'Confirm' }).click();
	await expect(page).toHaveURL(/\/account\?link=error$/);
	await expect(page.getByText('Linking failed. Try again.')).toBeVisible();
	expect(await identityCalls()).toEqual([]);
	expect(await cookie(context, OIDC_COOKIE)).toBeUndefined();
});

for (const [refusal, outcome, message] of [
	['already_linked', 'already_linked', 'That SSO account is already linked'],
	['reauth_required', 'reauth_required', 'Confirm your password again: the 5-minute window passed']
] as const) {
	test(`the API's ${refusal} is shown on /account`, async ({ page, context }) => {
		await setAuth({ mode: 'both', identities: refusal });
		await startLink(page);
		await page.getByRole('button', { name: 'Confirm' }).click();
		await expect(page).toHaveURL(new RegExp(`/account\\?link=${outcome}$`));
		await expect(page.getByText(message)).toBeVisible();
		expect(await identityCalls()).toHaveLength(1);
		expect(await cookie(context, OIDC_COOKIE)).toBeUndefined();
	});
}

test('POST /auth/link from a foreign Origin is 403 and calls nothing', async ({
	page,
	context
}) => {
	await startLink(page);
	const foreign = { origin: 'https://evil.example' };
	const form = await context.request.post('/auth/link', {
		form: { decision: 'confirm' },
		headers: foreign,
		maxRedirects: 0
	});
	expect(form.status()).toBe(403);
	// Not a form content type, so it passes SvelteKit's own check and reaches the route's.
	const other = await context.request.post('/auth/link', {
		data: { decision: 'confirm' },
		headers: foreign,
		maxRedirects: 0
	});
	expect(other.status()).toBe(403);
	expect(await identityCalls()).toEqual([]);

	await page.getByRole('button', { name: 'Confirm' }).click();
	await expect(page.getByText('SSO account linked')).toBeVisible();
	expect(await identityCalls()).toHaveLength(1);
});
