// See https://svelte.dev/docs/kit/types#app.d.ts
// for information about these interfaces
import type { User } from './lib/types.js';

declare global {
	namespace App {
		// interface Error {}
		interface Locals {
			authed: boolean;
			session: { token: string; kind: 'local' | 'oidc'; expiresAt: string; user: User } | null;
		}
		// interface PageData {}
		// interface PageState {}
		// interface Platform {}
	}
}

export {};
