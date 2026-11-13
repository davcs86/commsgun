import { createServer, type IncomingMessage, type ServerResponse } from 'node:http';
import { resolve } from 'node:path';

export const STUB_PORT = 4181;
export const STUB_URL = `http://127.0.0.1:${STUB_PORT}`;
export const STUB_TOKEN = 'fake-admin-api-token-00000000000000';
export const TEST_ADMIN_PASSWORD = 'fake-admin-password-0000';
export const TEST_SESSION_SECRET = 'fake-session-secret-000000000000000';

export const SCENARIOS = ['normal', 'empty', 'down', 'hostile', 'mixed', 'auth'] as const;
export type Scenario = (typeof SCENARIOS)[number];
export const ENDPOINTS = ['stats', 'overview', 'facets', 'messages'] as const;
export type Endpoint = (typeof ENDPOINTS)[number];

export const HOSTILE_RECIPIENT = '<script>alert(1)</script>@x.io';
export const HOSTILE_ERROR = 'E'.repeat(300);
export const MESSAGE_COUNT = 64;
// A bounced SMS with two failed attempts, so its detail page shows the hostile error text.
export const HOSTILE_INDEX = 4;
// A queued email held because no email provider is configured.
export const HELD_EMAIL_INDEX = 13;
const PAGE_SIZE = 50;
const MINUTE = 60_000;
const DAY = 86_400_000;

type Counts = {
	enqueued: number;
	sent: number;
	delivered: number;
	bounced: number;
	complained: number;
	failed: number;
	expired: number;
	retries: number;
};
const COUNT_KEYS = [
	'enqueued',
	'sent',
	'delivered',
	'bounced',
	'complained',
	'failed',
	'expired',
	'retries'
] as const;

// Outcomes a provider accepted first, so each is also counted in `sent`.
export const ACCEPTED_OUTCOMES = ['delivered', 'bounced', 'complained'] as const;

const NORMAL_TOTALS: Counts & { queued_now: number } = {
	enqueued: 44,
	sent: 44,
	delivered: 41,
	bounced: 1,
	complained: 0,
	failed: 0,
	expired: 0,
	retries: 4,
	queued_now: 0
};

const MIXED_TOTALS: Counts & { queued_now: number } = {
	enqueued: 40,
	sent: 36,
	delivered: 30,
	bounced: 3,
	complained: 1,
	failed: 2,
	expired: 3,
	retries: 5,
	queued_now: 6
};

function bucketSeconds(spanMs: number): number {
	const h = spanMs / 3_600_000;
	if (h <= 1) return 60;
	if (h <= 6) return 300;
	if (h <= 24) return 900;
	if (h <= 7 * 24) return 3600;
	if (h <= 14 * 24) return 21_600;
	return 86_400;
}

function window(query: URLSearchParams, now: number) {
	const toQ = Date.parse(query.get('to') ?? '');
	const fromQ = Date.parse(query.get('from') ?? '');
	const to0 = Number.isNaN(toQ) ? now : toQ;
	const from0 = Number.isNaN(fromQ) ? to0 - 3_600_000 : fromQ;
	const bucket = bucketSeconds(to0 - from0);
	const b = bucket * 1000;
	const to = Math.ceil(to0 / b) * b;
	const from = to - Math.ceil((to0 - from0) / b) * b;
	return { from, to, bucket };
}

function stats(scenario: Scenario, query: URLSearchParams, now: number) {
	const { from, to, bucket } = window(query, now);
	const n = Math.round((to - from) / (bucket * 1000));
	const zero = (): Counts => ({
		enqueued: 0,
		sent: 0,
		delivered: 0,
		bounced: 0,
		complained: 0,
		failed: 0,
		expired: 0,
		retries: 0
	});
	const series = Array.from({ length: n }, (_, i) => ({
		t: new Date(from + i * bucket * 1000).toISOString(),
		...zero()
	}));
	const totals =
		scenario === 'empty'
			? { ...zero(), queued_now: 0 }
			: scenario === 'mixed'
				? { ...MIXED_TOTALS }
				: { ...NORMAL_TOTALS };
	const place = (key: (typeof COUNT_KEYS)[number], k: number) => (series[(k * 7) % n][key] += 1);
	for (const key of COUNT_KEYS) {
		if (!(ACCEPTED_OUTCOMES as readonly string[]).includes(key)) {
			for (let k = 0; k < totals[key]; k++) place(key, k);
		}
	}
	// Accepted outcomes reuse the sent buckets' slots, so every bucket keeps sent >= their sum.
	let slot = 0;
	for (const key of ACCEPTED_OUTCOMES) {
		for (let k = 0; k < totals[key]; k++) place(key, slot++);
	}
	const empty = scenario === 'empty';
	return {
		from: new Date(from).toISOString(),
		to: new Date(to).toISOString(),
		bucket_seconds: bucket,
		totals,
		series,
		by_error_type: empty
			? []
			: [
					{ error_type: 'timeout', count: 3 },
					{ error_type: 'bounced', count: 1 },
					...(scenario === 'hostile' ? [{ error_type: HOSTILE_ERROR, count: 1 }] : [])
				],
		by_app: empty
			? []
			: [
					{ app: 'xstockstrat', count: 44 },
					...(scenario === 'hostile' ? [{ app: HOSTILE_RECIPIENT, count: 1 }] : [])
				],
		by_channel: empty ? [] : [{ channel: 'email', count: 44 }]
	};
}

function nextUtcMidnight(now: number): string {
	return new Date(Math.floor(now / DAY) * DAY + DAY).toISOString();
}

function mixedOverview(now: number) {
	const resets = nextUtcMidnight(now);
	const until = new Date(now + 2 * MINUTE).toISOString();
	return {
		queue_depth: 6,
		oldest_waiting_at: new Date(now - 5 * MINUTE).toISOString(),
		providers: [
			{
				channel: 'email',
				name: 'resend',
				minute: { used: 1, limit: 10 },
				day: { used: 20, limit: 100, resets_at: resets },
				paused_until: resets,
				pause_reason: 'monthly_quota',
				held: 4
			},
			{
				channel: 'sms',
				name: 'fake-sms',
				minute: { used: 5, limit: 5 },
				day: { used: 12, limit: 0, resets_at: resets },
				paused_until: until,
				pause_reason: 'rate_limit',
				held: 2
			}
		]
	};
}

function overview(scenario: Scenario, now: number) {
	if (scenario === 'mixed') return mixedOverview(now);
	const resets = nextUtcMidnight(now);
	const paused = scenario === 'hostile';
	const auth = scenario === 'auth';
	return {
		queue_depth: scenario === 'empty' ? 0 : 2,
		oldest_waiting_at: scenario === 'empty' ? null : new Date(now - 10 * MINUTE).toISOString(),
		providers: [
			{
				channel: 'email',
				name: 'resend',
				minute: { used: scenario === 'empty' ? 0 : 3, limit: 10 },
				day: {
					used: scenario === 'empty' ? 0 : paused ? 100 : 47,
					limit: 100,
					resets_at: resets
				},
				paused_until: paused ? resets : auth ? new Date(now + 10 * MINUTE).toISOString() : null,
				pause_reason: paused ? 'daily_quota' : auth ? 'auth' : null,
				held: 0
			},
			{
				channel: 'sms',
				name: null,
				minute: { used: 0, limit: 0 },
				day: { used: 0, limit: 0, resets_at: resets },
				paused_until: null,
				pause_reason: null,
				held: scenario === 'empty' ? 0 : 2
			}
		]
	};
}

function facets(scenario: Scenario) {
	const empty = scenario === 'empty';
	return {
		apps: empty ? [] : ['homelab', 'xstockstrat'],
		senders: empty ? [] : ['alerts@example.com', 'noreply@example.com'],
		error_types: empty ? [] : ['bounced', 'timeout'],
		channels: ['email', 'sms'],
		states: ['queued', 'sending', 'sent', 'delivered', 'bounced', 'complained', 'failed', 'expired']
	};
}

const STATE_CYCLE = ['delivered', 'delivered', 'delivered', 'sent', 'bounced', 'failed', 'queued'];

function messageFixtures(scenario: Scenario, now: number) {
	return Array.from({ length: MESSAGE_COUNT }, (_, i) => {
		const created = now - (i + 1) * 50_000;
		const state = STATE_CYCLE[i % STATE_CYCLE.length];
		const channel = i % 5 === 4 ? 'sms' : 'email';
		const failed = state === 'bounced' || state === 'failed';
		const errorType = state === 'bounced' ? 'bounced' : state === 'failed' ? 'timeout' : null;
		const attemptCount = state === 'queued' ? 0 : failed ? 2 : 1;
		const id = `msg-${String(i + 1).padStart(4, '0')}`;
		const hostile = scenario === 'hostile' && i === HOSTILE_INDEX;
		const message = {
			id,
			request_id: `req-${String(i + 1).padStart(4, '0')}`,
			app: i % 6 === 5 ? 'homelab' : 'xstockstrat',
			channel,
			recipient: hostile
				? HOSTILE_RECIPIENT
				: channel === 'sms'
					? `+5255000${String(i).padStart(4, '0')}`
					: `user${i}@example.com`,
			sender: channel === 'sms' ? '+15550000000' : 'alerts@example.com',
			state,
			hold_reason:
				state === 'queued' && (channel === 'sms' || i === HELD_EMAIL_INDEX) ? 'no_provider' : null,
			attempts: attemptCount,
			error_type: errorType,
			provider: attemptCount > 0 ? 'resend' : null,
			provider_message_id: attemptCount > 0 && !failed ? `re_${i}` : null,
			body_bytes: 1200 + i,
			created_at: new Date(created).toISOString(),
			sent_at: attemptCount > 0 && !failed ? new Date(created + 2000).toISOString() : null,
			final_at: state === 'delivered' || failed ? new Date(created + 9000).toISOString() : null,
			expires_at: new Date(created + DAY).toISOString(),
			next_attempt_at: state === 'queued' ? new Date(now + MINUTE).toISOString() : null
		};
		const attempts = Array.from({ length: attemptCount }, (_, n) => {
			const ok = n === attemptCount - 1 && !failed;
			return {
				n: n + 1,
				started_at: new Date(created + 1000 + n * 30_000).toISOString(),
				duration_ms: 180 + n * 20,
				outcome: ok ? 'accepted' : 'retryable',
				error_type: ok ? null : (errorType ?? 'timeout'),
				provider_status: ok ? 200 : 503,
				provider_error: ok ? null : hostile ? HOSTILE_ERROR : 'upstream unavailable',
				message: ok ? 'accepted' : hostile ? HOSTILE_ERROR : 'provider did not answer in time'
			};
		});
		return { message, attempts };
	});
}

function matches(m: Record<string, unknown>, q: URLSearchParams): boolean {
	const created = Date.parse(m.created_at as string);
	const from = Date.parse(q.get('from') ?? '');
	const to = Date.parse(q.get('to') ?? '');
	if (!Number.isNaN(from) && created < from) return false;
	if (!Number.isNaN(to) && created >= to) return false;
	for (const key of ['channel', 'app', 'state', 'error_type'] as const) {
		const v = q.get(key);
		if (v && m[key] !== v) return false;
	}
	for (const key of ['recipient', 'sender'] as const) {
		const v = q.get(key)?.toLowerCase();
		if (v && !(m[key] as string).toLowerCase().includes(v)) return false;
	}
	return true;
}

function messages(scenario: Scenario, q: URLSearchParams, now: number) {
	if (scenario === 'empty') return { items: [], next_cursor: null };
	const all = messageFixtures(scenario, now)
		.map((f) => f.message)
		.filter((m) => matches(m, q));
	const cursor = q.get('cursor') ?? '0';
	if (!/^\d+$/.test(cursor)) return undefined;
	const start = Number(cursor);
	const items = all.slice(start, start + PAGE_SIZE);
	const next = start + PAGE_SIZE < all.length ? String(start + PAGE_SIZE) : null;
	return { items, next_cursor: next };
}

function messageDetail(scenario: Scenario, id: string, now: number) {
	if (scenario === 'empty') return undefined;
	return messageFixtures(scenario, now).find((f) => f.message.id === id);
}

type State = { scenario: Scenario; fail: string[]; requests: string[] };

function send(res: ServerResponse, status: number, body?: unknown) {
	res.writeHead(status, { 'content-type': 'application/json' });
	res.end(body === undefined ? '' : JSON.stringify(body));
}

async function readBody(req: IncomingMessage): Promise<string> {
	let data = '';
	for await (const chunk of req) data += chunk;
	return data;
}

function handleAdmin(state: State, req: IncomingMessage, res: ServerResponse, url: URL) {
	state.requests.push(url.pathname + url.search);
	const route = url.pathname.slice('/admin/v1/'.length);
	if (state.scenario === 'down' || state.fail.includes(route.split('/')[0])) {
		req.socket.destroy();
		return;
	}
	if (req.headers.authorization !== `Bearer ${STUB_TOKEN}`) return send(res, 401);
	const now = Date.now();
	const s = state.scenario;
	if (route === 'stats') return send(res, 200, stats(s, url.searchParams, now));
	if (route === 'overview') return send(res, 200, overview(s, now));
	if (route === 'facets') return send(res, 200, facets(s));
	if (route === 'messages') {
		const page = messages(s, url.searchParams, now);
		return page ? send(res, 200, page) : send(res, 400, { error: 'invalid_cursor' });
	}
	const detail = route.startsWith('messages/')
		? messageDetail(s, decodeURIComponent(route.slice('messages/'.length)), now)
		: undefined;
	if (detail) return send(res, 200, detail);
	return send(res, 404, { error: 'not_found' });
}

export function startStub(port = STUB_PORT) {
	const state: State = { scenario: 'normal', fail: [], requests: [] };
	const server = createServer(async (req, res) => {
		const url = new URL(req.url ?? '/', STUB_URL);
		if (req.method === 'GET' && url.pathname.startsWith('/admin/v1/')) {
			return handleAdmin(state, req, res, url);
		}
		if (req.method === 'POST' && url.pathname === '/__scenario') {
			const body = JSON.parse((await readBody(req)) || '{}') as { name?: string; fail?: string[] };
			if (!(SCENARIOS as readonly string[]).includes(body.name ?? '')) return send(res, 400);
			state.scenario = body.name as Scenario;
			state.fail = (body.fail ?? []).filter((e) => (ENDPOINTS as readonly string[]).includes(e));
			return send(res, 204);
		}
		if (req.method === 'GET' && url.pathname === '/__requests') {
			return send(res, 200, state.requests);
		}
		if (req.method === 'POST' && url.pathname === '/__requests/reset') {
			state.requests = [];
			return send(res, 204);
		}
		return send(res, 404);
	});
	server.listen(port, '127.0.0.1');
	return server;
}

export async function setScenario(name: Scenario, fail: Endpoint[] = []): Promise<void> {
	const res = await fetch(`${STUB_URL}/__scenario`, {
		method: 'POST',
		body: JSON.stringify({ name, fail })
	});
	if (res.status !== 204) throw new Error(`setScenario ${name}: ${res.status}`);
}

export async function receivedRequests(): Promise<URL[]> {
	const res = await fetch(`${STUB_URL}/__requests`);
	return ((await res.json()) as string[]).map((p) => new URL(p, STUB_URL));
}

export async function resetRequests(): Promise<void> {
	await fetch(`${STUB_URL}/__requests/reset`, { method: 'POST' });
}

if (process.argv[1] && resolve(process.argv[1]) === import.meta.filename) startStub();
