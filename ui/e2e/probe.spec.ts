import AxeBuilder from '@axe-core/playwright';
import { expect, test, type Page } from '@playwright/test';
import { SESSION_COOKIE } from '../src/lib/server/constants.js';
import { deriveSessionKey, signSession } from '../src/lib/server/session.js';
import {
	PROBE_EMAIL_ID,
	PROBE_LIMITED,
	PROBE_SENDER,
	receivedProbes,
	receivedRequests,
	resetRequests,
	setProbeMode,
	setScenario,
	TEST_ADMIN_PASSWORD,
	TEST_SESSION_SECRET
} from './stub-api.js';

const SESSION_KEY = deriveSessionKey(TEST_SESSION_SECRET, TEST_ADMIN_PASSWORD);
const KEY = '3f2b8c1e-5d4a-4e6f-9a1b-0c2d3e4f5a6b';
const HELD_NOTICE =
	'No SMS provider is configured. The message will be accepted and held, not delivered.';

const CLIENT_ADDRESS = '203.0.113.9';
// The server reads the client address from the proxy header and refuses a request without it.
test.use({
	extraHTTPHeaders: { 'x-forwarded-proto': 'http', 'x-forwarded-for': CLIENT_ADDRESS }
});

test.beforeEach(async ({ context, baseURL }) => {
	await setScenario('normal');
	await context.addCookies([
		{ name: SESSION_COOKIE, value: signSession(SESSION_KEY, Date.now()), url: baseURL }
	]);
});

const dialog = (page: Page) => page.getByRole('dialog', { name: 'Send a hello-world probe' });
const sendButton = (page: Page) => dialog(page).getByRole('button', { name: 'Send', exact: true });
const destination = (page: Page) => dialog(page).getByRole('textbox');

async function openProbe(page: Page, path = '/') {
	await page.goto(path);
	await page.getByRole('button', { name: 'Send probe' }).click();
	await expect(dialog(page)).toBeVisible();
}

async function chooseSms(page: Page) {
	await dialog(page).getByRole('combobox', { name: 'Message type' }).click();
	await page.getByRole('option', { name: 'SMS' }).click();
	// The list animates out; later checks must not see it.
	await expect(page.getByRole('listbox')).toHaveCount(0);
}

async function probeOptionRequests(): Promise<number> {
	return (await receivedRequests()).filter((u) => u.pathname === '/admin/v1/probes').length;
}

async function horizontalOverflow(page: Page): Promise<number> {
	return page.evaluate(
		() => document.documentElement.scrollWidth - document.documentElement.clientWidth
	);
}

test('the header button opens the probe dialog on every signed-in page', async ({ page }) => {
	for (const path of ['/', '/messages', '/messages/msg-0001']) {
		await openProbe(page, path);
		await expect(dialog(page).getByRole('combobox', { name: 'Message type' })).toHaveText('Email');
		await expect(dialog(page).getByRole('combobox', { name: 'Provider' })).toHaveText('resend');
		await expect(dialog(page).getByLabel('Destination email')).toHaveValue('');
		await expect(dialog(page).getByText(`From: ${PROBE_SENDER}`)).toBeVisible();
		await expect(sendButton(page)).toBeDisabled();
	}
});

test('the options are fetched when the dialog opens, not on page load', async ({ page }) => {
	await resetRequests();
	await page.goto('/');
	await expect(page.getByRole('heading', { name: 'commsgun' })).toBeVisible();
	expect(await probeOptionRequests()).toBe(0);
	await page.getByRole('button', { name: 'Send probe' }).click();
	await expect(destination(page)).toBeVisible();
	expect(await probeOptionRequests()).toBe(1);
});

test('the open dialog has no accessibility violations and no sideways scroll at 390 px', async ({
	page
}) => {
	await openProbe(page);
	await expect(destination(page)).toBeVisible();
	expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
	expect(await horizontalOverflow(page)).toBeLessThanOrEqual(0);
	const box = await dialog(page).boundingBox();
	expect(box && box.x >= 0 && box.x + box.width <= 390).toBe(true);
	await chooseSms(page);
	await destination(page).fill('not a number');
	await sendButton(page).click();
	await expect(dialog(page).getByText('Enter a number in international format')).toBeVisible();
	expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
});

test('an email probe is queued once and links to its message', async ({ page }) => {
	await openProbe(page);
	await destination(page).fill('me@example.com');
	await sendButton(page).click();
	await expect(dialog(page).getByRole('status')).toContainText('Probe queued.');
	const probes = await receivedProbes();
	expect(probes).toHaveLength(1);
	expect(probes[0].body).toEqual({ channel: 'email', to: 'me@example.com', provider: 'resend' });
	expect(probes[0].key).toMatch(/^[0-9a-f-]{36}$/);
	expect(probes[0].forwardedFor).toBe(CLIENT_ADDRESS);
	await dialog(page).getByRole('link', { name: 'View message' }).click();
	await expect(page).toHaveURL(new RegExp(`/messages/${PROBE_EMAIL_ID}$`));
	await expect(dialog(page)).toBeHidden();
});

test('sending again after a success is a new probe with a new key', async ({ page }) => {
	await openProbe(page);
	await destination(page).fill('me@example.com');
	await sendButton(page).click();
	await expect(dialog(page).getByRole('status')).toContainText('Probe queued.');
	await sendButton(page).click();
	await expect.poll(async () => (await receivedProbes()).length).toBe(2);
	const [a, b] = await receivedProbes();
	expect(a.key).not.toBe(b.key);
});

test('a double click sends one probe', async ({ page }) => {
	await openProbe(page);
	await destination(page).fill('me@example.com');
	await sendButton(page).dblclick();
	await expect(dialog(page).getByRole('status')).toContainText('Probe queued.');
	expect(await receivedProbes()).toHaveLength(1);
});

test('SMS shows that no provider is configured and the probe is held', async ({ page }) => {
	await openProbe(page);
	await destination(page).fill('me@example.com');
	await chooseSms(page);
	await expect(dialog(page).getByRole('combobox', { name: 'Provider' })).toHaveText(
		'None configured'
	);
	await expect(dialog(page).getByRole('combobox', { name: 'Provider' })).toBeDisabled();
	await expect(dialog(page).getByText(HELD_NOTICE)).toBeVisible();
	await expect(dialog(page).getByLabel('Destination phone number')).toHaveValue('');
	await expect(dialog(page).getByText('From:')).toHaveCount(0);
	await destination(page).fill('+15551234567');
	await sendButton(page).click();
	await expect(dialog(page).getByRole('status')).toContainText('Probe accepted and held.');
	expect((await receivedProbes())[0].body).toEqual({
		channel: 'sms',
		to: '+15551234567',
		provider: null
	});
});

test('a rate-limited probe says which limit and when to retry', async ({ page }) => {
	await openProbe(page);
	await destination(page).fill(PROBE_LIMITED);
	await sendButton(page).click();
	await expect(dialog(page).getByRole('status')).toContainText(
		'The ops per-minute limit is reached. Try again in 42 s.'
	);
	expect(await receivedProbes()).toHaveLength(0);
});

test('a hostile or overlong destination is refused on the field and never sent', async ({
	page
}) => {
	let dialogs = 0;
	page.on('dialog', (d) => {
		dialogs++;
		void d.dismiss();
	});
	await openProbe(page);
	await resetRequests();
	for (const value of [
		'<script>alert(1)</script>',
		'Me <me@example.com>',
		`${'a'.repeat(400)}@example.com`
	]) {
		await destination(page).fill(value);
		await sendButton(page).click();
		await expect(dialog(page).getByText('Enter one valid email address.')).toBeVisible();
		await expect(destination(page)).toHaveAttribute('aria-invalid', 'true');
		expect(await horizontalOverflow(page)).toBeLessThanOrEqual(0);
	}
	await destination(page).fill('me@example.com');
	await expect(dialog(page).getByText('Enter one valid email address.')).toHaveCount(0);
	expect(dialogs).toBe(0);
	expect(await probeOptionRequests()).toBe(0);
	expect(await receivedProbes()).toHaveLength(0);
});

test('options that changed since the dialog opened are reloaded and nothing is sent', async ({
	page
}) => {
	await openProbe(page);
	await destination(page).fill('me@example.com');
	await setProbeMode('mismatch');
	await resetRequests();
	await sendButton(page).click();
	await expect(dialog(page).getByRole('status')).toContainText('The configuration changed.');
	await expect(dialog(page).getByRole('combobox', { name: 'Provider' })).toHaveText('other');
	// One refused POST, then one GET for the fresh options.
	expect(await probeOptionRequests()).toBe(2);
	expect(await receivedProbes()).toHaveLength(0);
});

test('without app ops the dialog explains and offers no form', async ({ page }) => {
	await setProbeMode('missing');
	await openProbe(page);
	await expect(
		dialog(page).getByText('App ops is not configured, so probes cannot be sent.')
	).toBeVisible();
	await expect(dialog(page).getByRole('button', { name: 'Send', exact: true })).toHaveCount(0);
});

test('an app without a default sender is reported', async ({ page }) => {
	await setProbeMode('nosender');
	await openProbe(page);
	await destination(page).fill('me@example.com');
	await sendButton(page).click();
	await expect(dialog(page).getByRole('status')).toContainText(
		'App ops has no default sender configured.'
	);
});

test('API down: the dialog says so, Retry recovers, and a failed send can be retried', async ({
	page
}) => {
	await page.goto('/');
	await setScenario('down');
	await page.getByRole('button', { name: 'Send probe' }).click();
	await expect(dialog(page).getByText('The API is unavailable.')).toBeVisible();
	await setScenario('normal');
	await dialog(page).getByRole('button', { name: 'Retry' }).click();
	await destination(page).fill('me@example.com');
	await setScenario('down');
	await sendButton(page).click();
	await expect(dialog(page).getByRole('status')).toContainText('The API is unavailable.');
	await setScenario('normal');
	await sendButton(page).click();
	await expect(dialog(page).getByRole('status')).toContainText('Probe queued.');
	expect(await receivedProbes()).toHaveLength(1);
});

test('POST /probe refuses another origin, a bad body and a signed-out caller', async ({
	page,
	playwright,
	baseURL
}) => {
	const data = { channel: 'email', to: 'me@example.com', provider: 'resend', key: KEY };
	const origin = new URL(baseURL ?? '').origin;
	for (const headers of [{ origin: 'https://evil.example' }, {}] as Record<string, string>[]) {
		const res = await page.request.post('/probe', { data, headers });
		expect(res.status()).toBe(403);
	}
	const bad = await page.request.post('/probe', {
		data: { ...data, channel: 'fax' },
		headers: { origin }
	});
	expect(bad.status()).toBe(400);
	const invalid = await page.request.post('/probe', {
		data: { ...data, to: 'nope' },
		headers: { origin }
	});
	expect(await invalid.json()).toEqual({ kind: 'invalid', field: 'to' });
	expect(await receivedProbes()).toHaveLength(0);

	const ok = await page.request.post('/probe', { data, headers: { origin } });
	expect(await ok.json()).toEqual({ kind: 'queued', messageId: PROBE_EMAIL_ID, held: false });
	const again = await page.request.post('/probe', { data, headers: { origin } });
	expect(await again.json()).toMatchObject({ kind: 'queued' });
	expect(await receivedProbes()).toHaveLength(1);

	const anon = await playwright.request.newContext({
		baseURL,
		extraHTTPHeaders: { 'x-forwarded-proto': 'http', 'x-forwarded-for': CLIENT_ADDRESS }
	});
	for (const res of [
		await anon.post('/probe', { data, headers: { origin }, maxRedirects: 0 }),
		await anon.get('/probe', { maxRedirects: 0 })
	]) {
		expect(res.status()).toBe(303);
		expect(res.headers().location).toContain('/login');
	}
	await anon.dispose();
	expect(await receivedProbes()).toHaveLength(1);
});
