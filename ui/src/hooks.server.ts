import type { ServerInit } from '@sveltejs/kit/hooks';
import { getEnv } from './lib/server/env';

export const init: ServerInit = () => {
	getEnv();
};
