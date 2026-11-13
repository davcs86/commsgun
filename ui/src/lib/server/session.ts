import { createHash, createHmac, timingSafeEqual } from 'node:crypto';
import {
	LOGIN_LIMITER_MAX_KEYS,
	LOGIN_MAX_ATTEMPTS,
	LOGIN_WINDOW_MS,
	SESSION_KEY_LABEL,
	SESSION_TTL_MS
} from './constants';

const sign = (key: Buffer, payload: string): Buffer =>
	createHmac('sha256', key).update(payload).digest();

// Depends on the password too, so changing either ADMIN_PASSWORD or SESSION_SECRET ends every session.
export function deriveSessionKey(secret: string, password: string): Buffer {
	return createHmac('sha256', secret).update(`${SESSION_KEY_LABEL}\0${password}`).digest();
}

export function signSession(key: Buffer, now: number): string {
	const payload = Buffer.from(JSON.stringify({ exp: now + SESSION_TTL_MS })).toString('base64url');
	return `${payload}.${sign(key, payload).toString('base64url')}`;
}

export function verifySession(key: Buffer, cookie: string | undefined, now: number): boolean {
	try {
		const parts = (cookie ?? '').split('.');
		if (parts.length !== 2) return false;
		const [payload, signature] = parts;
		const given = Buffer.from(signature, 'base64url');
		const expected = sign(key, payload);
		if (given.toString('base64url') !== signature) return false;
		if (given.length !== expected.length || !timingSafeEqual(given, expected)) return false;
		const { exp } = JSON.parse(Buffer.from(payload, 'base64url').toString('utf8'));
		return typeof exp === 'number' && exp > now;
	} catch {
		return false;
	}
}

export function checkPassword(given: string, expected: string): boolean {
	const digest = (value: string) => createHash('sha256').update(value).digest();
	return timingSafeEqual(digest(given), digest(expected));
}

// Only a plain same-origin path is honoured; login and logout targets and anything odd map to "/".
export function safeNext(next: string | null): string {
	if (!next || !next.startsWith('/') || next.startsWith('//')) return '/';
	// eslint-disable-next-line no-control-regex -- control characters are exactly what is rejected
	if (/[\x00-\x20\x7f\\]/.test(next)) return '/';
	try {
		const url = new URL(next, 'http://x');
		if (url.origin !== 'http://x') return '/';
		const { pathname } = url;
		if (pathname.startsWith('//')) return '/';
		if (/^\/(login|logout)\/?$/.test(pathname)) return '/';
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
