// Abby viewer: a view-only client of the Abby HTTP API, built only from
// spec/openapi.yaml, spec/schemas and spec/protocol.md. All user content is
// inserted as text nodes, never as HTML.

import { createFeed } from './feed.js';

const TOKEN_KEY = 'abby-viewer-token';
const WAIT_MS = 25000;     // EVT-011: long-poll budget, below limits.max_wait_ms
const PAGE = 200;          // PAGE-001: limits.max_page_size in the reference discovery

const $ = (id) => document.getElementById(id);
const S = {
  token: null, me: null, board: null, channels: [], actors: new Map(),
  threads: new Map(),      // thread id -> Thread
  msgs: new Map(),         // thread id -> Map(message id -> Message)
  tasks: new Map(),        // task id -> Task (P2), newest revision wins
  list: [],                // thread ids from GET /threads for the current filter
  seen: new Set(),         // EVT-003: event ids already applied
  cursor: null, gen: 0, abort: new AbortController(), sel: {}, listGen: 0,
};

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
    if (res.status === 401) needToken('The server rejected the token (401). Paste a valid token.');
    throw err;
  }
  return body;
}

// PAGE-001: follow next_page_token until it is null.
async function all(path, key, params = {}) {
  const out = [];
  let token = null;
  do {
    const page = await api('GET', path, { ...params, limit: PAGE, page_token: token });
    out.push(...page[key]);
    token = page.next_page_token;
  } while (token);
  return out;
}

const enc = encodeURIComponent;
const boardPath = () => `/v1/boards/${enc(S.board.id)}`;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// ---------- token ----------

function loadToken() {
  try { S.token = sessionStorage.getItem(TOKEN_KEY); } catch { S.token = null; }
}

function needToken(message) {
  stopLive();
  feed.stop();
  S.token = null; S.me = null; S.board = null;
  for (const id of ['board-list', 'channel-list', 'thread-list', 'thread-view']) $(id).replaceChildren();
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

// ---------- snapshot + live events (SNAP-001..003, EVT-002..011) ----------

function stopLive() {
  S.gen++;
  S.abort.abort();
  S.abort = new AbortController();
  setStatus(null);
}

// Take a whole-board snapshot (no filter), page it, keep the
// continuation_cursor, and discard the session. GET /events then uses the
// same (empty) filter, as EVT-009 binds a cursor to its filter.
async function snapshot(gen) {
  const B = boardPath();
  const [channels, actors] = await Promise.all([
    api('GET', `${B}/channels`),
    api('GET', `${B}/actors`).catch(() => ({ actors: [] })),
  ]);
  let info;
  try {
    info = await api('POST', `${B}/snapshots`);
  } catch (e) {
    if (e.status !== 413) throw e;
    // SNAP-004: too many items. Follow from the head; views load by GET.
    banner('Board too large for one snapshot; following new events only.');
    const head = await api('GET', `${B}/events`, { from: 'latest', limit: 1 });
    info = null;
    S.cursor = head.next_cursor;
  }
  const threads = new Map(), msgs = new Map();
  if (info) {
    let token = null, cursor = null;
    try {
      do {
        const page = await api('GET', `${B}/snapshots/${enc(info.id)}`, { limit: PAGE, page_token: token });
        for (const it of page.items) {
          if (it.type === 'thread') threads.set(it.id, it.data);
          else if (it.type === 'message') bucket(msgs, it.data.thread_id).set(it.id, it.data);
          else if (it.type === 'task') noteTask(it.data); // TASK-092
        }
        token = page.next_page_token;
        cursor = page.continuation_cursor;
      } while (token);
    } finally {
      api('DELETE', `${B}/snapshots/${enc(info.id)}`).catch(() => {});
    }
    S.cursor = cursor;
  }
  if (gen !== S.gen) return;
  S.channels = channels.channels;
  S.actors = new Map(actors.actors.map((a) => [a.id, a]));
  S.threads = threads;
  S.msgs = msgs;
  S.seen.clear();
  await refreshView();
}

async function live(gen) {
  let backoff = 1000;
  S.cursor = null;
  while (gen === S.gen) {
    try {
      if (!S.cursor) {
        setStatus('resyncing');
        await snapshot(gen);
        if (gen !== S.gen) return;
        setStatus('live');
        backoff = 1000;
      }
      // After an error, probe with wait_ms=0 so the status turns live at once.
      const wait = backoff > 1000 ? 0 : WAIT_MS;
      const page = await api('GET', `${boardPath()}/events`, { cursor: S.cursor, wait_ms: wait, limit: PAGE });
      if (gen !== S.gen) return;
      let changed = false;
      for (const ev of page.events) changed = apply(ev) || changed;
      S.cursor = page.next_cursor; // EVT-008: advances even with no matches
      setStatus('live');
      backoff = 1000;
      if (changed) render();
    } catch (e) {
      if (gen !== S.gen || e.name === 'AbortError' || e.status === 401) return;
      if (handleLiveError(e)) continue;
      setStatus('reconnecting');
      await sleep(backoff + Math.random() * 500); // keep the cursor
      backoff = Math.min(backoff * 2, 30000);
    }
  }
}

// Returns true when the error needs a fresh snapshot (cursor cleared).
function handleLiveError(e) {
  const code = e.problem?.code;
  if (code === 'resync_required' || code === 'cursor_expired' || code === 'snapshot_expired') {
    // EVT-009 / SNAP-003: rebuild from a fresh snapshot, then replay.
    banner(`Resyncing (${code}${e.problem.reason ? `: ${e.problem.reason}` : ''}).`);
    S.cursor = null;
    return true;
  }
  if (e.status && e.status !== 502 && e.status !== 503) banner(`Error ${e.status}: ${e.message}`);
  if (e.status === 400) S.cursor = null; // cursor no longer accepted: re-snapshot after the backoff
  return false;
}

function bucket(map, threadId) {
  if (!map.has(threadId)) map.set(threadId, new Map());
  return map.get(threadId);
}

// Apply one event to the local view. Returns true if the view changed.
function apply(ev) {
  if (S.seen.has(ev.id)) return false;
  S.seen.add(ev.id);
  if (S.seen.size > 5000) S.seen.delete(S.seen.values().next().value);
  const p = ev.payload || {};
  switch (ev.type) {
    case 'channel.created':
      // caller_capabilities is always [] inside events: refetch the list.
      api('GET', `${boardPath()}/channels`).then((r) => { S.channels = r.channels; renderChannels(); }).catch(() => {});
      return false;
    case 'thread.created':
      putMessage(p.message);
      putThread(p.thread);
      return true;
    case 'message.created': {
      putMessage(p.message);
      const t = S.threads.get(p.message.thread_id);
      if (t) {
        S.threads.set(t.id, { ...t, message_count: Math.max(t.message_count, p.message.seq),
          last_message_id: p.message.id, updated_at: p.message.created_at });
      }
      return true;
    }
    case 'message.edited':
    case 'message.deleted':
    case 'message.labels_changed':
      putMessage(p.message);
      return true;
    case 'thread.updated':
      putThread(p.thread);
      return true;
    case 'thread.view_invalidated':
      putThread(p.thread);
      refetchThread(p.thread.id);
      return true;
    default:
      if (ev.type.startsWith('task.') && p.task) { noteTask(p.task); return true; } // P2 task events
      return false; // forward compatibility: ignore unknown types
  }
}

// Apply task views by revision (TASK-092); a renewal keeps the revision but moves the expiry.
function noteTask(t) {
  const cur = S.tasks.get(t.id);
  if (!cur || t.revision >= cur.revision) S.tasks.set(t.id, t);
  if (S.board && t.thread_id === S.sel.t) renderThread();
}

function putMessage(m) {
  bucket(S.msgs, m.thread_id).set(m.id, m);
  const t = S.threads.get(m.thread_id);
  if (m.is_root && t) S.threads.set(t.id, { ...t, labels: m.labels });
}

function putThread(t) {
  S.threads.set(t.id, t);
  const inList = S.list.includes(t.id);
  if (matches(t) && !inList) S.list.push(t.id);
  else if (!matches(t) && inList) S.list = S.list.filter((id) => id !== t.id);
}

async function refetchThread(id) {
  try {
    const r = await api('GET', `${boardPath()}/threads/${enc(id)}`);
    S.threads.set(id, r.thread);
    if (S.sel.t === id) await loadThread(id);
    else render();
  } catch { /* the next event or resync repairs the view */ }
}

// ---------- filters (thread-level subset, EVT-005 / LBL-003) ----------

const labelClauses = () => (S.sel.label || '').split(/[\s,]+/).filter(Boolean);

function matches(t) {
  const ch = S.sel.ch;
  if (ch && !(t.channel_path === ch || (S.sel.sub && t.channel_path.startsWith(`${ch}/`)))) return false;
  const byKey = new Map();
  for (const c of labelClauses()) {
    const i = c.indexOf(':');
    const k = (i < 0 ? c : c.slice(0, i)).toLowerCase();
    if (!byKey.has(k)) byKey.set(k, []);
    byKey.get(k).push(i < 0 ? null : c.slice(i + 1));
  }
  for (const [k, vals] of byKey) {
    if (!Object.hasOwn(t.labels, k)) return false;
    if (!vals.some((v) => v === null || t.labels[k] === v)) return false;
  }
  return true;
}

async function loadList() {
  const n = ++S.listGen;
  const params = { label: labelClauses() };
  if (S.sel.ch) params[S.sel.sub ? 'subtree' : 'channel'] = S.sel.ch;
  try {
    const threads = await all(`${boardPath()}/threads`, 'threads', params);
    if (n !== S.listGen) return;
    for (const t of threads) S.threads.set(t.id, t);
    S.list = threads.map((t) => t.id);
    S.listError = null;
  } catch (e) {
    if (e.name === 'AbortError' || n !== S.listGen) return;
    S.list = [];
    S.listError = e.message;
  }
  renderThreads();
}

async function loadThread(id) {
  const B = boardPath();
  try {
    const [r, messages] = await Promise.all([
      api('GET', `${B}/threads/${enc(id)}`),
      all(`${B}/threads/${enc(id)}/messages`, 'messages'),
    ]);
    S.threads.set(id, r.thread);
    S.msgs.set(id, new Map(messages.map((m) => [m.id, m])));
    S.threadError = null;
    if (r.thread.task_id) { // TASK-024: a task thread; show its task view
      const task = await api('GET', `${B}/tasks/${enc(r.thread.task_id)}`).catch(() => null);
      if (task) noteTask(task);
    }
  } catch (e) {
    if (e.name === 'AbortError') return;
    S.threadError = e.status === 404 ? 'Thread not found or not visible.' : e.message;
  }
  renderThread();
  if (S.sel.m && id === S.sel.t) {
    document.getElementById(`m-${S.sel.m}`)?.scrollIntoView({ block: 'start' });
  }
}

async function refreshView() {
  await Promise.all([loadList(), S.sel.t ? loadThread(S.sel.t) : null]);
  render();
}

// ---------- routing: #/ (live), #/boards, #/b/<board>?ch=&sub=0|1&label=&t=<thread>&m=<message> ----------

function parseRoute() {
  const [path, query = ''] = location.hash.slice(1).split('?');
  if (path === '/boards') return { view: 'boards' };
  const m = /^\/b\/([^/]+)$/.exec(path);
  if (!m) return { view: 'live' };
  const q = new URLSearchParams(query);
  return { view: 'board', board: decodeURIComponent(m[1]), ch: q.get('ch') || '', sub: q.get('sub') !== '0',
    label: q.get('label') || '', t: q.get('t') || '', m: q.get('m') || '' };
}

function href(r) {
  const q = new URLSearchParams();
  if (r.ch) q.set('ch', r.ch);
  if (!r.sub) q.set('sub', '0');
  if (r.label) q.set('label', r.label);
  if (r.t) q.set('t', r.t);
  if (r.m) q.set('m', r.m);
  const qs = q.toString();
  return `#/b/${enc(r.board)}${qs ? `?${qs}` : ''}`;
}

async function onRoute() {
  if (!S.token) return showLogin();
  try {
    if (!S.me) { S.me = await api('GET', '/v1/me'); renderTop(); }
    feed.start();
    const r = parseRoute();
    feed.setVisible(r.view === 'live');
    if (r.view !== 'board') { stopLive(); S.board = null; show(r.view); renderTop(); }
    if (r.view === 'live') return;
    if (r.view === 'boards') {
      const { boards } = await api('GET', '/v1/boards');
      renderBoards(boards);
      return;
    }
    const prev = S.sel;
    S.sel = r;
    $('subtree').checked = r.sub;
    if ($('label-filter').value !== r.label) $('label-filter').value = r.label;
    show('board');
    if (!S.board || S.board.id !== r.board) {
      stopLive();
      S.board = await api('GET', `/v1/boards/${enc(r.board)}`);
      S.list = []; S.threads = new Map(); S.msgs = new Map(); S.tasks = new Map();
      renderTop(); render();
      live(S.gen);
      return;
    }
    if (prev.ch !== r.ch || prev.sub !== r.sub || prev.label !== r.label) loadList();
    if (r.t && (prev.t !== r.t || prev.m !== r.m)) loadThread(r.t);
    render();
  } catch (e) {
    if (e.name === 'AbortError' || e.status === 401) return;
    banner(e.status === 404 ? 'Board not found or not visible.' : `Error: ${e.message}`);
    if (e.status === 404) location.hash = '#/boards';
  }
}

// ---------- rendering (text only) ----------

function h(tag, attrs = {}, ...kids) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v == null || v === false) continue;
    if (k === 'class') e.className = v;
    else if (k.startsWith('--')) e.style.setProperty(k, v); // CSSOM: allowed by the CSP
    else if (k.startsWith('on')) e.addEventListener(k.slice(2), v);
    else e.setAttribute(k, v === true ? '' : v);
  }
  for (const k of kids.flat()) if (k != null && k !== false) e.append(k); // strings become text nodes
  return e;
}

const fmt = (ts) => (ts ? new Date(ts).toLocaleString() : '');
const time = (ts) => h('time', { datetime: ts, title: `${ts} (UTC)` }, fmt(ts));
const actor = (id) => {
  const a = S.actors.get(id);
  return h('span', { class: 'actor', title: a ? `${a.display_name} (${a.kind}) ${id}` : id },
    a ? `@${a.handle}` : id);
};
const chips = (labels) => h('span', { class: 'chips' },
  Object.entries(labels || {}).map(([k, v]) => h('span', { class: 'chip' }, v === '' ? k : `${k}: ${v}`)));

function show(id) {
  for (const s of ['login', 'live', 'boards', 'board']) $(s).hidden = s !== id;
  $('nav').hidden = id === 'login';
  for (const a of $('nav').children) a.classList.toggle('sel', a.dataset.view === id);
}

function renderTop() {
  $('me').replaceChildren(S.me && S.token
    ? h('span', { title: `scopes: ${S.me.credential.scopes.join(', ')}` }, `@${S.me.actor.handle}`) : '');
  $('forget').hidden = !S.token;
  $('board-name').textContent = S.board ? `/ ${S.board.name}` : '';
}

function renderBoards(boards) {
  $('board-list').replaceChildren(...(boards.length ? boards.map((b) => h('li', {},
    h('a', { href: href({ board: b.id, sub: true }) }, h('strong', {}, b.name)),
    h('span', { class: 'muted' }, b.caller_is_board_admin ? ' board admin' : ''),
    h('div', { class: 'muted small' }, `created ${fmt(b.created_at)}`))) : [h('li', { class: 'muted' }, 'No visible boards.')]));
}

function renderChannels() {
  const paths = new Set(S.channels.map((c) => c.path));
  const base = { ...S.sel, t: '' };
  const items = [h('li', { class: S.sel.ch ? '' : 'sel' }, h('a', { href: href({ ...base, ch: '' }) }, 'All channels'))];
  for (const c of S.channels) {
    const segs = c.path.split('/');
    const parent = segs.slice(0, -1).join('/');
    const name = parent && paths.has(parent) ? segs.at(-1) : c.path;
    items.push(h('li', { class: S.sel.ch === c.path ? 'sel' : '', '--depth': segs.length - 1 },
      h('a', { href: href({ ...base, ch: c.path }), title: c.path }, `#${name}`),
      c.access_mode === 'private' ? h('span', { class: 'badge' }, 'private') : null,
      h('span', { class: 'caps' }, c.caller_capabilities.join(' · ') || 'board admin view')));
  }
  $('channel-list').replaceChildren(...items);
}

function renderThreads() {
  const where = S.sel.ch ? `#${S.sel.ch}${S.sel.sub ? ' and below' : ''}` : 'all channels';
  $('threads-title').textContent = `Threads in ${where}`;
  // S.list keeps server creation order (PAGE-001); live threads append in commit order.
  const ts = S.list.map((id) => S.threads.get(id)).filter(Boolean);
  const items = ts.map((t) => h('li', { class: S.sel.t === t.id ? 'sel' : '' },
    h('a', { href: href({ ...S.sel, t: t.id }) },
      h('div', { class: 'title' }, t.title),
      h('div', { class: 'meta' }, `#${t.channel_path} · ${t.message_count} msg · `, time(t.updated_at)),
      chips(t.labels))));
  if (!items.length) items.push(h('li', { class: 'muted' }, S.listError ? `Error: ${S.listError}` : 'No threads.'));
  $('thread-list').replaceChildren(...items);
}

function renderThread() {
  const view = $('thread-view');
  $('board').toggleAttribute('data-open', !!S.sel.t);
  $('back').setAttribute('href', href({ ...S.sel, t: '' }));
  const t = S.threads.get(S.sel.t);
  if (!S.sel.t || (!t && !S.threadError)) {
    view.replaceChildren(h('p', { class: 'muted' }, S.sel.t ? 'Loading…' : 'Select a thread.'));
    return;
  }
  if (S.threadError) { view.replaceChildren(h('p', { class: 'error' }, S.threadError)); return; }
  const scroll = view.parentElement.scrollTop;
  const msgs = [...(S.msgs.get(t.id)?.values() || [])].sort((a, b) => a.seq - b.seq);
  const byId = new Map(msgs.map((m) => [m.id, m]));
  view.replaceChildren(
    h('h1', {}, t.title),
    h('div', { class: 'meta' }, `#${t.channel_path} · started by `, actor(t.created_by_actor_id), ' · ', time(t.created_at)),
    chips(t.labels),
    taskBox(t.task_id && S.tasks.get(t.task_id)),
    ...msgs.map((m) => message(m, byId)));
  view.parentElement.scrollTop = scroll;
}

// P2 task header: state, assignee, current claim and expiry, epoch (TASK-021, TASK-023).
function taskBox(task) {
  if (!task) return null;
  const c = task.current_claim;
  const lapsed = c && Date.parse(c.expires_at) < Date.now();
  return h('div', { class: 'task' },
    h('span', { class: `state state-${task.state}` }, task.state),
    ' assignee ', task.assignee_actor_id ? actor(task.assignee_actor_id) : 'none',
    ` · epoch ${task.epoch} · retries ${task.retry_count}`,
    c ? h('div', {}, 'claimed by ', actor(c.actor_id), ' until ', time(c.expires_at), lapsed ? ' (lapsed)' : '') : null,
    task.block ? h('div', {}, `blocked: ${task.block.reason ?? ''}`) : null,
    task.outcome ? h('div', {}, `${task.outcome.kind}: ${task.outcome.summary ?? task.outcome.reason ?? ''}`) : null);
}

function message(m, byId) {
  const parent = m.reply_to_message_id && byId.get(m.reply_to_message_id);
  const cls = `msg${m.deleted ? ' deleted' : ''}${m.id === S.sel.m ? ' target' : ''}`;
  return h('article', { class: cls, id: `m-${m.id}` },
    h('header', {}, actor(m.author_actor_id), ' ', time(m.created_at), h('span', { class: 'muted' }, ` #${m.seq}`),
      m.edited_at && !m.deleted ? h('span', { class: 'edited', title: `edited ${fmt(m.edited_at)}` }, ' (edited)') : null),
    m.reply_to_message_id ? h('button', { type: 'button', class: 'reply-link', onclick: () =>
      document.getElementById(`m-${m.reply_to_message_id}`)?.scrollIntoView({ behavior: 'smooth', block: 'start' }) },
      `↪ reply to #${parent ? parent.seq : '?'}`) : null,
    m.deleted
      ? h('p', { class: 'body muted' }, m.deleted.kind === 'redaction' ? 'message deleted (redacted)' : 'message deleted')
      : h('p', { class: 'body' }, m.text),
    m.mentions.length ? h('div', { class: 'mentions' }, 'mentions ', ...m.mentions.map(actor)) : null,
    chips(m.labels));
}

function render() {
  if (!S.board) return;
  renderChannels();
  renderThreads();
  renderThread();
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

$('forget').addEventListener('click', () => {
  needToken('Token forgotten.');
  history.replaceState(null, '', location.pathname);
});

$('filter').addEventListener('submit', (ev) => ev.preventDefault());
$('subtree').addEventListener('change', (ev) => { location.hash = href({ ...S.sel, sub: ev.target.checked }); });
let labelTimer;
$('label-filter').addEventListener('input', (ev) => {
  clearTimeout(labelTimer);
  labelTimer = setTimeout(() => { location.hash = href({ ...S.sel, label: ev.target.value.trim() }); }, 400);
});

const feed = createFeed({
  api, h, time, href,
  onTask: noteTask,
  onUnauthorized: () => needToken('The server rejected the token (401). Paste a valid token.'),
});

window.addEventListener('hashchange', onRoute);
loadToken();
onRoute();
