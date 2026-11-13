import { defineConfig } from '@playwright/test';

const PORT = 4173;

const testEnv = {
	ADMIN_PASSWORD: 'fake-admin-password-0000',
	SESSION_SECRET: 'fake-session-secret-000000000000000',
	ADMIN_API_TOKEN: 'fake-admin-api-token-00000000000000',
	API_BASE_URL: 'http://127.0.0.1:9',
	ORIGIN: `http://127.0.0.1:${PORT}`,
	HOST: '127.0.0.1',
	PORT: String(PORT)
};

export default defineConfig({
	testDir: 'e2e',
	use: {
		baseURL: `http://127.0.0.1:${PORT}`,
		launchOptions: process.env.PW_CHROMIUM_PATH
			? { executablePath: process.env.PW_CHROMIUM_PATH }
			: {}
	},
	webServer: {
		command: 'pnpm build && node build',
		url: `http://127.0.0.1:${PORT}/healthz`,
		env: testEnv,
		reuseExistingServer: false
	}
});
