import { expect, test } from '@playwright/test';
import { STUB_URL } from './stub-api.js';

test('/healthz returns 200 {"status":"ok"}', async ({ request }) => {
	const res = await request.get('/healthz');
	expect(res.status()).toBe(200);
	expect(await res.json()).toEqual({ status: 'ok' });
});

test('the UI imported the admin before /healthz answered', async ({ request }) => {
	expect((await request.get('/healthz')).status()).toBe(200);
	const imports = await (await fetch(`${STUB_URL}/__imports`)).json();
	expect(imports).toEqual([{ username: 'admin' }]);
});
