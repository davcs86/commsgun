const SECONDS_PER_DAY = 86400;

const pad = (n: number) => String(n).padStart(2, '0');

export function formatUtc(d: Date): string {
	return (
		`${d.getUTCFullYear()}-${pad(d.getUTCMonth() + 1)}-${pad(d.getUTCDate())} ` +
		`${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())}:${pad(d.getUTCSeconds())} UTC`
	);
}

// timeZone undefined means the runtime's zone (the browser's).
export function formatLocal(d: Date, timeZone?: string): string {
	const parts = new Intl.DateTimeFormat('en-CA', {
		timeZone,
		year: 'numeric',
		month: '2-digit',
		day: '2-digit',
		hour: '2-digit',
		minute: '2-digit',
		second: '2-digit',
		hourCycle: 'h23',
		timeZoneName: 'short'
	}).formatToParts(d);
	const get = (type: string) => parts.find((p) => p.type === type)?.value ?? '';
	return (
		`${get('year')}-${get('month')}-${get('day')} ` +
		`${get('hour')}:${get('minute')}:${get('second')} ${get('timeZoneName')}`
	);
}

export function bucketLabel(d: Date, bucketSeconds: number, timeZone?: string): string {
	const options: Intl.DateTimeFormatOptions =
		bucketSeconds >= SECONDS_PER_DAY
			? { month: '2-digit', day: '2-digit' }
			: { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' };
	return new Intl.DateTimeFormat('en-CA', { timeZone, ...options }).format(d);
}

// timeZone undefined means the runtime's zone (the browser's).
export function zoneName(timeZone?: string, at: Date = new Date()): string {
	const parts = new Intl.DateTimeFormat('en-CA', { timeZone, timeZoneName: 'short' }).formatToParts(
		at
	);
	return parts.find((p) => p.type === 'timeZoneName')?.value ?? '';
}

export function formatCompact(d: Date, now: Date, timeZone?: string): string {
	const day = new Intl.DateTimeFormat('en-CA', {
		timeZone,
		year: 'numeric',
		month: '2-digit',
		day: '2-digit'
	});
	const time = new Intl.DateTimeFormat('en-CA', {
		timeZone,
		hour: '2-digit',
		minute: '2-digit',
		hourCycle: 'h23'
	}).format(d);
	const date = day.format(d);
	const prefix = date === day.format(now) ? '' : `${date} `;
	return `${prefix}${time} ${zoneName(timeZone, d)}`;
}
