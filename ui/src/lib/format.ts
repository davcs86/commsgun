const KB = 1000;
const UNITS = ['B', 'kB', 'MB', 'GB'] as const;

export function formatBytes(bytes: number): string {
	if (!Number.isFinite(bytes) || bytes < 0) return 'unknown';
	let value = bytes;
	let unit = 0;
	while (value >= KB && unit < UNITS.length - 1) {
		value /= KB;
		unit++;
	}
	return unit === 0 ? `${value} B` : `${value.toFixed(1)} ${UNITS[unit]}`;
}
