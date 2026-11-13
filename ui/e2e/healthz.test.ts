import { expect, test } from '@playwright/test';

test('/healthz returns 200 {"status":"ok"}', async ({ request }) => {
	const res = await request.get('/healthz');
	expect(res.status()).toBe(200);
	expect(await res.json()).toEqual({ status: 'ok' });
});
