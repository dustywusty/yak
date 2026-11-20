// Shared pieces for the viewer skins (and the default view): a live board
// store, server-relative time, lease math, avatars, and a jump palette.
// All content is inserted as text, never as HTML.

const enc = encodeURIComponent;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const TERMINAL = ['completed', 'failed', 'canceled'];

// ---------- server clock (display only) ----------
// The offset comes from the Date header of the unauthenticated discovery
// document (DISC-001). It has 1 s resolution and is used only to draw
// countdowns; no decision depends on it.
export const clock = { offset: 0, limits: { max_page_size: 200, max_wait_ms: 30000 } };
export const now = () => Date.now() + clock.offset;

export async function syncClock() {
  try {
    const t0 = Date.now();
    const res = await fetch('/v1/server', { cache: 'no-store' });
    const t1 = Date.now();
    const d = Date.parse(res.headers.get('Date'));
    if (!Number.isNaN(d)) clock.offset = d + 500 - (t0 + t1) / 2; // mid-second estimate
    const doc = await res.json();
    Object.assign(clock.limits, doc.limits || {});
  } catch { /* keep the last offset */ }
}

// ---------- formatting ----------
export const hhmm = (ts) => new Date(ts).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
export function ago(ts) {
  const s = Math.max(0, Math.round((now() - Date.parse(ts)) / 1000));
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  if (s < 86400) return `${Math.floor(s / 3600)}h`;
  return `${Math.floor(s / 86400)}d`;
}
export const snip = (t, n = 200) => (t == null ? '' : t.length > n ? `${t.slice(0, n)}…` : t);
export const isToday = (ts) => ts && new Date(ts).toDateString() === new Date(now()).toDateString();

// Lease state of a claim: time left and the fraction of lease_seconds left.
export function lease(claim) {
  if (!claim) return null;
  const left = Date.parse(claim.expires_at) - now();
  const frac = Math.max(0, Math.min(1, left / (claim.lease_seconds * 1000)));
  const s = Math.max(0, Math.ceil(left / 1000));
  const text = left <= 0 ? 'lapsing' : `${String(Math.floor(s / 60)).padStart(2, '0')}:${String(s % 60).padStart(2, '0')}`;
  return { left, frac, text };
}

// ---------- DOM ----------
export function h(tag, attrs = {}, ...kids) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v == null || v === false) continue;
    if (k === 'class') e.className = v;
    else if (k.startsWith('--')) e.style.setProperty(k, v);
    else if (k.startsWith('on')) e.addEventListener(k.slice(2), v);
    else e.setAttribute(k, v === true ? '' : v);
  }
  for (const k of kids.flat(Infinity)) if (k != null && k !== false) e.append(k); // strings become text nodes
  return e;
}

// Generated avatar: initials on a hue derived from the actor ID. No photos.
export function avatar(actor, id) {
  const key = actor?.id || id || '?';
  let hash = 0;
  for (const ch of key) hash = (hash * 31 + ch.codePointAt(0)) >>> 0;
  const name = actor?.handle || '?';
  const parts = name.split(/[-_.]/).filter(Boolean);
  const initials = (parts.length > 1 ? parts[0][0] + parts.at(-1)[0] : name.slice(0, 2)).toUpperCase();
  return h('span', { class: `av av-${actor?.kind || 'unknown'}`, '--hue': hash % 360, title: name, 'aria-hidden': 'true' }, initials);
}

// An action the viewer cannot perform: shown, but disabled.
export const readOnly = (label, cls = '') =>
  h('button', { type: 'button', class: `ro ${cls}`, disabled: true, title: 'read-only viewer' }, label);

// Ctrl-K / Cmd-K jump palette over a list of { label, hint, go }.
export function jumpPalette(getItems, placeholder = 'Jump to…') {
  let box = null;
  const close = () => { box?.remove(); box = null; };
  function open() {
    if (box) return close();
    const input = h('input', { type: 'text', placeholder, 'aria-label': placeholder, spellcheck: 'false' });
    const list = h('ul', {});
    let sel = 0, items = [];
    const draw = () => {
      const q = input.value.toLowerCase();
      items = getItems().filter((x) => x.label.toLowerCase().includes(q)).slice(0, 12);
      sel = Math.min(sel, Math.max(0, items.length - 1));
      list.replaceChildren(...items.map((x, i) => h('li', { class: i === sel ? 'sel' : '', onclick: () => { close(); x.go(); } },
        x.label, x.hint ? h('span', { class: 'hint' }, x.hint) : null)));
    };
    input.addEventListener('input', () => { sel = 0; draw(); });
    input.addEventListener('keydown', (e) => {
      if (e.key === 'ArrowDown') { sel++; draw(); e.preventDefault(); }
      else if (e.key === 'ArrowUp') { sel = Math.max(0, sel - 1); draw(); e.preventDefault(); }
      else if (e.key === 'Enter' && items[sel]) { const x = items[sel]; close(); x.go(); }
      else if (e.key === 'Escape') close();
    });
    box = h('div', { class: 'jump', role: 'dialog' }, input, list);
    document.body.append(box);
    draw();
    input.focus();
  }
  const onKey = (e) => { if (e.key.toLowerCase() === 'k' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); open(); } };
  document.addEventListener('keydown', onKey);
  return { open, close, dispose: () => { close(); document.removeEventListener('keydown', onKey); } };
}

// ---------- live board store ----------
// Snapshot (SNAP-001..003, TASK-092), then long-poll events from the
// continuation cursor (EVT-008..011) with the viewer's recovery rules:
// dedupe by id; 409 resync_required / 410 / 400 → new snapshot; 401 → stop;
// network errors → backoff, keep the cursor.
export function createStore({ api, boardId, onChange: change, onEvent: event, onStatus: status, onUnauthorized }) {
  // View callbacks are guarded: a drawing error is reported once and never
  // reaches the poll loop, where it would look like a network error.
  let drawFailed = false;
  const guard = (fn) => (...args) => {
    try { fn?.(...args); } catch (e) { if (!drawFailed) { drawFailed = true; console.error('viewer: draw failed', e); } }
  };
  const onChange = guard(change), onEvent = guard(event), onStatus = guard(status);
  const S = { board: null, actors: new Map(), channels: [], threads: new Map(), msgs: new Map(), tasks: new Map(),
    order: [], me: null, cursor: null, seen: new Set(), events: 0, lastSync: null };
  const abort = new AbortController();
  // Each replacement snapshot owns its refresh requests and bookkeeping.
  // A task revision orders rows only within one snapshot generation.
  const newRefresh = () => ({ abort: new AbortController(), reads: new Set(), retry: new Map(), ready: false });
  let refresh = newRefresh();
  const currentRefresh = (generation) => generation === refresh && !abort.signal.aborted;
  const invalidateRefresh = () => { refresh.abort.abort(); refresh = newRefresh(); };
  const B = `/v1/boards/${enc(boardId)}`;
  const get = async (path, params) => {
    const result = await api('GET', path, params, abort.signal);
    if (abort.signal.aborted) throw new DOMException('Store stopped', 'AbortError');
    return result;
  };
  // PAGE-001: follow next_page_token until it is null.
  const all = async (path, key) => {
    const out = [];
    let token = null;
    do {
      const page = await get(path, { limit: clock.limits.max_page_size, page_token: token });
      out.push(...page[key]);
      token = page.next_page_token;
    } while (token);
    return out;
  };
  const bucket = (id) => S.msgs.get(id) || S.msgs.set(id, new Map()).get(id);
  const putTask = (t) => {
    const cur = S.tasks.get(t.id);
    if (!cur) S.order.push(t.id);
    if (!cur || t.revision >= cur.revision) S.tasks.set(t.id, t);
  };

  async function snapshot() {
    invalidateRefresh();
    const [board, actors, channels, me] = await Promise.all([get(B), get(`${B}/actors`), get(`${B}/channels`), get('/v1/me')]);
    if (abort.signal.aborted) throw new DOMException('Store stopped', 'AbortError');
    Object.assign(S, { board, me: me.actor, channels: channels.channels, actors: new Map(actors.actors.map((a) => [a.id, a])),
      threads: new Map(), msgs: new Map(), tasks: new Map(), order: [] });
    let info;
    try {
      info = await api('POST', `${B}/snapshots`, {}, abort.signal);
    } catch (e) {
      if (e.status !== 413) throw e;
      // SNAP-004: too many items for one snapshot. Take the head position
      // first (EVT-010), then read the thread and task lists and the messages
      // of the 50 most recently active threads. Live events fill in the rest.
      const head = await get(`${B}/events`, { from: 'latest', limit: 1 });
      for (const t of await all(`${B}/threads`, 'threads')) S.threads.set(t.id, t);
      for (const t of await all(`${B}/tasks`, 'items')) putTask(t);
      const recent = [...S.threads.values()].sort((a, b) => b.updated_at.localeCompare(a.updated_at)).slice(0, 50);
      await Promise.all(recent.map(async ({ id }) => {
        for (const m of await all(`${B}/threads/${enc(id)}/messages`, 'messages')) bucket(id).set(m.id, m);
      }));
      S.cursor = head.next_cursor;
      S.seen.clear();
      return;
    }
    let token = null;
    try {
      do {
        const page = await get(`${B}/snapshots/${enc(info.id)}`, { limit: clock.limits.max_page_size, page_token: token });
        for (const it of page.items) {
          if (it.type === 'thread') S.threads.set(it.id, it.data);
          else if (it.type === 'message') bucket(it.data.thread_id).set(it.id, it.data);
          else if (it.type === 'task') putTask(it.data);
        }
        token = page.next_page_token;
        S.cursor = page.continuation_cursor;
      } while (token);
    } finally {
      // Not tied to this store's signal: the session is discarded even if the store stops now.
      api('DELETE', `${B}/snapshots/${enc(info.id)}`).catch(() => {});
    }
    S.seen.clear();
  }

  function apply(ev) {
    if (S.seen.has(ev.id)) return false;
    S.seen.add(ev.id);
    if (S.seen.size > 5000) S.seen.delete(S.seen.values().next().value);
    S.events++;
    const p = ev.payload || {};
    if (p.thread) S.threads.set(p.thread.id, p.thread);
    if (p.message && !S.threads.has(p.message.thread_id)) {
      // A thread we have not seen: use thread_title from the event (EVT-013) at once,
      // then read the thread. Events recorded before the upgrade have no thread_title.
      const m = p.message;
      if (typeof p.thread_title === 'string') {
        S.threads.set(m.thread_id, { id: m.thread_id, board_id: m.board_id, channel_id: m.channel_id, channel_path: m.channel_path,
          title: p.thread_title, labels: {}, message_count: m.seq, last_message_id: m.id, root_message_id: null,
          created_by_actor_id: m.author_actor_id, created_at: m.created_at, updated_at: m.created_at, task_id: p.task_id ?? null });
      }
      get(`${B}/threads/${enc(m.thread_id)}`).then((r) => { S.threads.set(r.thread.id, r.thread); onChange(); }).catch(() => {});
    }
    if (p.message) {
      bucket(p.message.thread_id).set(p.message.id, p.message);
      const t = S.threads.get(p.message.thread_id);
      if (t && ev.type === 'message.created') {
        S.threads.set(t.id, { ...t, message_count: Math.max(t.message_count, p.message.seq), last_message_id: p.message.id, updated_at: p.message.created_at });
      }
    }
    if (p.task) putTask(p.task);
    if (ev.type === 'channel.created') get(`${B}/channels`).then((r) => { S.channels = r.channels; onChange(); }).catch(() => {});
    if (ev.type === 'thread.view_invalidated') get(`${B}/threads/${enc(ev.thread_id)}`).then((r) => { S.threads.set(r.thread.id, r.thread); onChange(); }).catch(() => {});
    onEvent(ev);
    return true;
  }

  async function loop() {
    let backoff = 1000;
    while (!abort.signal.aborted) {
      try {
        if (!S.cursor) {
          onStatus('resyncing');
          await snapshot();
          if (abort.signal.aborted) return;
          refresh.ready = true;
          S.lastSync = now();
          onStatus('live');
          onChange();
        }
        const wait = backoff > 1000 ? 0 : Math.min(25000, clock.limits.max_wait_ms);
        const page = await get(`${B}/events`, { cursor: S.cursor, wait_ms: wait, limit: clock.limits.max_page_size });
        if (abort.signal.aborted) return;
        let changed = false;
        for (const ev of page.events) changed = apply(ev) || changed;
        S.cursor = page.next_cursor;
        S.lastSync = now();
        onStatus('live');
        backoff = 1000;
        if (changed) onChange();
      } catch (e) {
        if (abort.signal.aborted || e.name === 'AbortError') return;
        if (e.status === 401) return onUnauthorized();
        const code = e.problem?.code;
        if (code === 'resync_required' || code === 'cursor_expired' || code === 'snapshot_expired' || e.status === 400) {
          invalidateRefresh();
          S.cursor = null;
          if (e.status === 400) await sleep(backoff);
          continue;
        }
        onStatus('reconnecting');
        await sleep(backoff + Math.random() * 500);
        backoff = Math.min(backoff * 2, 30000);
      }
    }
  }

  // Once display time passes expiry, ask for the effective server view.
  // Do not invent a release from the browser clock (TASK-023..025).
  const expiryTimer = setInterval(() => {
    const generation = refresh;
    if (!generation.ready || !S.cursor || !currentRefresh(generation)) return;
    for (const t of S.tasks.values()) {
      if (generation.reads.size >= 4) break;
      if (t.state !== 'claimed' || !t.current_claim || Date.parse(t.current_claim.expires_at) > now()
        || generation.reads.has(t.id) || (generation.retry.get(t.id) || 0) > now()) continue;
      generation.reads.add(t.id);
      generation.retry.set(t.id, now() + 10000);
      api('GET', `${B}/tasks/${enc(t.id)}`, {}, generation.abort.signal)
        .then((r) => { if (currentRefresh(generation)) { putTask(r); onChange(); } })
        .catch((e) => { if (currentRefresh(generation) && e.status === 401) onUnauthorized(); })
        .finally(() => { if (currentRefresh(generation)) generation.reads.delete(t.id); });
    }
  }, 1000);

  // ---- derived views ----
  const actor = (id) => S.actors.get(id);
  const handle = (id) => actor(id)?.handle || (id ? `${id.slice(0, 10)}…` : 'none');
  const via = (id) => { const o = actor(id)?.owner_actor_id; return o ? handle(o) : null; };
  // Display number: creation ordinal among readable tasks (IDs are opaque).
  const num = (taskId) => { const i = S.order.indexOf(taskId); return i < 0 ? '#?' : `#${i + 1}`; };
  const tasks = () => S.order.map((id) => S.tasks.get(id));
  const thread = (id) => S.threads.get(id);
  const messages = (threadId) => [...(S.msgs.get(threadId)?.values() || [])].sort((a, b) => a.seq - b.seq);
  const latest = (threadId) => messages(threadId).filter((m) => !m.deleted).at(-1);
  const labels = (task) => S.threads.get(task.thread_id)?.labels || {};
  const gated = (task) => task.state === 'blocked' && task.direct_action_required === true;
  const blockers = (task) => task.dependencies.filter((d) => d.state !== 'completed');
  const settled = (task) => TERMINAL.includes(task.state);

  syncClock().then(loop);
  const clockTimer = setInterval(syncClock, 60000);
  return {
    S, actor, handle, via, num, tasks, thread, messages, latest, labels, gated, blockers, settled,
    stop: () => { abort.abort(); refresh.abort.abort(); clearInterval(clockTimer); clearInterval(expiryTimer); },
  };
}
