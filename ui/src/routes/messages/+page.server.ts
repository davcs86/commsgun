import { parseFilters } from '#lib/filters.js';
import { createApi } from '#lib/server/api.js';
import { getEnv } from '#lib/server/env.js';
import type { PageServerLoad } from './$types';

export const load: PageServerLoad = async ({ url, fetch }) => {
	const filters = parseFilters(url.searchParams, new Date());
	const cursor = url.searchParams.get('cursor') || undefined;
	const api = createApi(getEnv(), fetch);
	const [messages, facets] = await Promise.all([api.messages(filters, cursor), api.facets()]);
	return { filters, messages, facets };
};
