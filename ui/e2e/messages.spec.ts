import AxeBuilder from '@axe-core/playwright';
import { expect, test, type Page } from '@playwright/test';
import { SESSION_COOKIE } from '../src/lib/server/constants.js';
import { signSession } from '../src/lib/server/session.js';
import {
	HOSTILE_ERROR,
	HOSTILE_RECIPIENT,
	MESSAGE_COUNT,
	receivedRequests,
	resetRequests,
	setScenario
} from './stub-api.js';

const SESSION_SECRET = 'fake-session-secret-000000000000000';
const PAGE_SIZE = 50;
const FAILED_ID = 'msg-0006';
const HELD_ID = 'msg-0035';
const HOSTILE_ID = 'msg-0005';

test.beforeEach(async ({ context, baseURL }) => {
	await setScenario('normal');
	await context.addCookies([
		{ name: SESSION_COOKIE, value: signSession(SESSION_SECRET, Date.now()), url: baseURL }
	]);
});

const cards = (page: Page) => page.getByTestId('message-card');
const rows = (page: Page) => page.getByTestId('message-row');

async function horizontalOverflow(page: Page): Promise<number> {
	return page.evaluate(
		() => document.documentElement.scrollWidth - document.documentElement.clientWidth
	);
}

test('at 390 px each message is a card with state, channel, recipient, app and time', async ({
	page
}) => {
	const response = await page.goto('/messages');
	expect(response?.status()).toBe(200);
	await expect(cards(page)).toHaveCount(PAGE_SIZE);
	await expect(rows(page).first()).toBeHidden();
	const first = cards(page).first();
	await expect(first).toContainText('delivered');
	await expect(first).toContainText('Email');
	await expect(first).toContainText('user0@example.com');
	await expect(first).toContainText('xstockstrat');
	await expect(first.locator('time')).toHaveAttribute('datetime', /^\d{4}-\d{2}-\d{2}T/);
	expect(await horizontalOverflow(page)).toBeLessThanOrEqual(0);
});

test('at 1280 px the list is a table that adds sender, attempts and error type', async ({
	page
}) => {
	await page.setViewportSize({ width: 1280, height: 800 });
	await page.goto('/messages');
	await expect(cards(page).first()).toBeHidden();
	const headers = page.getByRole('columnheader');
	await expect(headers).toHaveText([
		'State',
		'Channel',
		'Recipient',
		'App',
		'Sender',
		'Attempts',
		'Error type',
		'Created'
	]);
	await expect(rows(page)).toHaveCount(PAGE_SIZE);
	const bounced = rows(page).filter({ hasText: 'bounced' }).filter({ hasText: 'Email' }).first();
	await expect(bounced).toContainText('alerts@example.com');
	await expect(bounced.getByRole('cell').nth(5)).toHaveText('2');
	await expect(bounced.getByRole('cell').nth(6)).toHaveText('bounced');
	await bounced.getByRole('link').click();
	await expect(page).toHaveURL(/\/messages\/msg-\d{4}$/);
});

test('state badges always carry a text label', async ({ page }) => {
	await page.goto('/messages');
	for (const state of ['delivered', 'sent', 'bounced', 'failed', 'queued']) {
		await expect(page.getByTestId('state-badge').filter({ hasText: state }).first()).toBeVisible();
	}
});

test('the list honours the URL filters and sends them to the API', async ({ page }) => {
	await resetRequests();
	await page.goto('/messages?range=7d&channel=sms&app=xstockstrat&recipient=%2B5255');
	await expect(cards(page).first()).toBeVisible();
	for (const card of await cards(page).all()) await expect(card).toContainText('SMS');
	const list = (await receivedRequests()).filter((u) => u.pathname === '/admin/v1/messages');
	expect(list).toHaveLength(1);
	const q = list[0].searchParams;
	expect(q.get('channel')).toBe('sms');
	expect(q.get('app')).toBe('xstockstrat');
	expect(q.get('recipient')).toBe('+5255');
	expect(Date.parse(q.get('to') ?? '') - Date.parse(q.get('from') ?? '')).toBe(7 * 86_400_000);
	await expect(
		page.getByRole('navigation', { name: 'Range' }).getByRole('link', { name: '7d' })
	).toHaveAttribute('aria-current', 'page');
	await expect(page.getByRole('button', { name: 'Filters · 3' })).toBeVisible();
});

test('range chips keep the list path and drop the cursor', async ({ page }) => {
	await page.goto('/messages?cursor=50');
	await page
		.getByRole('navigation', { name: 'Range' })
		.getByRole('link', { name: '3d', exact: true })
		.click();
	await expect(page).toHaveURL(/\/messages\?range=3d$/);
});

test('the header Messages link does not carry the cursor', async ({ page }) => {
	await page.goto('/messages?range=3d&cursor=50');
	await expect(page.getByRole('link', { name: 'Messages' })).toHaveAttribute(
		'href',
		'/messages?range=3d'
	);
});

test('Older loads the next page via ?cursor and is hidden on the last page', async ({ page }) => {
	await page.goto('/messages');
	await expect(page.getByRole('link', { name: 'Newest' })).toHaveCount(0);
	await resetRequests();
	await page.getByRole('link', { name: 'Older' }).click();
	await expect(page).toHaveURL(/\/messages\?range=1h&cursor=50$/);
	await expect(cards(page)).toHaveCount(MESSAGE_COUNT - PAGE_SIZE);
	await expect(page.getByRole('link', { name: 'Older' })).toHaveCount(0);
	const list = (await receivedRequests()).filter((u) => u.pathname === '/admin/v1/messages');
	expect(list.map((u) => u.searchParams.get('cursor'))).toEqual(['50']);
	await page.getByRole('link', { name: 'Newest' }).click();
	await expect(page).toHaveURL(/\/messages\?range=1h$/);
	await expect(cards(page)).toHaveCount(PAGE_SIZE);
});

test('an invalid cursor shows the API error, not a 500', async ({ page }) => {
	const response = await page.goto('/messages?cursor=garbage');
	expect(response?.status()).toBe(200);
	await expect(page.getByText('The delivery API returned an error')).toBeVisible();
	await expect(page.getByRole('link', { name: 'Retry' })).toBeVisible();
});

test('empty scenario shows an empty state', async ({ page }) => {
	await setScenario('empty');
	await page.goto('/messages');
	await expect(page.getByText('No messages match these filters')).toBeVisible();
	await expect(page.getByRole('link', { name: 'Older' })).toHaveCount(0);
});

test('down scenario shows the unavailable message with a retry link', async ({ page }) => {
	await setScenario('down');
	const response = await page.goto('/messages');
	expect(response?.status()).toBe(200);
	await expect(page.getByText('The delivery API is not responding')).toBeVisible();
	await expect(page.getByRole('link', { name: 'Retry' })).toBeVisible();
});

test('tapping a card opens the detail page with metadata, timeline and attempts', async ({
	page
}) => {
	await page.goto('/messages');
	await cards(page).filter({ hasText: 'failed' }).first().click();
	await expect(page).toHaveURL(`/messages/${FAILED_ID}`);
	await expect(page.getByTestId('state-badge')).toContainText('failed');
	await expect(page.getByText('Email', { exact: true }).first()).toBeVisible();

	const meta = page.getByTestId('metadata');
	const field = (label: string) =>
		meta
			.locator('div', { has: page.getByText(label, { exact: true }) })
			.locator('dd')
			.first();
	await expect(field('ID')).toHaveText(FAILED_ID);
	await expect(field('Request ID')).toHaveText('req-0006');
	await expect(field('App')).toHaveText('homelab');
	await expect(field('Recipient')).toHaveText('user5@example.com');
	await expect(field('Sender')).toHaveText('alerts@example.com');
	await expect(field('Provider')).toHaveText('resend');
	await expect(field('Attempts')).toHaveText('2');
	await expect(field('Error type')).toHaveText('timeout');
	await expect(field('Body size')).toHaveText('1.2 kB');
	await expect(field('Created').locator('time')).toBeVisible();
	await expect(field('Expires').locator('time')).toBeVisible();
	await expect(meta.getByText('Next attempt', { exact: true })).toHaveCount(0);

	const timeline = page.getByTestId('timeline');
	await expect(timeline.getByRole('listitem')).toHaveText([/Enqueued/, /Failed/]);

	const attempts = page.getByTestId('attempt');
	await expect(attempts).toHaveCount(2);
	await expect(attempts.nth(0)).toContainText('1');
	await expect(attempts.nth(0)).toContainText('180 ms');
	await expect(attempts.nth(0)).toContainText('retryable');
	await expect(attempts.nth(0)).toContainText('timeout');
	await expect(attempts.nth(0)).toContainText('503');
	await expect(attempts.nth(0)).toContainText('upstream unavailable');
	await expect(attempts.nth(0)).toContainText('provider did not answer in time');
	await expect(attempts.nth(1)).toContainText('200 ms');
	await expect(attempts.nth(0).locator('time')).toBeVisible();
});

test('a delivered message has Enqueued, Sent and Delivered in the timeline', async ({ page }) => {
	await page.goto('/messages/msg-0001');
	await expect(page.getByTestId('timeline').getByRole('listitem')).toHaveText([
		/Enqueued/,
		/Sent/,
		/Delivered/
	]);
	await expect(page.getByTestId('attempt')).toHaveCount(1);
});

test('a held SMS says why and when it expires', async ({ page }) => {
	await page.goto(`/messages/${HELD_ID}`);
	const held = page.getByTestId('held');
	await expect(held).toContainText('Held: no SMS provider configured');
	await expect(held).toContainText('Expires');
	await expect(held.locator('time')).toBeVisible();
	await expect(page.getByText('No attempts yet')).toBeVisible();
});

test('the detail page has no body or subject section', async ({ page }) => {
	await page.goto(`/messages/${FAILED_ID}`);
	await expect(page.getByRole('heading', { name: /body|subject/i })).toHaveCount(0);
	await expect(page.getByText(/subject/i)).toHaveCount(0);
	await expect(page.getByText(/body/i)).toHaveCount(1);
	await expect(page.getByText('Body size', { exact: true })).toBeVisible();
});

for (const id of ['does-not-exist', 'bad%20id']) {
	test(`unknown id ${id} gives a 404 page with a link back to the list`, async ({ page }) => {
		const response = await page.goto(`/messages/${id}`);
		expect(response?.status()).toBe(404);
		await expect(page.getByText('Not found', { exact: true })).toBeVisible();
		await expect(page.getByRole('link', { name: 'Back to messages' })).toHaveAttribute(
			'href',
			'/messages'
		);
	});
}

test('detail with the API down is a 200 with a retry link', async ({ page }) => {
	await setScenario('down');
	const response = await page.goto(`/messages/${FAILED_ID}`);
	expect(response?.status()).toBe(200);
	await expect(page.getByText('The delivery API is not responding')).toBeVisible();
	await expect(page.getByRole('link', { name: 'Retry' })).toHaveAttribute(
		'href',
		`/messages/${FAILED_ID}`
	);
});

test('hostile data is text, fires no dialog and does not widen the list or detail page', async ({
	page
}) => {
	await setScenario('hostile');
	let dialogs = 0;
	page.on('dialog', async (d) => {
		dialogs++;
		await d.dismiss();
	});
	await page.goto('/messages');
	await expect(page.getByText(HOSTILE_RECIPIENT).locator('visible=true')).toBeVisible();
	expect(await horizontalOverflow(page)).toBeLessThanOrEqual(0);

	await page.goto(`/messages/${HOSTILE_ID}`);
	await expect(page.getByText(HOSTILE_RECIPIENT).first()).toBeVisible();
	await expect(page.getByText(HOSTILE_ERROR).first()).toBeVisible();
	expect(await horizontalOverflow(page)).toBeLessThanOrEqual(0);
	expect(dialogs).toBe(0);
});

test('the hostile list does not widen the page at 1280 px either', async ({ page }) => {
	await setScenario('hostile');
	await page.setViewportSize({ width: 1280, height: 800 });
	await page.goto('/messages');
	await expect(page.getByText(HOSTILE_RECIPIENT).locator('visible=true')).toBeVisible();
	expect(await horizontalOverflow(page)).toBeLessThanOrEqual(0);
});

for (const colorScheme of ['light', 'dark'] as const) {
	test.describe(`${colorScheme} scheme`, () => {
		test.use({ colorScheme });

		for (const [name, path] of [
			['list', '/messages'],
			['detail', `/messages/${FAILED_ID}`],
			['held detail', `/messages/${HELD_ID}`]
		]) {
			test(`${name}: no serious or critical axe violations`, async ({ page }) => {
				await page.goto(path);
				await expect(page.getByRole('main')).toBeVisible();
				const results = await new AxeBuilder({ page }).analyze();
				const bad = results.violations.filter(
					(v) => v.impact === 'serious' || v.impact === 'critical'
				);
				expect(bad.map((v) => `${v.id}: ${v.nodes.map((n) => n.target).join(', ')}`)).toEqual([]);
			});
		}

		test('list table at 1280 px: no serious or critical axe violations', async ({ page }) => {
			await page.setViewportSize({ width: 1280, height: 800 });
			await page.goto('/messages');
			await expect(rows(page).first()).toBeVisible();
			const results = await new AxeBuilder({ page }).analyze();
			const bad = results.violations.filter(
				(v) => v.impact === 'serious' || v.impact === 'critical'
			);
			expect(bad.map((v) => `${v.id}: ${v.nodes.map((n) => n.target).join(', ')}`)).toEqual([]);
		});
	});
}
