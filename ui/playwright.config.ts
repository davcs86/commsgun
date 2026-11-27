import { defineConfig } from '@playwright/test';
import { STUB_TOKEN, STUB_URL, TEST_ADMIN_PASSWORD, TEST_SESSION_SECRET } from './e2e/stub-api.js';
import {
	ISSUER_CA_FILE,
	ISSUER_CLIENT_ID,
	ISSUER_CLIENT_SECRET,
	ISSUER_URL
} from './e2e/stub-issuer.js';

const PORT = 4173;

const testEnv = {
	ADMIN_PASSWORD: TEST_ADMIN_PASSWORD,
	SESSION_SECRET: TEST_SESSION_SECRET,
	ADMIN_API_TOKEN: STUB_TOKEN,
	API_BASE_URL: STUB_URL,
	ORIGIN: `http://127.0.0.1:${PORT}`,
	PROTOCOL_HEADER: 'x-forwarded-proto',
	HOST_HEADER: 'x-forwarded-host',
	ADDRESS_HEADER: 'x-forwarded-for',
	XFF_DEPTH: '1',
	HOST: '127.0.0.1',
	PORT: String(PORT),
	OIDC_ISSUER: ISSUER_URL,
	OIDC_CLIENT_ID: ISSUER_CLIENT_ID,
	OIDC_CLIENT_SECRET: ISSUER_CLIENT_SECRET,
	// Read once at start: the only way the UI's fetch trusts the stub issuer's test CA.
	NODE_EXTRA_CA_CERTS: ISSUER_CA_FILE
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
		// The stub issuer's certificate comes from a CA made at its start.
		ignoreHTTPSErrors: true,
		launchOptions: process.env.PW_CHROMIUM_PATH
			? { executablePath: process.env.PW_CHROMIUM_PATH }
			: {}
	},
	// In order, each awaited: the issuer writes its CA before the UI process starts.
	webServer: [
		{
			command: 'node e2e/stub-issuer.ts',
			url: `${ISSUER_URL}/.well-known/openid-configuration`,
			ignoreHTTPSErrors: true,
			reuseExistingServer: false
		},
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
