export const API_TIMEOUT_MS = 5000;
export const TEXT_FILTER_MAX_BYTES = 200;
export const MAX_CUSTOM_RANGE_MS = 30 * 24 * 60 * 60 * 1000;

export const RANGE_SECONDS = {
	'1h': 3600,
	'3h': 3 * 3600,
	'6h': 6 * 3600,
	'12h': 12 * 3600,
	'24h': 24 * 3600,
	'3d': 3 * 86400,
	'7d': 7 * 86400,
	'14d': 14 * 86400,
	'30d': 30 * 86400
} as const;
export const REFRESH_INTERVAL_MS = 30_000;
export const PROBE_DESTINATION_MAX = 320;
