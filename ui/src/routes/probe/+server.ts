import { json } from '@sveltejs/kit';
import { parseProbeInput, validDestination, type ProbeResult } from '#lib/probe.js';
import { createApi } from '#lib/server/api.js';
import { getEnv } from '#lib/server/env.js';
import type { RequestHandler } from './$types';

const NO_STORE = { 'cache-control': 'no-store' };

export const GET: RequestHandler = async ({ fetch }) => {
	const options = await createApi(getEnv(), fetch).probeOptions();
	if (!options.ok) return json({ error: 'unavailable' }, { status: 503, headers: NO_STORE });
	return json(options.data, { headers: NO_STORE });
};

// The session cookie is SameSite=Strict; the Origin check also refuses a same-site cross-origin page.
export const POST: RequestHandler = async ({ request, fetch, getClientAddress }) => {
	const env = getEnv();
	if (request.headers.get('origin') !== env.origin) {
		return json({ error: 'bad_origin' }, { status: 403, headers: NO_STORE });
	}
	const input = parseProbeInput(await request.json().catch(() => null));
	if (!input) return json({ error: 'bad_request' }, { status: 400, headers: NO_STORE });
	const result: ProbeResult = validDestination(input.channel, input.to)
		? await createApi(env, fetch).sendProbe(input, getClientAddress())
		: { kind: 'invalid', field: 'to' };
	return json(result, { headers: NO_STORE });
};
