// Yak viewer: a client of the Yak HTTP API, read-only by default, built only from
// spec/openapi.yaml, spec/schemas, spec/protocol.md and spec/p2-tasks.md.
// The read transport stays read-only; opt-in operator writes use a separate
// guarded transport under spec/operator-mode.md §4.
// All user content is inserted as text nodes, never as HTML.
//
// Views: #/ and #/b/<board>?ch=&t=&m=&v= (term.js, the default chat view,
// with Activity = feed.js), #/boards, and the skins #/board (also #/wall),
// #/forum (skins.js). Board data is kept live by the store in skin-kit.js.

import { parseWireJSON, stringifyWireJSON } from './wire-json.js';

import { createFeed } from './feed.js';
import { createSkins } from './skins.js';
import { createTerm } from './term.js';
import { OperatorSession, allowedRequest, credentialRefusal } from './operator.js';
import { createOperatorUI } from './operator-ui.js';
import { h } from './skin-kit.js';

const TOKEN_KEY = 'yak-viewer-token';
const UNAUTHORIZED = 'The server rejected the token (401). Paste a valid token.';
const $ = (id) => document.getElementById(id);
const S = { token: null, me: null, abort: new AbortController(), operatorEnabled: false, serverDate: NaN, serverMono: 0 };
let session, operatorUI;

// ---------- HTTP ----------

class ApiError extends Error {
  constructor(status, problem) {
    super(problem.detail || problem.code || `HTTP ${status}`);
    this.status = status;
    this.problem = problem;
  }
}

async function api(method, path, params = {}, signal = S.abort.signal) {
  // Read-only guard: only GET, plus snapshot sessions (which are reads).
  if (method !== 'GET' && !(method === 'POST' && /^\/v1\/boards\/[^/]+\/snapshots$/.test(path)) && !(method === 'DELETE' && /^\/v1\/boards\/[^/]+\/snapshots\/[^/]+$/.test(path))) {
    throw new Error(`viewer is read-only: refusing ${method} ${path}`);
  }
  if (session?.active && !allowedRequest(method, path, session.me.credential.id)) throw new Error('Operator read route refused.');
  const url = new URL(path, location.origin);
  for (const [k, v] of Object.entries(params)) {
    if (v == null || v === '') continue;
    for (const x of [].concat(v)) url.searchParams.append(k, x);
  }
  let res;
  const started = performance.now();
  try {
    res = await fetch(url, {
      method,
      headers: { Authorization: `Bearer ${S.token}`, Accept: 'application/json' },
      signal,
      cache: 'no-store',
      credentials: 'omit',
    });
  } catch (e) {
    if (e.name === 'AbortError') throw e;
    throw new ApiError(0, { code: 'network', detail: 'network error' });
  }
  if (signal.aborted) throw new DOMException('Request canceled', 'AbortError');
  if (path === '/v1/server') { S.serverDate = Date.parse(res.headers.get('Date')); S.serverMono = started; }
  if (res.status === 204) return null;
  const body = await res.text().then(parseWireJSON).catch((e) => { if (res.ok) throw e; return {}; });
  // Cancellation can outlive fetch while the response body is being read.
  // An old 401 must not forget the token for a replacement view.
  if (signal.aborted) throw new DOMException('Request canceled', 'AbortError');
  if (!res.ok) {
    const err = new ApiError(res.status, body);
    if (res.status === 401) needToken(UNAUTHORIZED);
    throw err;
  }
  return body;
}

// ---------- token ----------

function loadToken() {
  try { S.token = sessionStorage.getItem(TOKEN_KEY); } catch { S.token = null; }
}

function needToken(message) {
  if (session?.ending) return;
  if (session?.active) { session.end({ unauthorized: true }).finally(() => needToken(message)); return; }
  operatorUI?.clear();
  document.body.removeAttribute('data-operator');
  feed.stop();
  skins.unmount();
  term.unmount();
  S.abort.abort();
  S.abort = new AbortController();
  S.token = null; S.me = null;
  $('board-list').replaceChildren();
  try { sessionStorage.removeItem(TOKEN_KEY); } catch { /* storage blocked */ }
  showLogin(message);
}

function showLogin(message) {
  show('login');
  renderTop();
  $('login-error').hidden = !message;
  $('login-error').textContent = message || '';
  $('token').focus();
}

// ---------- routing ----------

function parseRoute() {
  const [path, query = ''] = location.hash.slice(1).split('?');
  const q = new URLSearchParams(query);
  if (path === '/boards') return { view: 'boards' };
  if (path === '/dispatch') {
    history.replaceState(null, '', q.get('b') ? `#/b/${encodeURIComponent(q.get('b'))}` : '#/');
    return parseRoute();
  }
  if (path === '/irc') {
    history.replaceState(null, '', '#/forum' + (query ? '?' + query : ''));
    return parseRoute();
  }
  const skin = /^\/(board|wall|forum)$/.exec(path);
  if (skin) return { view: 'skin', skin: skin[1] === 'wall' ? 'board' : skin[1], board: q.get('b') || '', ch: q.get('ch') || '', agent: q.get('agent') || '', label: q.get('label') || '', t: q.get('t') || '' };
  const m = /^\/b\/([^/]+)$/.exec(path);
  if (!m) return { view: 'term', board: '', v: 'activity', key: 'activity' };
  // sub= and label= keep the meaning they had in the first board view:
  // sub=0 is the exact channel (default: the channel and its subchannels),
  // label= is a root-label filter (`key` or `key:value`, space-separated).
  const r = { view: 'term', board: decodeURIComponent(m[1]), ch: q.get('ch') || '', t: q.get('t') || '', m: q.get('m') || '',
    v: q.get('v') || '', sub: q.get('sub') !== '0', label: q.get('label') || '' };
  r.key = [r.board, r.ch, r.t, r.v, r.sub, r.label].join('|');
  return r;
}

function href(r) {
  const q = new URLSearchParams();
  for (const k of ['ch', 't', 'm', 'v', 'label']) if (r[k]) q.set(k, r[k]);
  if (r.sub === false) q.set('sub', '0');
  const qs = q.toString();
  return `#/b/${encodeURIComponent(r.board)}${qs ? `?${qs}` : ''}`;
}

async function onRoute() {
  if (!S.token) return showLogin();
  try {
    if (!S.me) { S.me = await api('GET', '/v1/me'); renderTop(); }
    feed.start();
    const r = parseRoute();
    if (r.view !== 'skin') skins.unmount();
    if (r.view !== 'term') term.unmount();
    feed.setVisible(r.view === 'term' && r.v === 'activity');
    show(r.view);
    if (r.view === 'term') return await term.mount(r);
    if (r.view === 'skin') return await skins.mount(r);
    const { boards } = await api('GET', '/v1/boards');
    $('board-list').replaceChildren(...(boards.length ? boards.map((b) => h('li', {},
      h('a', { href: href({ board: b.id }) }, h('strong', {}, b.name)),
      h('span', { class: 'muted' }, b.caller_is_board_admin ? ' board admin' : ''),
      h('div', { class: 'muted small' }, `created ${new Date(b.created_at).toLocaleString()}`))) : [h('li', { class: 'muted' }, 'No visible boards.')]));
  } catch (e) {
    if (e.name === 'AbortError' || e.status === 401) return;
    banner(e.status === 404 ? 'Board not found or not visible.' : `Error: ${e.message}`);
    if (e.status === 404) location.hash = '#/boards';
  }
}

// ---------- chrome ----------

function show(id) {
  for (const s of ['login', 'boards', 'skin', 'term']) $(s).hidden = s !== id;
  document.body.dataset.view = id;
  $('nav').hidden = id === 'login';
  for (const a of $('nav').children) a.classList.toggle('sel', a.dataset.view === id);
}

function renderTop() {
  $('me').replaceChildren(S.me && S.token
    ? h('span', { title: `scopes: ${S.me.credential.scopes.join(', ')}` }, `@${S.me.actor.handle}`) : '');
  $('forget').hidden = !S.token;
  $('forget').textContent = session?.active ? 'Logout (revokes token everywhere)' : 'Forget token';
}

function setStatus(state) {
  const el = $('status');
  el.hidden = !state;
  el.dataset.state = state || '';
  el.textContent = state || '';
}

let bannerTimer;
function banner(text) {
  $('banner').textContent = text;
  $('banner').hidden = false;
  clearTimeout(bannerTimer);
  bannerTimer = setTimeout(() => { $('banner').hidden = true; }, 8000);
}

const time = (ts) => h('time', { datetime: ts, title: `${ts} (UTC)` }, new Date(ts).toLocaleString());

// ---------- wiring ----------

$('login-form').addEventListener('submit', async (ev) => {
  ev.preventDefault();
  const v = $('token').value.trim();
  $('token').value = '';
  if (!v || session?.ending) return;
  $('connect').disabled = true;
  S.token = v; S.me = null;
  try {
    const [me, server] = await Promise.all([api('GET', '/v1/me'), api('GET', '/v1/server')]);
    if ($('operator-login').checked && S.operatorEnabled) {
      const why = credentialRefusal(me, server, S.serverDate);
      if (why) throw new Error(why);
      try { sessionStorage.removeItem(TOKEN_KEY); } catch { /* unavailable */ }
      S.me = me; session.start(me, server, S.serverDate, S.serverMono);
      document.body.dataset.operator = 'true';
      if (!server.capabilities?.includes('yak.credentials.restrictions')) banner("Warning: a stolen token could issue and revoke your credentials. This server has no credential restrictions.");
    } else {
      if (!me.credential.scopes.every((s) => s === 'read')) throw new Error('A write credential requires operator mode. Use a read-only credential here.');
      S.me = me;
      try { sessionStorage.setItem(TOKEN_KEY, v); } catch { /* memory only */ }
    }
    renderTop();
    await onRoute();
  } catch (e) { needToken(e.status === 401 ? UNAUTHORIZED : e.message); }
  finally { $('connect').disabled = false; }
});

function forget() {
  if (session?.active) { session.end().finally(() => needToken(session.notice || 'Logged out; token revoked everywhere.')); return; }
  needToken('Token forgotten.');
  history.replaceState(null, '', location.pathname);
}
$('forget').addEventListener('click', forget);

const feed = createFeed({ api, h, time, href, onTask: () => {}, onUnauthorized: () => needToken(UNAUTHORIZED), onError: banner });
const skins = createSkins({ api, setStatus, onUnauthorized: () => needToken(UNAUTHORIZED) });
const term = createTerm({ api, href, setStatus, onUnauthorized: () => needToken(UNAUTHORIZED), onForget: forget, liveEl: $('live') });

window.addEventListener('hashchange', onRoute);
// Raw operator transport is separate from the viewer's read transport.
// It always passes through the same exact allowlist and owns no stored token.
async function write(method, path, body, headers = {}, options = {}) {
  if (!S.operatorEnabled || !session.active || !allowedRequest(method, path, session.me.credential.id)) throw new Error('Operator route refused.');
  if (method === 'PATCH' && /\/tasks\/[^/]+$/.test(path) && (!body || Object.keys(body).length !== 1 || typeof body.direct_action_required !== 'boolean')) throw new Error('Operator task patches may only change direct_action_required.');
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 10000);
  try {
    const requestMono = performance.now();
    const res = await fetch(path, { method, headers: { ...headers, Authorization: `Bearer ${S.token}`, Accept: 'application/json', ...(body != null ? { 'Content-Type': 'application/json' } : {}) }, body: body == null ? undefined : stringifyWireJSON(body), credentials: 'omit', cache: 'no-store', signal: controller.signal, ...options });
    if (!res.ok) {
      // A known HTTP rejection stays known even if its problem body is lost.
      const problem = await res.text().then(parseWireJSON).catch(() => null);
      throw new ApiError(res.status, problem && typeof problem === 'object' ? problem : { code: 'http_error', detail: `HTTP ${res.status}` });
    }
    // Successful JSON bodies are part of the write result. Losing one leaves
    // the outcome uncertain, so perform() must retain this intent's receipt.
    const result = res.status === 204 ? null : await res.text().then(parseWireJSON);
    if (path.endsWith('/renew') && result && typeof result === 'object') {
      Object.defineProperty(result, '__responseTiming', { value: { serverDate: Date.parse(res.headers?.get('Date')), requestMono } });
    }
    return result;
  } catch (e) {
    if (e instanceof ApiError) throw e;
    throw new ApiError(0, { code: 'network', detail: 'Request outcome unknown. Retry this same intent.' });
  } finally { clearTimeout(timeout); }
}
session = new OperatorSession({ send: write, changed: () => {
  operatorUI?.update();
  const el = $('operator-session'); el.hidden = !session.active;
  if (session.active) {
    const c = session.me.credential;
    $('operator-identity').textContent = `@${session.me.actor.handle} (${session.me.actor.kind}) · boards ${c.board_ids.join(', ')} · scopes ${c.scopes.join(', ')} · expires in ${Math.max(0, Math.ceil(session.lifeLeft() / 60000))} min · idle ${Math.max(0, Math.ceil(session.idleLeft() / 1000))} s${session.idleLeft() <= 60000 ? ' · idle logout in less than 60 seconds' : ''}${session.notice ? ` · ${session.notice}` : ''}`;
  } else if (S.me && document.body.dataset.operator) needToken(session.notice || 'Operator session ended.');
} });
operatorUI = createOperatorUI({ session, read: api, route: parseRoute });
$('operator-tools').addEventListener('click', () => operatorUI.open());
$('operator-logout').addEventListener('click', forget);
for (const event of ['keydown', 'pointerdown', 'touchstart']) document.addEventListener(event, () => session.input(), { passive: true, capture: true });
// end's keepalive path dispatches synchronously before clearing the token.
window.addEventListener('pagehide', () => { if (session.active) session.end({ keepalive: true }); });
window.addEventListener('yak-operator-open', (e) => operatorUI.open(e.detail || parseRoute()));
async function boot() {
  try { S.operatorEnabled = (await (await fetch('/viewer-config', { cache: 'no-store' })).json()).operator === true; } catch { /* default read-only */ }
  $('operator-option').hidden = !S.operatorEnabled;
  loadToken();
  // Only credentials validated as read-only may be restored from older viewers.
  if (S.token) {
    try { const me = await api('GET', '/v1/me'); if (!me.credential.scopes.every((s) => s === 'read')) return needToken('Stored token was not read-only. Paste a short-lived operator token to enter operator mode.'); S.me = me; renderTop(); }
    catch { return needToken(UNAUTHORIZED); }
  }
  onRoute();
}
boot();
