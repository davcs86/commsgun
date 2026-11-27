import { createServer, type IncomingMessage, type ServerResponse } from 'node:http';
import { resolve } from 'node:path';

export const STUB_PORT = 4181;
export const STUB_URL = `http://127.0.0.1:${STUB_PORT}`;
export const STUB_TOKEN = 'fake-admin-api-token-00000000000000';
export const TEST_ADMIN_PASSWORD = 'fake-admin-password-0000';
export const TEST_SESSION_SECRET = 'fake-session-secret-000000000000000';
// The only session the stub accepts; both tokens have the API's shape (43 base64url characters).
export const STUB_SESSION_TOKEN = 'fake-session-token-000000000000000000000000';
export const STUB_SESSION_TOKEN_2 = 'fake-session-token-000000000000000000000002';
export const STUB_USER = { id: 'fake-user-0001', username: 'admin', role: 'admin' } as const;
// Not 12 h, so a cookie lifetime that follows expires_at is told apart from a constant.
export const STUB_SESSION_TTL_MS = 3 * 60 * 60 * 1000;
export const AUTH_MODES = ['local', 'oidc', 'both'] as const;
export type AuthSettings = {
	mode: (typeof AUTH_MODES)[number];
	sessionsLocal?: 'unauthorized' | 'mode';
};
export type SessionCall = { method: string; session: string | null };

export const SCENARIOS = ['normal', 'empty', 'down', 'hostile', 'mixed', 'auth', 'window'] as const;
export type Scenario = (typeof SCENARIOS)[number];
// `message` is the detail route `messages/<id>`; `probes` covers both GET and POST;
// `session_check` is only GET session (the hook's lookup), so DELETE session still answers.
export const ENDPOINTS = [
	'stats',
	'overview',
	'facets',
	'messages',
	'message',
	'probes',
	'session_check'
] as const;
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
	const windowFull = scenario === 'window';
	return {
		queue_depth: scenario === 'empty' ? 0 : 2,
		oldest_waiting_at: scenario === 'empty' ? null : new Date(now - 10 * MINUTE).toISOString(),
		providers: [
			{
				channel: 'email',
				name: 'resend',
				minute: { used: scenario === 'empty' ? 0 : 3, limit: 10 },
				day: {
					used: scenario === 'empty' ? 0 : paused || windowFull ? 100 : 47,
					limit: 100,
					resets_at: resets
				},
				paused_until:
					paused || windowFull ? resets : auth ? new Date(now + 10 * MINUTE).toISOString() : null,
				pause_reason: paused ? 'daily_quota' : auth ? 'auth' : windowFull ? 'window_day' : null,
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
			next_attempt_at: state === 'queued' ? new Date(now + MINUTE).toISOString() : null,
			source_ip: '192.0.2.10'
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

export const PROBE_MODES = ['normal', 'missing', 'mismatch', 'nosender'] as const;
export type ProbeMode = (typeof PROBE_MODES)[number];
export type ReceivedProbe = { key: string; body: unknown; forwardedFor: string };
export const PROBE_SENDER = 'Ops <ops@example.com>';
export const PROBE_LIMITED = 'limited@example.com';
export const PROBE_EMAIL_ID = 'msg-0001';
export const PROBE_SMS_ID = 'msg-0005';

type State = {
	scenario: Scenario;
	fail: string[];
	requests: string[];
	probeMode: ProbeMode;
	probes: ReceivedProbe[];
	imports: { username: string }[];
	auth: AuthSettings;
	sessionCalls: SessionCall[];
	issued: string[];
};

function probeOptions(mode: ProbeMode) {
	if (mode === 'missing') return { app: 'ops', available: false, channels: [] };
	return {
		app: 'ops',
		available: true,
		channels: [
			{
				channel: 'email',
				enabled: true,
				provider: mode === 'mismatch' ? 'other' : 'resend',
				sender: mode === 'nosender' ? null : PROBE_SENDER
			},
			{ channel: 'sms', enabled: true, provider: null, sender: null }
		]
	};
}

// Mirrors the API's result table closely enough for the dashboard's mapping to be exercised.
async function handleProbe(state: State, req: IncomingMessage, res: ServerResponse) {
	const key = String(req.headers['idempotency-key'] ?? '');
	const body = JSON.parse((await readBody(req)) || '{}') as Record<string, unknown>;
	const { channel, to, provider } = body;
	const mismatch = state.probeMode === 'mismatch';
	const want = channel !== 'email' ? null : mismatch ? 'other' : 'resend';
	if (state.probeMode === 'missing') return send(res, 409, { error: 'probe_app_missing' });
	if (provider !== want) return send(res, 409, { error: 'provider_mismatch' });
	if (state.probeMode === 'nosender' && channel === 'email') {
		return send(res, 422, { error: 'sender_required', field: 'from' });
	}
	const valid =
		typeof to === 'string' &&
		(channel === 'sms' ? /^\+[1-9][0-9]{1,14}$/ : /^[^\s<>@]+@[^\s<>@]+$/).test(to);
	if (!valid) return send(res, 422, { error: 'invalid_recipient', field: 'to' });
	if (to === PROBE_LIMITED) {
		res.setHeader('retry-after', '42');
		return send(res, 429, { error: 'rate_limited', window: 'minute' });
	}
	const duplicate = state.probes.some((p) => p.key === key);
	if (!duplicate) {
		state.probes.push({ key, body, forwardedFor: String(req.headers['x-forwarded-for'] ?? '') });
	}
	return send(res, duplicate ? 200 : 202, {
		request_id: 'req-probe',
		message_ids: [channel === 'sms' ? PROBE_SMS_ID : PROBE_EMAIL_ID],
		duplicate,
		held: channel === 'sms'
	});
}

function send(res: ServerResponse, status: number, body?: unknown) {
	res.writeHead(status, { 'content-type': 'application/json' });
	res.end(body === undefined ? '' : JSON.stringify(body));
}

async function readBody(req: IncomingMessage): Promise<string> {
	let data = '';
	for await (const chunk of req) data += chunk;
	return data;
}

function failKey(route: string, method: string): string {
	if (route === 'session' && method === 'GET') return 'session_check';
	return route.startsWith('messages/') ? 'message' : route;
}

function handleAdmin(state: State, res: ServerResponse, url: URL, route: string) {
	const now = Date.now();
	const s = state.scenario;
	if (route === 'probes') return send(res, 200, probeOptions(state.probeMode));
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

// Mirrors the API: the bearer first, then the session on every route but these four.
const UNGATED = new Set(['auth', 'sessions/local', 'sessions/oidc', 'bootstrap/import']);

async function sessionsLocal(state: State, req: IncomingMessage, res: ServerResponse) {
	const body = JSON.parse((await readBody(req)) || '{}') as Record<string, unknown>;
	if (state.auth.sessionsLocal === 'unauthorized') return send(res, 401, { error: 'unauthorized' });
	if (state.auth.sessionsLocal === 'mode' || state.auth.mode === 'oidc') {
		return send(res, 403, { error: 'mode' });
	}
	if (body.username !== STUB_USER.username || body.password !== TEST_ADMIN_PASSWORD) {
		return send(res, 401, { error: 'invalid_credentials' });
	}
	const expires = new Date(Date.now() + STUB_SESSION_TTL_MS).toISOString();
	state.issued.push(expires);
	return send(res, 201, { token: STUB_SESSION_TOKEN, expires_at: expires, user: STUB_USER });
}

async function handleApi(state: State, req: IncomingMessage, res: ServerResponse, url: URL) {
	const route = url.pathname.slice('/admin/v1/'.length);
	const method = req.method ?? 'GET';
	if (method === 'POST' && route === 'bootstrap/import') {
		if (req.headers.authorization !== `Bearer ${STUB_TOKEN}`) return send(res, 401);
		const body = JSON.parse((await readBody(req)) || '{}') as { username?: unknown };
		const result = state.imports.length === 0 ? 'imported' : 'skipped';
		state.imports.push({ username: String(body.username) });
		return send(res, 200, { result });
	}
	state.requests.push(url.pathname + url.search);
	const session = req.headers['commsgun-session'];
	if (route === 'session') {
		state.sessionCalls.push({ method, session: typeof session === 'string' ? session : null });
	}
	if (state.scenario === 'down' || state.fail.includes(failKey(route, method))) {
		req.socket.destroy();
		return;
	}
	if (req.headers.authorization !== `Bearer ${STUB_TOKEN}`) return send(res, 401);
	if (!UNGATED.has(route)) {
		if (route === 'session' && method === 'DELETE') return send(res, 204);
		if (session !== STUB_SESSION_TOKEN) return send(res, 401, { error: 'session' });
	}
	if (method === 'GET' && route === 'auth') return send(res, 200, { mode: state.auth.mode });
	if (method === 'POST' && route === 'sessions/local') return sessionsLocal(state, req, res);
	if (method === 'GET' && route === 'session') {
		const expires = state.issued.at(-1) ?? new Date(Date.now() + STUB_SESSION_TTL_MS).toISOString();
		return send(res, 200, { user: STUB_USER, kind: 'local', expires_at: expires });
	}
	if (method === 'POST' && route === 'probes') return handleProbe(state, req, res);
	if (method === 'GET') return handleAdmin(state, res, url, route);
	return send(res, 404);
}

export function startStub(port = STUB_PORT) {
	const state: State = {
		scenario: 'normal',
		fail: [],
		requests: [],
		probeMode: 'normal',
		probes: [],
		imports: [],
		auth: { mode: 'local' },
		sessionCalls: [],
		issued: []
	};
	const server = createServer(async (req, res) => {
		const url = new URL(req.url ?? '/', STUB_URL);
		if (url.pathname.startsWith('/admin/v1/')) return handleApi(state, req, res, url);
		if (req.method === 'POST' && url.pathname === '/__auth') {
			const body = JSON.parse((await readBody(req)) || '{}') as Partial<AuthSettings>;
			if (!(AUTH_MODES as readonly string[]).includes(body.mode ?? '')) return send(res, 400);
			if (![undefined, 'unauthorized', 'mode'].includes(body.sessionsLocal)) return send(res, 400);
			state.auth = { mode: body.mode as AuthSettings['mode'], sessionsLocal: body.sessionsLocal };
			return send(res, 204);
		}
		if (req.method === 'GET' && url.pathname === '/__session-calls') {
			return send(res, 200, state.sessionCalls);
		}
		if (req.method === 'GET' && url.pathname === '/__issued') return send(res, 200, state.issued);
		if (req.method === 'POST' && url.pathname === '/__probe-mode') {
			const body = JSON.parse((await readBody(req)) || '{}') as { mode?: string };
			if (!(PROBE_MODES as readonly string[]).includes(body.mode ?? '')) return send(res, 400);
			state.probeMode = body.mode as ProbeMode;
			return send(res, 204);
		}
		if (req.method === 'GET' && url.pathname === '/__imports') {
			return send(res, 200, state.imports);
		}
		if (req.method === 'GET' && url.pathname === '/__probes') {
			return send(res, 200, state.probes);
		}
		if (req.method === 'POST' && url.pathname === '/__scenario') {
			const body = JSON.parse((await readBody(req)) || '{}') as { name?: string; fail?: string[] };
			if (!(SCENARIOS as readonly string[]).includes(body.name ?? '')) return send(res, 400);
			state.scenario = body.name as Scenario;
			state.fail = (body.fail ?? []).filter((e) => (ENDPOINTS as readonly string[]).includes(e));
			state.probeMode = 'normal';
			state.probes = [];
			state.auth = { mode: 'local' };
			state.sessionCalls = [];
			state.issued = [];
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

export async function setProbeMode(mode: ProbeMode): Promise<void> {
	const res = await fetch(`${STUB_URL}/__probe-mode`, {
		method: 'POST',
		body: JSON.stringify({ mode })
	});
	if (res.status !== 204) throw new Error(`setProbeMode ${mode}: ${res.status}`);
}

export async function receivedProbes(): Promise<ReceivedProbe[]> {
	return (await (await fetch(`${STUB_URL}/__probes`)).json()) as ReceivedProbe[];
}

export async function receivedRequests(): Promise<URL[]> {
	const res = await fetch(`${STUB_URL}/__requests`);
	return ((await res.json()) as string[]).map((p) => new URL(p, STUB_URL));
}

export async function setAuth(auth: AuthSettings): Promise<void> {
	const res = await fetch(`${STUB_URL}/__auth`, { method: 'POST', body: JSON.stringify(auth) });
	if (res.status !== 204) throw new Error(`setAuth ${auth.mode}: ${res.status}`);
}

export async function sessionCalls(): Promise<SessionCall[]> {
	return (await (await fetch(`${STUB_URL}/__session-calls`)).json()) as SessionCall[];
}

// The expires_at of every session the stub created, oldest first.
export async function issuedSessions(): Promise<string[]> {
	return (await (await fetch(`${STUB_URL}/__issued`)).json()) as string[];
}

export async function resetRequests(): Promise<void> {
	await fetch(`${STUB_URL}/__requests/reset`, { method: 'POST' });
}

if (process.argv[1] && resolve(process.argv[1]) === import.meta.filename) startStub();
