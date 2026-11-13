import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { OUTCOMES as STACKED } from './components/OutcomeChart.svelte';

const OUTCOMES = ['delivered', 'sent', 'bounced', 'complained', 'failed', 'expired'] as const;
const css = readFileSync(new URL('../routes/layout.css', import.meta.url), 'utf8');
const darkStart = css.indexOf('@media (prefers-color-scheme: dark) {');
const SCHEMES = { light: css.slice(0, darkStart), dark: css.slice(darkStart) };

function token(block: string, name: string): [number, number, number] {
	const m = block.match(new RegExp(`--${name}:\\s*oklch\\(([\\d.]+) ([\\d.]+) ([\\d.]+)\\)`));
	if (!m) throw new Error(`--${name} not found`);
	return [Number(m[1]), Number(m[2]), Number(m[3])];
}

function luminance([L, C, h]: [number, number, number]): number {
	const a = C * Math.cos((h * Math.PI) / 180);
	const b = C * Math.sin((h * Math.PI) / 180);
	const l = (L + 0.3963377774 * a + 0.2158037573 * b) ** 3;
	const m = (L - 0.1055613458 * a - 0.0638541728 * b) ** 3;
	const s = (L - 0.0894841775 * a - 1.291485548 * b) ** 3;
	const clip = (v: number) => Math.min(1, Math.max(0, v));
	const r = clip(4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s);
	const g = clip(-1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s);
	const bl = clip(-0.0041960863 * l - 0.7034186147 * m + 1.707614701 * s);
	return 0.2126 * r + 0.7152 * g + 0.0722 * bl;
}

function contrast(x: number, y: number): number {
	return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05);
}

describe.each(Object.entries(SCHEMES))('outcome colours, %s scheme', (_, block) => {
	const card = luminance(token(block, 'card'));
	const lum = OUTCOMES.map((o) => luminance(token(block, `outcome-${o}`)));

	it.each(OUTCOMES.map((o, i) => [o, i] as const))(
		'%s has at least 3:1 against the card',
		(_o, i) => {
			expect(contrast(lum[i], card)).toBeGreaterThanOrEqual(3);
		}
	);

	it('neighbouring stacked outcomes differ in lightness, not only in hue', () => {
		const stacked = STACKED.map((o) => luminance(token(block, `outcome-${o}`)));
		for (let i = 1; i < stacked.length; i++) {
			expect(contrast(stacked[i], stacked[i - 1])).toBeGreaterThanOrEqual(1.2);
		}
	});
});
