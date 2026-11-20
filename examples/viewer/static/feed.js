// Live feed: the newest activity across every readable board. One event
// long-poll loop per board, starting at the head (EVT-010 from=latest).

const WAIT_MS = 25000;
const CAP = 500;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const enc = encodeURIComponent;
const short = (id) => (id && id.length > 12 ? `${id.slice(0, 10)}…` : id || '?');
const snip = (t) => (t == null ? '' : t.length > 200 ? `${t.slice(0, 200)}…` : t);
const labelDiff = (a = {}, b = {}) => [
  ...Object.keys(b).filter((k) => a[k] !== b[k]).map((k) => `+${k}${b[k] === '' ? '' : `:${b[k]}`}`),
  ...Object.keys(a).filter((k) => !(k in b)).map((k) => `−${k}`)].join(' ');

export function createFeed({ api, h, time, href, onTask, onUnauthorized, onError = () => {} }) {
  const F = { boards: new Map(), entries: [], pending: [], seen: new Set(), paused: false,
    abort: null, timer: null, visible: false };
  // Element references are taken once. A view may move the feed out of the
  // document for a while (term.js does); the feed keeps working on its own nodes.
  const els = Object.fromEntries(['feed', 'feed-status', 'feed-pause', 'feed-board', 'feed-kind', 'feed-inval']
    .map((id) => [id, document.getElementById(id)]));
  const $ = (id) => els[id];
  // A drawing error must never end a poll loop: catch it, report it once, keep polling.
  let drawFailed = false;
  const report = (e) => {
    if (!drawFailed) { drawFailed = true; console.error('Activity feed: draw failed', e); onError(`Activity feed could not draw: ${e.message}`); }
  };
  const guard = (fn) => (...args) => { try { fn(...args); } catch (e) { report(e); } };

  function start() {
    if (F.abort) return;
    F.abort = new AbortController();
    relist();
    F.timer = setInterval(relist, 60000); // boards that appear later
  }

  function stop() {
    F.abort?.abort();
    clearInterval(F.timer);
    Object.assign(F, { abort: null, boards: new Map(), entries: [], pending: [], seen: new Set() });
    render();
  }

  async function relist() {
    const signal = F.abort?.signal;
    try {
      const { boards } = await api('GET', '/v1/boards', {}, signal);
      for (const b of boards) {
        const rec = F.boards.get(b.id);
        if (rec) { rec.board = b; continue; }
        const r = { board: b, actors: new Map(), titles: new Map(), cursor: null, state: 'connecting' };
        F.boards.set(b.id, r);
        loadActors(r, signal);
        loop(r, signal);
      }
      renderBoardFilter();
    } catch (e) {
      if (e.status === 401) onUnauthorized();
    }
  }

  async function loadActors(rec, signal) {
    try {
      const { actors } = await api('GET', `/v1/boards/${enc(rec.board.id)}/actors`, {}, signal);
      rec.actors = new Map(actors.map((a) => [a.id, a]));
      render();
    } catch { /* fall back to short IDs */ }
  }

  async function loop(rec, signal) {
    const path = `/v1/boards/${enc(rec.board.id)}/events`;
    let backoff = 1000;
    while (!signal.aborted && F.boards.get(rec.board.id) === rec) {
      try {
        const pos = rec.cursor ? { cursor: rec.cursor } : { from: 'latest' };
        const page = await api('GET', path, { ...pos, wait_ms: backoff > 1000 || !rec.cursor ? 0 : WAIT_MS, limit: 200 }, signal);
        rec.cursor = page.next_cursor; // EVT-008
        rec.state = 'live';
        backoff = 1000;
        for (const ev of page.events) add(rec, ev);
        if (page.events.length) render(); else renderStatus();
      } catch (e) {
        if (signal.aborted || e.name === 'AbortError') return;
        if (e.status === 401) return onUnauthorized();
        if (e.status === 404) { F.boards.delete(rec.board.id); renderBoardFilter(); return; }
        const code = e.problem?.code;
        if (code === 'resync_required' || code === 'cursor_expired' || e.status === 400) {
          // EVT-009: restart this board's position at the head; mark the gap.
          rec.cursor = null;
          push({ id: `resync-${rec.board.id}-${Date.now()}`, marker: `resynced (${code || e.status})`, rec, at: new Date().toISOString() });
          render();
          await sleep(1000);
          continue;
        }
        rec.state = 'reconnecting';
        renderStatus();
        await sleep(backoff + Math.random() * 500);
        backoff = Math.min(backoff * 2, 30000);
      }
    }
  }

  function add(rec, ev) {
    if (F.seen.has(ev.id)) return; // EVT-003
    F.seen.add(ev.id);
    if (F.seen.size > 5000) F.seen.delete(F.seen.values().next().value);
    const p = ev.payload || {};
    if (p.task) onTask(p.task);
    if (p.thread) rec.titles.set(p.thread.id, p.thread.title);
    if (p.task) rec.titles.set(p.task.thread_id, p.task.title);
    // message.* payloads carry the thread title at commit (EVT-013).
    if (typeof p.thread_title === 'string' && ev.thread_id) rec.titles.set(ev.thread_id, p.thread_title);
    push({ id: ev.id, ev, rec, at: ev.recorded_at, count: 1 });
  }

  const renewalOf = (e) => e.ev?.type === 'task.claim_renewed' && e.ev.payload.task?.id;

  function push(entry, list = F.paused ? F.pending : F.entries) {
    const task = renewalOf(entry);
    if (task) { // collapse repeated renewals of one task into the newest row
      const i = list.findIndex((x) => renewalOf(x) === task);
      if (i >= 0) { entry.count += list[i].count; list.splice(i, 1); }
    }
    list.unshift(entry);
    if (list.length > CAP) list.length = CAP;
  }

  // Fallback for servers before EVT-013, whose message.* payloads have no
  // thread_title: read the thread once and cache its title.
  function title(rec, threadId) {
    if (!threadId) return '';
    if (rec.titles.has(threadId)) return rec.titles.get(threadId);
    rec.titles.set(threadId, '…');
    api('GET', `/v1/boards/${enc(rec.board.id)}/threads/${enc(threadId)}`, {}, F.abort?.signal)
      .then((r) => { rec.titles.set(threadId, r.thread.title); render(); })
      .catch(() => rec.titles.set(threadId, '(thread not visible)'));
    return '…';
  }

  function who(rec, id) {
    const a = rec.actors.get(id);
    return a ? `@${a.handle} (${a.kind})` : short(id);
  }

  // One line of detail per event type; text only.
  function detail(rec, ev) {
    const p = ev.payload || {};
    const t = p.task, c = p.claim, at = (ts) => (ts ? new Date(ts).toLocaleTimeString() : '?');
    switch (ev.type) {
      case 'thread.created': return snip(p.message?.text);
      case 'message.created': case 'message.edited': return snip(p.message?.text);
      case 'message.deleted': return 'message deleted';
      case 'message.labels_changed': case 'thread.view_invalidated': return labelDiff(p.labels_before, p.labels_after);
      case 'thread.updated': return `title: ${p.thread?.title}`;
      case 'channel.created': return `#${p.channel?.path} (${p.channel?.access_mode})`;
      case 'task.created': return `origin ${p.origin}`;
      case 'task.claimed': return `${who(rec, c?.actor_id)} holds it until ${at(c?.expires_at)} · epoch ${c?.epoch}`;
      case 'task.claim_renewed': return `lease until ${at(c?.expires_at)}`;
      case 'task.released': return `reason: ${p.reason ?? '—'}`;
      case 'task.claim_expired': return `claim of ${who(rec, c?.actor_id)} expired`;
      case 'task.completed': return snip(p.outcome?.summary) || 'completed';
      case 'task.failed': return snip(p.outcome?.reason || p.outcome?.summary) || 'failed';
      case 'task.blocked': return `${p.reason ?? ''} (${p.cause})`;
      case 'task.unblocked': return `${p.reason ?? ''} · credential ${ev.credential_id || 'unknown'}${ev.on_behalf_of_actor_id || ev.delegation_id || rec.actors.get(ev.actor_id)?.kind !== 'human' ? `; unblocked by ${who(rec, ev.actor_id)}${ev.on_behalf_of_actor_id ? ` for ${who(rec, ev.on_behalf_of_actor_id)}` : ''}, not a person's approval` : ''}`;
      case 'task.reopened': return p.reason ?? '';
      case 'task.canceled': return `${p.reason ?? ''} (${p.cause})`;
      case 'task.assigned': return `${p.assignee_before ? who(rec, p.assignee_before) : 'none'} → ${p.assignee_after ? who(rec, p.assignee_after) : 'none'}`;
      case 'task.dependencies_changed': return `+${p.added?.length ?? 0} −${p.removed?.length ?? 0} deps · ${p.state_before} → ${p.state_after}`;
      case 'task.brief_updated': return `brief revision ${p.brief_revision}`;
      case 'task.became_ready': return 'dependencies satisfied';
      case 'task.updated': return `changed: ${(p.changed || []).join(', ')}`;
      default: return t ? `state ${t.state}` : '';
    }
  }

  function visible(e) {
    const board = $('feed-board').value, kind = $('feed-kind').value;
    if (board && e.rec.board.id !== board) return false;
    if (e.marker) return true;
    const type = e.ev.type;
    if (type === 'thread.view_invalidated' && !$('feed-inval').checked) return false;
    if (kind === 'tasks') return type.startsWith('task.');
    if (kind === 'messages') return !type.startsWith('task.');
    return true;
  }

  function row(e) {
    if (e.marker) {
      return h('li', { class: 'feed-marker' }, `${e.rec.board.name}: ${e.marker} at `, time(e.at));
    }
    const { ev, rec } = e, p = ev.payload || {};
    const path = p.message?.channel_path || p.thread?.channel_path || p.task?.channel_path || p.channel?.path;
    const kind = ev.type.split('.')[0];
    const link = ev.thread_id
      ? href({ board: rec.board.id, sub: true, t: ev.thread_id, m: p.message?.id || '' })
      : href({ board: rec.board.id, sub: true, ch: path || '' });
    return h('li', {}, h('a', { href: link },
      h('div', { class: 'meta' }, time(e.at), ` · ${rec.board.name}`, path ? ` · #${path}` : '',
        ' · ', h('span', { class: 'actor' }, `${who(rec, ev.actor_id)}${ev.on_behalf_of_actor_id ? ` for ${who(rec, ev.on_behalf_of_actor_id)}` : ''}`)),
      h('div', {}, h('span', { class: `type type-${kind}` }, ev.type), e.count > 1 ? ` ×${e.count} ` : ' ',
        h('span', { class: 'title' }, p.thread?.title || p.task?.title || p.thread_title || title(rec, ev.thread_id))),
      h('div', { class: 'snippet' }, detail(rec, ev))));
  }

  const renderBoardFilter = guard(drawBoardFilter);
  function drawBoardFilter() {
    const sel = $('feed-board'), cur = sel.value;
    sel.replaceChildren(h('option', { value: '' }, 'All boards'),
      ...[...F.boards.values()].map((r) => h('option', { value: r.board.id }, r.board.name)));
    sel.value = F.boards.has(cur) ? cur : '';
    renderStatus();
  }

  const renderStatus = guard(drawStatus);
  function drawStatus() {
    const recs = [...F.boards.values()];
    const state = !recs.length ? 'connecting' : recs.some((r) => r.state === 'reconnecting') ? 'reconnecting'
      : recs.every((r) => r.state === 'live') ? 'live' : 'connecting';
    const el = $('feed-status');
    el.dataset.state = state;
    el.textContent = `${state} · ${recs.length} board${recs.length === 1 ? '' : 's'}`;
    $('feed-pause').textContent = F.paused ? `Resume (${F.pending.filter((e) => { try { return visible(e); } catch { return false; } }).length} new)` : 'Pause';
  }

  const render = guard(draw);
  function draw() {
    drawStatus();
    if (!F.visible) return;
    const rows = [];
    for (const e of F.entries) { try { if (visible(e)) rows.push(row(e)); } catch (err) { report(err); } } // one bad entry skips only its row
    $('feed').replaceChildren(...(rows.length ? rows : [h('li', { class: 'muted' }, 'Waiting for activity…')]));
  }

  function togglePause() {
    F.paused = !F.paused;
    if (!F.paused) {
      for (const e of F.pending.reverse()) push(e, F.entries);
      F.pending = [];
    }
    render();
  }

  function setVisible(v) { F.visible = v; if (v) render(); }

  $('feed-pause').addEventListener('click', togglePause);
  for (const id of ['feed-board', 'feed-kind', 'feed-inval']) $(id).addEventListener('change', render);
  return { start, stop, setVisible };
}
