import { createCipheriv, createDecipheriv, randomBytes } from 'node:crypto';

// SSO spec § 3 "Callback state": the callback's only state.
export type Transaction = {
	purpose: 'login' | 'link';
	state: string;
	nonce: string;
	verifier: string;
	next: string;
	started: number;
	exp: number;
	binding?: string;
	username?: string;
	iss?: string;
	sub?: string;
	preferredUsername?: string | null;
};

const IV_BYTES = 12;
const TAG_BYTES = 16;

// AES-256-GCM over the JSON: iv || ciphertext || tag, base64url.
export function sealTransaction(key: Buffer, tx: Transaction): string {
	const iv = randomBytes(IV_BYTES);
	const cipher = createCipheriv('aes-256-gcm', key, iv);
	const body = Buffer.concat([cipher.update(JSON.stringify(tx), 'utf8'), cipher.final()]);
	return Buffer.concat([iv, body, cipher.getAuthTag()]).toString('base64url');
}

const isString = (v: unknown) => typeof v === 'string';

// Tampered, expired or junk → null; never throws.
export function openTransaction(
	key: Buffer,
	value: string | undefined,
	now: number
): Transaction | null {
	if (!value || !/^[A-Za-z0-9_-]+$/.test(value)) return null;
	const raw = Buffer.from(value, 'base64url');
	if (raw.length <= IV_BYTES + TAG_BYTES) return null;
	try {
		const decipher = createDecipheriv('aes-256-gcm', key, raw.subarray(0, IV_BYTES));
		decipher.setAuthTag(raw.subarray(raw.length - TAG_BYTES));
		const plain = Buffer.concat([
			decipher.update(raw.subarray(IV_BYTES, raw.length - TAG_BYTES)),
			decipher.final()
		]);
		const tx = JSON.parse(plain.toString('utf8')) as Transaction;
		const valid =
			(tx.purpose === 'login' || tx.purpose === 'link') &&
			[tx.state, tx.nonce, tx.verifier, tx.next].every(isString) &&
			Number.isFinite(tx.started) &&
			Number.isFinite(tx.exp);
		return valid && now < tx.exp ? tx : null;
	} catch {
		return null;
	}
}
