import { error } from '@sveltejs/kit';
import { createApi } from '#lib/server/api.js';
import { getEnv } from '#lib/server/env.js';
import type { PageServerLoad } from './$types';

export const load: PageServerLoad = async ({ params, fetch }) => {
	const detail = await createApi(getEnv(), fetch).message(params.id);
	if (!detail.ok && detail.reason === 'not_found') error(404, 'Not found');
	return { detail };
};
