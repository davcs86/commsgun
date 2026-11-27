import { expect, test, type APIResponse, type Page } from '@playwright/test';
import { SESSION_COOKIE } from '../src/lib/server/constants.js';
import {
	issuedSessions,
	sessionCalls,
	setAuth,
	setScenario,
	STUB_SESSION_TOKEN,
	TEST_ADMIN_PASSWORD as PASSWORD
} from './stub-api.js';

// Only these specs drive the form (UI-05). Each posts from its own x-forwarded-for address, so each has
// its own limiter budget.
test.beforeEach(async ({ context }, testInfo) => {
	await setScenario('normal');
	await context.setExtraHTTPHeaders({
		'x-forwarded-proto': 'http',
		'x-forwarded-for': `test-${testInfo.testId}`
	});
});

async function submit(page: Page, username: string, password: string) {
	await page.getByLabel('Username').fill(username);
	await page.getByLabel('Password').fill(password);
	const response = page.waitForResponse((r) => r.request().method() === 'POST');
	await page.getByRole('button', { name: 'Sign in' }).click();
	return response;
}

test('unauthenticated GET / redirects to /login with next', async ({ page }) => {
	await page.goto('/');
	await expect(page).toHaveURL(/\/login\?next=%2F$/);
});

test('GET /healthz works without a cookie', async ({ request }) => {
	const response = await request.get('/healthz');
	expect(response.status()).toBe(200);
});

test('GET /logout is 405', async ({ request }) => {
	const response = await request.get('/logout', { maxRedirects: 0 });
	expect(response.status()).toBe(405);
});

test('login page fits a phone without horizontal scroll', async ({ page }) => {
	await page.goto('/login');
	const overflow = await page.evaluate(
		() => document.documentElement.scrollWidth - document.documentElement.clientWidth
	);
	expect(overflow).toBe(0);
});

test('wrong password stays on /login and sets no cookie', async ({ page, context }) => {
	await page.goto('/login');
	const response = await submit(page, 'admin', 'not-the-password');
	expect(response.status()).toBe(400);
	await expect(page).toHaveURL(/\/login/);
	await expect(page.getByText('Incorrect username or password')).toBeVisible();
	expect((await context.cookies()).find((c) => c.name === SESSION_COOKIE)).toBeUndefined();
});

test('another username with the right password gives the same message', async ({ page }) => {
	await page.goto('/login');
	const response = await submit(page, 'root', PASSWORD);
	expect(response.status()).toBe(400);
	await expect(page.getByText('Incorrect username or password')).toBeVisible();
});

test('right credentials land on next and set a hardened cookie', async ({ page, context }) => {
	await page.goto('/login?next=%2Fhealthz');
	await submit(page, 'admin', PASSWORD);
	await expect(page).toHaveURL(/\/healthz$/);
	const cookie = (await context.cookies()).find((c) => c.name === SESSION_COOKIE);
	expect(cookie).toMatchObject({
		value: STUB_SESSION_TOKEN,
		httpOnly: true,
		secure: true,
		sameSite: 'Strict',
		path: '/'
	});
	const [expiresAt] = await issuedSessions();
	expect(Math.abs((cookie?.expires ?? 0) * 1000 - Date.parse(expiresAt))).toBeLessThan(60_000);
});

test('an API that refuses the bearer is an API error, not a credentials message', async ({
	page,
	context
}) => {
	await setAuth({ mode: 'local', sessionsLocal: 'unauthorized' });
	await page.goto('/login');
	await submit(page, 'admin', PASSWORD);
	await expect(page.getByText('The delivery API returned an error')).toBeVisible();
	await expect(page.getByText('Incorrect username or password')).toHaveCount(0);
	expect((await context.cookies()).find((c) => c.name === SESSION_COOKIE)).toBeUndefined();
});

test('a mode that refuses password sessions says password sign-in is off', async ({ page }) => {
	await setAuth({ mode: 'local', sessionsLocal: 'mode' });
	await page.goto('/login');
	await submit(page, 'admin', PASSWORD);
	await expect(page.getByText('Password sign-in is turned off')).toBeVisible();
});

test('an external next is ignored', async ({ page }) => {
	await page.goto('/login?next=https%3A%2F%2Fevil.example');
	await submit(page, 'admin', PASSWORD);
	await expect(page).toHaveURL(/127\.0\.0\.1:\d+\/$/);
	await expect(page.getByRole('heading', { name: 'commsgun' })).toBeVisible();
});

test('logout clears the cookie and / redirects to /login', async ({ page, context }) => {
	await page.goto('/login');
	await submit(page, 'admin', PASSWORD);
	await page.getByRole('button', { name: 'Sign out' }).click();
	await expect(page).toHaveURL(/\/login$/);
	expect((await context.cookies()).find((c) => c.name === SESSION_COOKIE)).toBeUndefined();
	expect(await sessionCalls()).toContainEqual({ method: 'DELETE', session: STUB_SESSION_TOKEN });
	await page.goto('/');
	await expect(page).toHaveURL(/\/login\?next=%2F$/);
});

test('logout clears the cookie even when the API does not answer', async ({ page, context }) => {
	await page.goto('/login');
	await submit(page, 'admin', PASSWORD);
	await expect(page.getByRole('button', { name: 'Sign out' })).toBeVisible();
	await setScenario('down');
	await page.getByRole('button', { name: 'Sign out' }).click();
	await expect(page).toHaveURL(/\/login$/);
	expect((await context.cookies()).find((c) => c.name === SESSION_COOKIE)).toBeUndefined();
});

test('a cookie the API refuses redirects to /login and is deleted', async ({
	page,
	context,
	baseURL
}) => {
	const refused = 'fake-session-token-refused-0000000000000000';
	expect(refused).toHaveLength(43);
	await context.addCookies([{ name: SESSION_COOKIE, value: refused, url: baseURL }]);
	const response = await page.goto('/messages');
	expect(response?.url()).toMatch(/\/login\?next=%2Fmessages$/);
	expect((await context.cookies()).find((c) => c.name === SESSION_COOKIE)).toBeUndefined();
	expect(await sessionCalls()).toEqual([{ method: 'GET', session: refused }]);
});

const deletesCookie = (response: APIResponse) =>
	response
		.headersArray()
		.some(
			(h) =>
				h.name.toLowerCase() === 'set-cookie' &&
				h.value.startsWith(`${SESSION_COOKIE}=;`) &&
				/Max-Age=0/i.test(h.value)
		);

// Review Focus 4: a malformed cookie is signed out without asking the API.
for (const [name, value] of [
	['empty', ''],
	['42 characters', 'a'.repeat(42)],
	['44 characters', 'a'.repeat(44)],
	['10 KB', 'a'.repeat(10240)],
	['non-base64url', '!'.repeat(43)]
]) {
	test(`a junk cookie (${name}) is signed out without an API call and deleted`, async ({
		request
	}) => {
		const response = await request.get('/', {
			headers: { cookie: `${SESSION_COOKIE}=${value}` },
			maxRedirects: 0
		});
		expect(response.status()).toBe(303);
		expect(response.headers()['location']).toBe('/login?next=%2F');
		expect(deletesCookie(response)).toBe(true);
		expect(await sessionCalls()).toEqual([]);
	});
}

// Review Focus 1: an unreachable API is a 503, never a sign-out.
test('with the API down a valid cookie gets a 503, keeps the cookie and is not redirected', async ({
	page,
	context,
	baseURL,
	request
}) => {
	await context.addCookies([{ name: SESSION_COOKIE, value: STUB_SESSION_TOKEN, url: baseURL }]);
	await setScenario('down');
	for (const path of ['/', '/messages']) {
		const response = await page.goto(path);
		expect(response?.status()).toBe(503);
		expect(response?.url()).toBe(`${baseURL}${path}`);
		await expect(page.getByText('The delivery API is not responding')).toBeVisible();
	}
	expect((await context.cookies()).find((c) => c.name === SESSION_COOKIE)?.value).toBe(
		STUB_SESSION_TOKEN
	);
	const direct = await request.get('/', {
		headers: { cookie: `${SESSION_COOKIE}=${STUB_SESSION_TOKEN}` },
		maxRedirects: 0
	});
	expect(direct.status()).toBe(503);
	expect(deletesCookie(direct)).toBe(false);
	expect((await request.get('/healthz')).status()).toBe(200);
});

test('the 6th attempt within a minute is rate limited', async ({ page }) => {
	await page.goto('/login');
	for (let i = 0; i < 5; i++) {
		expect((await submit(page, 'admin', 'not-the-password')).status()).toBe(400);
	}
	const response = await submit(page, 'admin', PASSWORD);
	expect(response.status()).toBe(429);
	await expect(page.getByText('Too many attempts. Try again in a minute.')).toBeVisible();
});

const guarded: [string, string][] = [
	['GET', '/loginx'],
	['GET', '/healthz/x'],
	['GET', '/login/'],
	['GET', '/__data.json'],
	['GET', '/messages/__data.json'],
	['POST', '/']
];
for (const [method, path] of guarded) {
	test(`unauthenticated ${method} ${path} is never served`, async ({ request }) => {
		const response = await request.fetch(path, { method, maxRedirects: 0 });
		const status = response.status();
		if (status >= 300 && status < 400) {
			expect(response.headers()['location']).toMatch(/^(\/|\.\.\/)login/);
		} else if (status === 200) {
			// SvelteKit data requests answer 200 with a redirect envelope, never page data.
			const body = await response.json();
			expect(body).toMatchObject({ type: 'redirect', location: expect.stringMatching(/^\/login/) });
		} else {
			expect(status).toBeGreaterThanOrEqual(400);
			expect(status).toBeLessThan(500);
		}
	});
}
