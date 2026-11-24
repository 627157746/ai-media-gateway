// Fetch helpers: admin API (Bearer ADMIN_TOKEN) and gateway API (the same
// admin token — /v1/* accepts it, so the 试用 page needs no gateway key).

const ADMIN_KEY = 'mgw_admin_token';
localStorage.removeItem('mgw_gateway_key'); // stale key from the sk-mg-* era

export function getToken() {
  return sessionStorage.getItem(ADMIN_KEY) || localStorage.getItem(ADMIN_KEY) || '';
}

export function saveToken(token, remember) {
  clearToken();
  (remember ? localStorage : sessionStorage).setItem(ADMIN_KEY, token);
}

export function clearToken() {
  sessionStorage.removeItem(ADMIN_KEY);
  localStorage.removeItem(ADMIN_KEY);
}

// ApiError carries the OpenAI-shaped error body when present.
export class ApiError extends Error {
  constructor(status, body) {
    const msg = body && body.error && body.error.message ? body.error.message : `HTTP ${status}`;
    super(msg);
    this.status = status;
    this.body = body;
  }
}

async function request(path, opts = {}, token = '', isAdmin = false) {
  const headers = Object.assign({}, opts.headers);
  if (token) headers.Authorization = `Bearer ${token}`;
  if (opts.json !== undefined) {
    headers['Content-Type'] = 'application/json';
    opts = Object.assign({}, opts, { body: JSON.stringify(opts.json) });
    delete opts.json;
  }
  const resp = await fetch(path, Object.assign({}, opts, { headers }));
  const ct = resp.headers.get('Content-Type') || '';
  const body = ct.includes('json') ? await resp.json() : await resp.text();
  if (!resp.ok) {
    // stored admin token rejected → drop session and return to login
    if (resp.status === 401 && isAdmin) {
      clearToken();
      location.reload();
    }
    throw new ApiError(resp.status, body);
  }
  return body;
}

export function admin(path, opts = {}) { return request(path, opts, getToken(), true); }
export function v1(path, opts = {}) { return request(path, opts, getToken()); }

// Validate an admin token without storing it.
export async function validateToken(token) {
  await request('/admin/status', {}, token);
}
