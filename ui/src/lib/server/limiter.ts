import { LoginLimiter } from './session.js';

// One budget for every route that takes a password (spec 12.3).
export const passwordLimiter = new LoginLimiter();
