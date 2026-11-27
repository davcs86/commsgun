import { LOGIN_LIMITER_MAX_KEYS, LOGIN_MAX_ATTEMPTS, LOGIN_WINDOW_MS } from './constants';

// Only a plain same-origin path is honoured; login, logout and SSO targets and anything odd map to "/".
export function safeNext(next: string | null): string {
	if (!next || !next.startsWith('/') || next.startsWith('//')) return '/';
	// eslint-disable-next-line no-control-regex -- control characters are exactly what is rejected
	if (/[\x00-\x20\x7f\\]/.test(next)) return '/';
	try {
		const url = new URL(next, 'http://x');
		if (url.origin !== 'http://x') return '/';
		const { pathname } = url;
		if (pathname.startsWith('//')) return '/';
		if (/^\/(login|logout|login\/sso)\/?$/.test(pathname) || /^\/auth\//.test(pathname)) return '/';
		const result = pathname + url.search;
		const again = new URL(result, 'http://x');
		if (again.origin !== 'http://x' || again.pathname !== pathname) return '/';
		return result;
	} catch {
		return '/';
	}
}

export class LoginLimiter {
	private readonly attempts = new Map<string, number[]>();

	allow(key: string, now: number): boolean {
		if (this.attempts.size >= LOGIN_LIMITER_MAX_KEYS) this.sweep(now);
		const recent = (this.attempts.get(key) ?? []).filter((t) => now - t < LOGIN_WINDOW_MS);
		const allowed = recent.length < LOGIN_MAX_ATTEMPTS;
		if (allowed) recent.push(now);
		this.attempts.delete(key);
		if (recent.length > 0) this.attempts.set(key, recent);
		return allowed;
	}

	private sweep(now: number): void {
		for (const [key, times] of this.attempts) {
			if (now - times[times.length - 1] >= LOGIN_WINDOW_MS) this.attempts.delete(key);
		}
		// Re-inserted on every use, so the first keys are the least recently active.
		for (const key of this.attempts.keys()) {
			if (this.attempts.size < LOGIN_LIMITER_MAX_KEYS) break;
			this.attempts.delete(key);
		}
	}
}
