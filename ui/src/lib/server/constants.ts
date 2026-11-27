export const SESSION_COOKIE = 'commsgun_session';
// Twin of the API's SessionTokenBytes (32 bytes, base64url unpadded).
export const SESSION_TOKEN = /^[A-Za-z0-9_-]{43}$/;
export const SESSION_HEADER = 'commsgun-session';
export const LOGIN_MAX_ATTEMPTS = 5;
export const LOGIN_WINDOW_MS = 60 * 1000;
export const ADMIN_USERNAME = 'admin';
export const LOGIN_LIMITER_MAX_KEYS = 10_000;
export const MIN_ADMIN_PASSWORD = 16;
export const MIN_SESSION_SECRET = 32;
export const MIN_ADMIN_API_TOKEN = 32;
export const IMPORT_RETRY_MS = 1000;
