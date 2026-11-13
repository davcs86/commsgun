import { createHash, createHmac, timingSafeEqual } from 'node:crypto';
import { LOGIN_MAX_ATTEMPTS, LOGIN_WINDOW_MS, SESSION_TTL_MS } from './constants';

const sign = (secret: string, payload: string): Buffer =>
	createHmac('sha256', secret).update(payload).digest();

export function signSession(secret: string, now: number): string {
	const payload = Buffer.from(JSON.stringify({ exp: now + SESSION_TTL_MS })).toString('base64url');
	return `${payload}.${sign(secret, payload).toString('base64url')}`;
}

export function verifySession(secret: string, cookie: string | undefined, now: number): boolean {
	try {
		const parts = (cookie ?? '').split('.');
		if (parts.length !== 2) return false;
		const [payload, signature] = parts;
		const given = Buffer.from(signature, 'base64url');
		const expected = sign(secret, payload);
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

// Only a same-origin absolute path is honoured; anything else falls back to "/".
export function safeNext(next: string | null): string {
	if (next && next.startsWith('/') && !next.startsWith('//') && !next.startsWith('/\\'))
		return next;
	return '/';
}

export class LoginLimiter {
	private readonly attempts = new Map<string, number[]>();

	allow(key: string, now: number): boolean {
		const recent = (this.attempts.get(key) ?? []).filter((t) => now - t < LOGIN_WINDOW_MS);
		const allowed = recent.length < LOGIN_MAX_ATTEMPTS;
		if (allowed) recent.push(now);
		if (recent.length > 0) this.attempts.set(key, recent);
		else this.attempts.delete(key);
		return allowed;
	}
}
