import { expect, test, type Page } from '@playwright/test';

const PASSWORD = 'fake-admin-password-0000';
const SESSION_COOKIE = 'commsgun_session';

// Each test posts from its own x-forwarded-for address, so each has its own limiter budget.
test.beforeEach(async ({ context }, testInfo) => {
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
	expect(cookie).toMatchObject({ httpOnly: true, secure: true, sameSite: 'Strict' });
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
	await page.goto('/');
	await expect(page).toHaveURL(/\/login\?next=%2F$/);
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
