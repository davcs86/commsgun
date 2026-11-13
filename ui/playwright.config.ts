import { defineConfig } from '@playwright/test';
import { STUB_TOKEN, STUB_URL } from './e2e/stub-api.js';

const PORT = 4173;

const testEnv = {
	ADMIN_PASSWORD: 'fake-admin-password-0000',
	SESSION_SECRET: 'fake-session-secret-000000000000000',
	ADMIN_API_TOKEN: STUB_TOKEN,
	API_BASE_URL: STUB_URL,
	ORIGIN: `http://127.0.0.1:${PORT}`,
	PROTOCOL_HEADER: 'x-forwarded-proto',
	HOST_HEADER: 'x-forwarded-host',
	ADDRESS_HEADER: 'x-forwarded-for',
	XFF_DEPTH: '1',
	HOST: '127.0.0.1',
	PORT: String(PORT)
};

export default defineConfig({
	testDir: 'e2e',
	retries: 0,
	// The stub API's scenario is global state shared by every test.
	workers: 1,
	use: {
		viewport: { width: 390, height: 844 },
		baseURL: `http://127.0.0.1:${PORT}`,
		extraHTTPHeaders: { 'x-forwarded-proto': 'http' },
		launchOptions: process.env.PW_CHROMIUM_PATH
			? { executablePath: process.env.PW_CHROMIUM_PATH }
			: {}
	},
	webServer: [
		{
			command: 'node e2e/stub-api.ts',
			url: `${STUB_URL}/__requests`,
			reuseExistingServer: false
		},
		{
			command: 'pnpm build && node build',
			url: `http://127.0.0.1:${PORT}/healthz`,
			env: testEnv,
			timeout: 180_000,
			reuseExistingServer: false
		}
	]
});
