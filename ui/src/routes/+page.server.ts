import { parseFilters } from '#lib/filters.js';
import { createApi } from '#lib/server/api.js';
import { getEnv } from '#lib/server/env.js';
import type { PageServerLoad } from './$types';

export const load: PageServerLoad = async ({ url, fetch }) => {
	const filters = parseFilters(url.searchParams, new Date());
	const api = createApi(getEnv(), fetch);
	const [stats, overview, facets] = await Promise.all([
		api.stats(filters),
		api.overview(),
		api.facets()
	]);
	return { filters, stats, overview, facets };
};
