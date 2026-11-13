import { expect, test } from '@playwright/test';

test('a request whose origin differs from ORIGIN is rejected with 400', async ({ request }) => {
	const res = await request.get('/login', { headers: { 'x-forwarded-host': 'evil.example.test' } });
	expect(res.status()).toBe(400);
	expect(await res.text()).toBe('Bad origin');
});

test('/healthz ignores the origin check', async ({ request }) => {
	const res = await request.get('/healthz', {
		headers: { 'x-forwarded-host': 'evil.example.test' }
	});
	expect(res.status()).toBe(200);
});

test('dark and light colour schemes use different background tokens', async ({ browser }) => {
	const bg = async (colorScheme: 'light' | 'dark') => {
		const ctx = await browser.newContext({
			colorScheme,
			baseURL: 'http://127.0.0.1:4173',
			extraHTTPHeaders: { 'x-forwarded-proto': 'http' }
		});
		const page = await ctx.newPage();
		await page.goto('/login');
		const value = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);
		await ctx.close();
		return value;
	};
	const light = await bg('light');
	const dark = await bg('dark');
	expect(dark).not.toBe(light);
	expect(light).toBe('oklch(1 0 0)');
	expect(dark).toBe('oklch(0.145 0 0)');
});
