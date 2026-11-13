import AxeBuilder from '@axe-core/playwright';
import { expect, test, type Page } from '@playwright/test';
import { SESSION_COOKIE } from '../src/lib/server/constants.js';
import { signSession } from '../src/lib/server/session.js';
import { receivedRequests, resetRequests, setScenario } from './stub-api.js';

const SESSION_SECRET = 'fake-session-secret-000000000000000';
const DAY_MS = 86_400_000;

test.beforeEach(async ({ context, baseURL }) => {
	await setScenario('normal');
	await context.addCookies([
		{ name: SESSION_COOKIE, value: signSession(SESSION_SECRET, Date.now()), url: baseURL }
	]);
});

const tile = (page: Page, label: string) =>
	page.getByTestId('stat-tiles').locator('div', { has: page.getByText(label, { exact: true }) });

const chip = (page: Page, range: string) =>
	page.getByRole('navigation', { name: 'Range' }).getByRole('link', { name: range, exact: true });

async function statsRequests(): Promise<URL[]> {
	return (await receivedRequests()).filter((u) => u.pathname === '/admin/v1/stats');
}

async function horizontalOverflow(page: Page): Promise<number> {
	return page.evaluate(
		() => document.documentElement.scrollWidth - document.documentElement.clientWidth
	);
}

test('default view: 1h is the current range and the tiles show the totals', async ({ page }) => {
	const response = await page.goto('/');
	expect(response?.status()).toBe(200);
	await expect(page.getByRole('heading', { name: 'commsgun' })).toBeVisible();
	await expect(chip(page, '1h')).toHaveAttribute('aria-current', 'page');
	await expect(chip(page, '7d')).not.toHaveAttribute('aria-current');
	const expected: [string, string][] = [
		['Delivered', '41'],
		['Sent', '3'],
		['Queued', '0'],
		['Bounced', '1'],
		['Failed', '0'],
		['Retries', '4']
	];
	for (const [label, value] of expected) {
		await expect(tile(page, label).locator('dd')).toHaveText(value);
	}
	await expect(page.getByRole('heading', { name: 'Errors by type' })).toBeVisible();
	const errors = page.getByRole('region', { name: 'Errors by type' });
	await expect(errors.getByRole('listitem').filter({ hasText: 'timeout' })).toContainText('3');
	const apps = page.getByRole('region', { name: 'By app' });
	await expect(apps.getByRole('listitem').filter({ hasText: 'xstockstrat' })).toContainText('44');
});

test('tapping 7d asks the API for a 7-day window', async ({ page }) => {
	await page.goto('/');
	await resetRequests();
	await chip(page, '7d').click();
	await expect(page).toHaveURL(/\/\?range=7d$/);
	await expect(chip(page, '7d')).toHaveAttribute('aria-current', 'page');
	const stats = await statsRequests();
	expect(stats).toHaveLength(1);
	const from = Date.parse(stats[0].searchParams.get('from') ?? '');
	const to = Date.parse(stats[0].searchParams.get('to') ?? '');
	expect(to - from).toBe(7 * DAY_MS);
});

test('range chips work without JavaScript', async ({ browser, baseURL }) => {
	const context = await browser.newContext({
		javaScriptEnabled: false,
		baseURL,
		extraHTTPHeaders: { 'x-forwarded-proto': 'http' }
	});
	await context.addCookies([
		{ name: SESSION_COOKIE, value: signSession(SESSION_SECRET, Date.now()), url: baseURL }
	]);
	const page = await context.newPage();
	await page.goto('/?channel=email');
	await chip(page, '24h').click();
	await expect(page).toHaveURL(/\/\?range=24h&channel=email$/);
	await context.close();
});

test('filters sheet puts channel and app in the URL; Clear and Back work', async ({ page }) => {
	await page.goto('/');
	await page.getByRole('button', { name: 'Filters', exact: true }).click();
	const sheet = page.getByRole('dialog');
	await sheet.getByLabel('Message type').click();
	await page.getByRole('option', { name: 'Email' }).click();
	await sheet.getByLabel('Subscriber app').click();
	await page.getByRole('option', { name: 'xstockstrat' }).click();
	await resetRequests();
	await sheet.getByRole('button', { name: 'Apply' }).click();

	await expect(page).toHaveURL(/\/\?range=1h&channel=email&app=xstockstrat$/);
	await expect(page.getByRole('button', { name: 'Filters · 2' })).toBeVisible();
	const stats = await statsRequests();
	expect(stats.at(-1)?.searchParams.get('channel')).toBe('email');
	expect(stats.at(-1)?.searchParams.get('app')).toBe('xstockstrat');

	await page.getByRole('button', { name: 'Filters · 2' }).click();
	await page.getByRole('dialog').getByRole('link', { name: 'Clear' }).click();
	await expect(page).toHaveURL(/\/\?range=1h$/);
	await expect(page.getByRole('button', { name: 'Filters', exact: true })).toBeVisible();

	await page.goBack();
	await expect(page).toHaveURL(/channel=email&app=xstockstrat$/);
	await expect(page.getByRole('button', { name: 'Filters · 2' })).toBeVisible();
});

test.describe('in Mexico City', () => {
	test.use({ timezoneId: 'America/Mexico_City' });

	test('custom range converts local inputs to ISO UTC and unpresses every chip', async ({
		page
	}) => {
		await page.goto('/?channel=email');
		await page.getByText('Custom range', { exact: true }).click();
		await page.getByLabel('From').fill('2026-11-12T08:00');
		await page.getByLabel('To').fill('2026-11-12T20:30');
		await page.getByRole('button', { name: 'Show range' }).click();

		await expect(page).toHaveURL(/from=/);
		const params = new URL(page.url()).searchParams;
		expect(params.get('from')).toBe('2026-11-12T14:00:00.000Z');
		expect(params.get('to')).toBe('2026-11-13T02:30:00.000Z');
		expect(params.get('channel')).toBe('email');
		expect(params.has('range')).toBe(false);
		await expect(
			page.getByRole('navigation', { name: 'Range' }).locator('[aria-current]')
		).toHaveCount(0);
	});

	test('times are re-rendered in the browser zone after hydration', async ({ page }) => {
		await page.goto('/');
		const time = page.getByTestId('utc-time').first();
		await expect(time).toHaveAttribute('datetime', /Z$/);
		await expect(time).toHaveText(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} CST$/);
	});
});

test.describe('without JavaScript', () => {
	test.use({ javaScriptEnabled: false });

	test('times are rendered in UTC', async ({ page }) => {
		await page.goto('/');
		await expect(page.getByTestId('utc-time').first()).toHaveText(
			/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} UTC$/
		);
	});

	test('custom range submits the raw values, read as UTC', async ({ page }) => {
		await page.goto('/');
		await page.getByText('Custom range', { exact: true }).click();
		await page.getByLabel('From').fill('2026-11-12T08:00');
		await page.getByLabel('To').fill('2026-11-12T20:30');
		await resetRequests();
		await page.getByRole('button', { name: 'Show range' }).click();
		await expect(page).toHaveURL(/from=2026-11-12T08%3A00/);
		const stats = await statsRequests();
		expect(stats.at(-1)?.searchParams.get('from')).toBe('2026-11-12T08:00:00.000Z');
		expect(stats.at(-1)?.searchParams.get('to')).toBe('2026-11-12T20:30:00.000Z');
	});
});

test('provider usage shows the day quota, reset time and held messages', async ({ page }) => {
	await page.goto('/');
	const usage = page.getByRole('region', { name: 'Provider usage' });
	const email = usage.getByRole('listitem').filter({ hasText: 'resend' });
	await expect(email).toContainText('47 / 100');
	await expect(email).toContainText('3 / 10 this minute');
	await expect(email.getByText(/^resets/)).toBeVisible();
	await expect(email.getByTestId('utc-time')).toHaveAttribute('datetime', /T00:00:00.000Z$/);
	const bar = email.getByRole('progressbar');
	await expect(bar).toHaveAttribute('aria-valuenow', '47');
	await expect(bar).toHaveAttribute('aria-valuemin', '0');
	await expect(bar).toHaveAttribute('aria-valuemax', '100');
	await expect(usage.getByText('No provider · 2 held')).toBeVisible();
});

test('a paused provider shows the reason and when it resumes', async ({ page }) => {
	await setScenario('hostile');
	await page.goto('/');
	const email = page
		.getByRole('region', { name: 'Provider usage' })
		.getByRole('listitem')
		.filter({ hasText: 'resend' });
	await expect(email).toContainText('Daily quota reached');
	await expect(email.getByText(/^resumes/)).toBeVisible();
	await expect(email).toContainText('100 / 100');
});

test('hostile data is shown as text and does not widen the page', async ({ page }) => {
	await setScenario('hostile');
	let dialogs = 0;
	page.on('dialog', async (d) => {
		dialogs++;
		await d.dismiss();
	});
	await page.goto('/');
	await expect(page.getByText('<script>alert(1)</script>@x.io')).toBeVisible();
	expect(dialogs).toBe(0);
	expect(await horizontalOverflow(page)).toBeLessThanOrEqual(0);
});

test('auto-refresh reloads stats every 30 s only while the tab is visible', async ({ page }) => {
	await page.clock.install();
	await page.goto('/');
	await expect(tile(page, 'Delivered').locator('dd')).toHaveText('41');
	await resetRequests();

	await page.clock.runFor(30_000);
	await expect.poll(async () => (await statsRequests()).length).toBe(1);

	await resetRequests();
	await page.evaluate(() => {
		Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'hidden' });
		Object.defineProperty(document, 'hidden', { configurable: true, get: () => true });
	});
	await page.clock.runFor(30_000);
	await page.evaluate(() => {
		Object.defineProperty(document, 'visibilityState', {
			configurable: true,
			get: () => 'visible'
		});
		Object.defineProperty(document, 'hidden', { configurable: true, get: () => false });
	});
	await page.clock.runFor(30_000);
	await expect.poll(async () => (await statsRequests()).length).toBe(1);
	await expect(page.getByRole('heading', { name: 'commsgun' })).toBeVisible();
	expect(await statsRequests()).toHaveLength(1);
});

test('the refresh button reloads the data', async ({ page }) => {
	await page.goto('/');
	await expect(tile(page, 'Delivered').locator('dd')).toHaveText('41');
	await resetRequests();
	await page.getByRole('button', { name: 'Refresh' }).click();
	await expect.poll(async () => (await statsRequests()).length).toBe(1);
});

test('the Messages link carries the current filters', async ({ page }) => {
	await page.goto('/?range=7d&channel=email');
	await expect(page.getByRole('link', { name: 'Messages' })).toHaveAttribute(
		'href',
		'/messages?range=7d&channel=email'
	);
});

for (const width of [390, 1280]) {
	test(`no horizontal scroll at ${width} px`, async ({ page }) => {
		await page.setViewportSize({ width, height: width === 390 ? 844 : 800 });
		await page.goto('/');
		await expect(page.getByTestId('outcome-chart').locator('svg').first()).toBeVisible();
		expect(await horizontalOverflow(page)).toBeLessThanOrEqual(0);
	});
}

test.describe('light scheme', () => {
	test.use({ colorScheme: 'light' });

	test('no serious or critical axe violations', async ({ page }) => {
		await page.goto('/');
		await expect(page.getByTestId('outcome-chart').locator('svg').first()).toBeVisible();
		const results = await new AxeBuilder({ page }).analyze();
		const bad = results.violations.filter((v) => v.impact === 'serious' || v.impact === 'critical');
		expect(bad.map((v) => `${v.id}: ${v.nodes.map((n) => n.target).join(', ')}`)).toEqual([]);
	});
});

test('empty scenario: zero tiles, an empty-chart message and no NaN', async ({ page }) => {
	await setScenario('empty');
	await page.goto('/');
	for (const label of ['Delivered', 'Sent', 'Queued', 'Bounced', 'Failed', 'Retries']) {
		await expect(tile(page, label).locator('dd')).toHaveText('0');
	}
	await expect(page.getByText('No messages in this range')).toBeVisible();
	expect(await page.locator('body').innerText()).not.toContain('NaN');
});

test('down scenario: each section says the API is not responding; healthz stays 200', async ({
	page,
	request
}) => {
	await setScenario('down');
	const response = await page.goto('/');
	expect(response?.status()).toBe(200);
	const alerts = page.getByRole('alert').filter({ hasText: 'The delivery API is not responding' });
	await expect(alerts).toHaveCount(2);
	await expect(alerts.first().getByRole('link', { name: 'Retry' })).toBeVisible();
	expect((await request.get('/healthz')).status()).toBe(200);
});
