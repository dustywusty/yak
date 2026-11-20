// Abby viewer: a view-only client of the Abby HTTP API, built only from
// spec/openapi.yaml, spec/schemas, spec/protocol.md and spec/p2-tasks.md.
// All user content is inserted as text nodes, never as HTML.
//
// Views: #/ and #/b/<board>?ch=&t=&m=&v= (term.js, the default chat view,
// with Activity = feed.js), #/boards, and the skins #/dispatch, #/wall,
// #/irc (skins.js). Board data is kept live by the store in skin-kit.js.

import { createFeed } from './feed.js';
import { createSkins } from './skins.js';
import { createTerm } from './term.js';
import { h } from './skin-kit.js';

const TOKEN_KEY = 'abby-viewer-token';
const UNAUTHORIZED = 'The server rejected the token (401). Paste a valid token.';
const $ = (id) => document.getElementById(id);
const S = { token: null, me: null, abort: new AbortController() };

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
  if (method !== 'GET' && !/\/snapshots(\/[^/]+)?$/.test(path)) {
    throw new Error(`viewer is read-only: refusing ${method} ${path}`);
  }
  const url = new URL(path, location.origin);
  for (const [k, v] of Object.entries(params)) {
    if (v == null || v === '') continue;
    for (const x of [].concat(v)) url.searchParams.append(k, x);
  }
  let res;
  try {
    res = await fetch(url, {
      method,
      headers: { Authorization: `Bearer ${S.token}`, Accept: 'application/json' },
      signal,
      cache: 'no-store',
    });
  } catch (e) {
    if (e.name === 'AbortError') throw e;
    throw new ApiError(0, { code: 'network', detail: 'network error' });
  }
  if (res.status === 204) return null;
  const body = await res.json().catch(() => ({}));
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
  const skin = /^\/(dispatch|wall|irc)$/.exec(path);
  if (skin) return { view: 'skin', skin: skin[1], board: q.get('b') || '' };
  const m = /^\/b\/([^/]+)$/.exec(path);
  if (!m) return { view: 'term', board: '', v: 'activity', key: 'activity' };
  const r = { view: 'term', board: decodeURIComponent(m[1]), ch: q.get('ch') || '', t: q.get('t') || '', m: q.get('m') || '', v: q.get('v') || '' };
  r.key = [r.board, r.ch, r.t, r.v].join('|');
  return r;
}

function href(r) {
  const q = new URLSearchParams();
  for (const k of ['ch', 't', 'm', 'v']) if (r[k]) q.set(k, r[k]);
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
  if (!v) return;
  S.token = v;
  try { sessionStorage.setItem(TOKEN_KEY, v); } catch { /* kept in memory only */ }
  S.me = null;
  $('login-error').hidden = true;
  await onRoute();
});

function forget() {
  needToken('Token forgotten.');
  history.replaceState(null, '', location.pathname);
}
$('forget').addEventListener('click', forget);

const feed = createFeed({ api, h, time, href, onTask: () => {}, onUnauthorized: () => needToken(UNAUTHORIZED) });
const skins = createSkins({ api, setStatus, onUnauthorized: () => needToken(UNAUTHORIZED) });
const term = createTerm({ api, href, setStatus, onUnauthorized: () => needToken(UNAUTHORIZED), onForget: forget, liveEl: $('live') });

window.addEventListener('hashchange', onRoute);
loadToken();
onRoute();
