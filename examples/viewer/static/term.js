// Default board view: a dense terminal-style chat client. Sidebar with
// Mentions, Activity (the live feed), and channels with unread counts;
// thread summaries; a thread pane; keyboard navigation. Read-only by default.
// Opt-in operator mode opens shared Operator tools for message/task writes
// through the guarded workspace and exposes revoking logout.
// Terminal command input stays disabled, including in operator mode.

import { createStore, h, gateHint, avatar, jumpPalette, snip, ago, now } from './skin-kit.js';

import { createAgentActivity } from './agent-activity.js';

const SEEN_KEY = 'yak-viewer-seen';   // per-viewer "last seen" times (a convenience only)
const THEME_KEY = 'yak-viewer-theme';
const store_ = {
  get: (k) => { try { return JSON.parse(localStorage.getItem(k)); } catch { return null; } },
  set: (k, v) => { try { localStorage.setItem(k, JSON.stringify(v)); } catch { /* storage blocked */ } },
};
const STATE_ICON = { completed: '✓', failed: '✗', canceled: '✗', claimed: '▶', blocked: '■', waiting: '◷', ready: '○' };
const time = (ts) => h('time', { datetime: ts, title: `${ts} (UTC)` }, new Date(ts).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' }));

function dayLabel(ts) {
  const d = new Date(ts), today = new Date(now());
  const days = Math.round((new Date(today.toDateString()) - new Date(d.toDateString())) / 86400000);
  if (days === 0) return 'Today';
  if (days === 1) return 'Yesterday';
  return d.toLocaleDateString([], { weekday: 'long', month: 'long', day: 'numeric' });
}

export function createTerm({ api, href, setStatus, onUnauthorized, onForget, liveEl }) {
  const root = () => document.getElementById('term');
  let cur = null, route = {}, boards = [], status = 'connecting', activityNew = 0, sel = -1, sideOpen = false;
  let ownershipOpen = false;
  let theme = store_.get(THEME_KEY) || 'dark';
  const seen = store_.get(SEEN_KEY) || {};
  let mark = {};        // "New" divider positions, fixed when a view opens
  let scrolledTo = '';  // message already scrolled to for this route

  const st = () => cur.store;
  const seenOf = (k) => (seen[cur.boardId] ||= {})[k];
  const setSeen = (k) => { (seen[cur.boardId] ||= {})[k] = new Date(now()).toISOString(); store_.set(SEEN_KEY, seen); };
  const go = (r) => { location.hash = href({ board: cur.boardId, ...r }); };

  async function mount(r) {
    if (route.key !== r.key) sideOpen = false;
    route = r;
    let boardId = r.board;
    if (!boards.length || !boardId) boards = (await api('GET', '/v1/boards')).boards;
    boardId ||= boards[0]?.id;
    if (!boardId) { root().replaceChildren(h('p', { class: 'pad' }, 'No visible boards.')); return; }
    if (cur?.boardId !== boardId) {
      unmount();
      const store = createStore({
        api, boardId, onUnauthorized,
        onStatus: (s) => { status = s; setStatus(s); render(); },
        onChange: () => render(),
        onEvent: (ev) => { if (route.v !== 'activity') activityNew++; },
      });
      cur = { boardId, store };
      activityTimer = setInterval(() => activity?.update(), 1000);
      palette = jumpPalette(paletteItems, 'Jump to a channel or thread', rememberFocus);
    }
    open();
    render();
  }

  // Fix the "New" divider for the view being opened, then mark it seen.
  function open() {
    if (!cur.store.S.board) return;
    // First visit to a channel: start its unread count now, not from the beginning.
    for (const c of cur.store.S.channels) if (!seenOf(`#${c.path}`)) setSeen(`#${c.path}`);
    if (!seenOf('@')) setSeen('@');
    const key = route.t ? `t:${route.t}` : route.v === 'mentions' ? '@' : route.v === 'activity' ? null : `#${route.ch || ''}`;
    if (route.v === 'activity') activityNew = 0;
    if (!key || mark.key === key) return;
    mark = { key, at: seenOf(key) || new Date(now()).toISOString() };
    setSeen(key);
    if (route.ch) setSeen(`#${route.ch}`);
    sel = -1;
  }

  let palette = null, activity = null, activityTimer = null;
  function unmount() {
    if (!cur) return;
    cur.store.stop();
    clearInterval(activityTimer);
    palette?.dispose();
    helpDialog?.close();
    helpDialog?.remove();
    helpDialog = null;
    cur = null;
    activity = null;
    ownershipOpen = false;
    sideOpen = false;
    mark = {};
  }

  // ---------- derived ----------
  const meId = () => st().S.me?.id;
  const channelThreads = (path) => [...st().S.threads.values()].filter((t) => !path || t.channel_path === path);
  // Channel view filter: subchannels unless sub=0, and root-label clauses
  // (LBL-003: values of one key are ORed, different keys are ANDed).
  function inView(t) {
    const ch = route.ch;
    if (ch && !(t.channel_path === ch || (route.sub !== false && t.channel_path.startsWith(`${ch}/`)))) return false;
    const byKey = new Map();
    for (const c of (route.label || '').split(/[\s,]+/).filter(Boolean)) {
      const i = c.indexOf(':'), k = (i < 0 ? c : c.slice(0, i)).toLowerCase();
      byKey.set(k, [...(byKey.get(k) || []), i < 0 ? null : c.slice(i + 1)]);
    }
    for (const [k, vals] of byKey) {
      if (!Object.hasOwn(t.labels || {}, k) || !vals.some((v) => v === null || t.labels[k] === v)) return false;
    }
    return true;
  }
  const taskOf = (threadId) => st().tasks().find((x) => x.thread_id === threadId);
  function unread(path) {
    const since = seenOf(`#${path}`);
    if (!since) return 0;
    let n = 0;
    for (const t of channelThreads(path)) for (const m of st().messages(t.id)) if (m.created_at > since && m.author_actor_id !== meId()) n++;
    return n;
  }
  const mentions = () => [...st().S.threads.values()].flatMap((t) => st().messages(t.id))
    .filter((m) => m.mentions.includes(meId())).sort((a, b) => a.created_at.localeCompare(b.created_at));

  // ---------- pieces ----------
  function who(id, ts, principal) {
    const a = st().actor(id), via = st().via(id);
    return h('div', { class: 't-who' }, h('b', {}, st().handle(id)), principal ? ` for ${st().handle(principal)} (${st().actor(principal)?.kind || 'unknown'})` : '', via ? h('span', { class: 't-mute' }, ` via ${via}`) : '',
      a ? h('span', { class: 't-badge' }, a.kind) : '', ts ? [' ', h('span', { class: 't-mute' }, time(ts))] : '');
  }

  function text(m) {
    if (m.deleted) return h('p', { class: 't-text t-mute' }, m.deleted.kind === 'redaction' ? 'message deleted (redacted)' : 'message deleted');
    const handles = m.mentions.map((id) => st().actor(id)?.handle).filter(Boolean);
    if (!handles.length) return h('p', { class: 't-text' }, m.text);
    const re = new RegExp(`(@(?:${handles.map((x) => x.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')).join('|')}))`, 'g');
    return h('p', { class: 't-text' }, m.text.split(re).map((part, i) => (i % 2 ? h('span', { class: 't-mention' }, part) : part)));
  }

  const labels = (ls) => {
    const shown = Object.entries(ls || {}).filter(([k]) => !k.startsWith('yak.') && !k.startsWith('plan.'));
    return shown.length ? h('div', { class: 't-labels' }, shown.map(([k, v]) => h('span', {}, v === '' ? k : `${k}=${v}`))) : null;
  };

  function taskChip(task) {
    if (!task) return null;
    const c = task.current_claim;
    const extra = task.outcome?.summary || task.outcome?.reason || task.block?.reason
      || (c ? `${st().handle(c.actor_id)} until ${new Date(c.expires_at).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' })}` : '')
      || (task.state === 'waiting' ? `needs ${st().blockers(task).map((d) => st().num(d.task_id)).join(', ') || 'hidden'}` : '');
    return h('div', { class: `t-task t-task-${task.state}` }, h('span', { class: 't-badge' }, `TASK ${st().num(task.id)}`),
      h('b', {}, ` ${STATE_ICON[task.state] || '?'} ${task.state}`), extra ? `: ${snip(extra, 160)}` : '', gateHint(task, st().labels(task)) ? h('p', {}, gateHint(task, st().labels(task))) : null);
  }

  function summary(t) {
    const msgs = st().messages(t.id);
    const root = msgs.find((m) => m.is_root) || msgs[0];
    const replies = msgs.filter((m) => !m.is_root);
    const people = [...new Set(replies.map((m) => st().handle(m.author_actor_id)))].slice(0, 3);
    return h('article', { class: 't-item', 'data-thread': t.id, onclick: (e) => { if (!e.target.closest('a')) go({ ch: t.channel_path, t: t.id }); } },
      root ? who(root.author_actor_id, root.created_at, root.on_behalf_of_actor_id) : null,
      h('a', { class: 't-title', 'data-control': `thread:${t.id}`, href: href({ board: cur.boardId, ch: t.channel_path, t: t.id }) }, t.title),
      labels(t.labels), root ? text({ ...root, text: snip(root.text, 400) }) : null, taskChip(taskOf(t.id)),
      replies.length ? h('div', { class: 't-replies' }, people.join(', '), ' ',
        h('a', { 'data-control': `replies:${t.id}`, href: href({ board: cur.boardId, ch: t.channel_path, t: t.id }) }, `${replies.length} repl${replies.length === 1 ? 'y' : 'ies'}`),
        h('span', { class: 't-mute' }, ` Last reply ${ago(replies.at(-1).created_at)} ago`)) : null);
  }

  function message(m, byId) {
    const parent = m.reply_to_message_id && byId.get(m.reply_to_message_id);
    return h('article', { class: `t-item${m.id === route.m ? ' t-target' : ''}`, id: `tm-${m.id}` },
      who(m.author_actor_id, m.created_at, m.on_behalf_of_actor_id),
      parent && !parent.is_root ? h('div', { class: 't-mute t-small' }, `↪ reply to ${st().handle(parent.author_actor_id)} #${parent.seq}`) : null,
      m.edited_at && !m.deleted ? h('span', { class: 't-mute t-small' }, '(edited)') : null,
      labels(m.is_root ? null : m.labels), text(m));
  }

  // Interleave day dividers and the "New" divider into a time-ordered list.
  function dividers(items, tsOf, build) {
    const out = [];
    let day = null, newDone = false;
    for (const it of items) {
      const ts = tsOf(it);
      const d = dayLabel(ts);
      if (d !== day) { out.push(h('div', { class: 't-day' }, h('span', {}, d))); day = d; }
      if (!newDone && mark.at && ts > mark.at) { out.push(h('div', { class: 't-new' }, h('span', {}, 'New'))); newDone = true; }
      out.push(build(it));
    }
    return out;
  }

  // ---------- views ----------
  function channelView() {
    const ts = [...st().S.threads.values()].filter(inView).sort((a, b) => a.updated_at.localeCompare(b.updated_at));
    const filter = route.label ? h('span', { class: 't-filter' }, `label: ${route.label} `,
      h('a', { href: href({ board: cur.boardId, ch: route.ch, sub: route.sub }), 'data-control': 'clear-filter', 'aria-label': 'Clear the label filter', title: 'Clear the label filter' }, '×')) : null;
    return {
      head: [h('b', {}, route.ch ? `# ${route.ch}` : '# all channels'), filter, h('span', { class: 't-mute' }, `${ts.length} threads`)],
      body: ts.length ? [h('div', { class: 't-intro' }, h('b', {}, route.ch ? `# ${route.ch}` : '# all channels'), h('p', { class: 't-mute' }, 'Threads by last activity, oldest first.')),
        dividers(ts, (t) => t.updated_at, summary)] : [h('p', { class: 't-mute t-pad' }, 'No threads here yet.')],
      composer: `Message #${route.ch || 'channel'}`,
    };
  }

  function threadView() {
    const t = st().thread(route.t);
    if (!t) return { head: [h('b', {}, 'Thread')], body: [h('p', { class: 't-mute t-pad' }, 'Thread not found or not visible.')], composer: 'Reply…' };
    const msgs = st().messages(t.id), byId = new Map(msgs.map((m) => [m.id, m]));
    const root = msgs.find((m) => m.is_root), replies = msgs.filter((m) => !m.is_root);
    return {
      head: [h('b', {}, 'Thread'), h('span', { class: 't-mute' }, ` # ${t.channel_path}`), h('span', { class: 't-spacer' }),
        h('a', { class: 't-close', 'data-control': 'close', href: href({ board: cur.boardId, ch: t.channel_path }), title: 'Close (Esc)', 'aria-label': 'Close thread' }, '×')],
      body: [h('h1', { class: 't-h1' }, t.title), labels(t.labels), root ? message(root, byId) : null, taskChip(taskOf(t.id)),
        h('div', { class: 't-count' }, h('span', {}, `${replies.length} repl${replies.length === 1 ? 'y' : 'ies'}`)),
        dividers(replies, (m) => m.created_at, (m) => message(m, byId))],
      composer: 'Reply…',
    };
  }

  function mentionsView() {
    const ms = mentions();
    return {
      head: [h('b', {}, '@ Mentions'), h('span', { class: 't-mute' }, `${ms.length}`)],
      body: ms.length ? dividers(ms, (m) => m.created_at, (m) => h('article', { class: 't-item', 'data-thread': m.thread_id, 'data-msg': m.id,
        onclick: () => go({ ch: m.channel_path, t: m.thread_id, m: m.id }) },
        h('div', { class: 't-mute t-small' }, `# ${m.channel_path} · ${st().thread(m.thread_id)?.title || ''}`), who(m.author_actor_id, m.created_at, m.on_behalf_of_actor_id), text(m)))
        : [h('p', { class: 't-mute t-pad' }, `No messages mention @${st().S.me?.handle}.`)],
      composer: null,
    };
  }

  // Live feed (feed.js, from the head of every board) above the newest messages of this board.
  function activityView() {
    const recent = [...st().S.threads.values()].flatMap((t) => st().messages(t.id))
      .sort((a, b) => b.created_at.localeCompare(a.created_at)).slice(0, 30);
    return {
      head: [h('b', {}, '~ Activity'), h('span', { class: 't-mute' }, 'all boards, newest first')],
      body: [h('div', { class: 't-day' }, h('span', {}, `Earlier on ${st().S.board.name}`)),
        recent.map((m) => h('article', { class: 't-item', 'data-thread': m.thread_id, 'data-msg': m.id, onclick: () => go({ ch: m.channel_path, t: m.thread_id, m: m.id }) },
          h('div', { class: 't-mute t-small' }, `# ${m.channel_path} · ${st().thread(m.thread_id)?.title || ''}`), who(m.author_actor_id, m.created_at, m.on_behalf_of_actor_id),
          text(m.deleted ? m : { ...m, text: snip(m.text, 300) })))],
      composer: null, activity: true,
    };
  }

  // ---------- render ----------
  const phone = matchMedia('(max-width: 700px)');
  function setSideOpen(open) {
    sideOpen = open;
    const app = root().querySelector('.t-app');
    if (!app) return;
    app.classList.toggle('t-sideopen', open);
    app.querySelector('.t-main').inert = open;
    const menu = app.querySelector('.t-menu');
    menu.setAttribute('aria-expanded', String(open));
    if (phone.matches) (open ? app.querySelector('.t-side-close') : menu).focus();
  }
  phone.addEventListener('change', () => {
    if (sideOpen) setSideOpen(false);
  });
  // Capture stable identities before a render detaches the focused node.
  function rememberFocus(active) {
    const control = active.dataset.control;
    const row = active.closest('[data-activity-task]');
    const index = row ? [...row.querySelectorAll('a')].indexOf(active) : -1;
    return () => {
      let target = active;
      if (!active.isConnected) {
        if (control) target = root().querySelector(`[data-control="${CSS.escape(control)}"]`);
        else if (active.matches('.t-agent-activity')) target = activity?.root;
        else if (row && index >= 0) {
          const replacement = root().querySelector(`[data-activity-task="${CSS.escape(row.dataset.activityTask)}"]`);
          target = replacement?.querySelectorAll('a')[index] || activity?.root;
        } else if (active.id) target = document.getElementById(active.id);
      }
      target?.focus({ preventScroll: true });
    };
  }
  function render() {
    if (!cur) return;
    const S = st().S;
    if (!S.board) { root().replaceChildren(h('p', { class: 'pad' }, 'Loading board…')); return; }
    open();
    const view = route.v === 'activity' ? activityView() : route.v === 'mentions' ? mentionsView() : route.t ? threadView() : channelView();
    const active = document.activeElement;
    const sameView = route.key === render.lastKey && root().contains(active);
    const restoreFocus = sameView ? rememberFocus(active) : null;
    const activityTop = root().querySelector('.t-agent-activity')?.scrollTop || 0;
    const sideTop = root().querySelector('.t-side')?.scrollTop || 0;
    const old = root().querySelector('.t-scroll');
    const keep = old && route.key === render.lastKey ? { top: old.scrollTop, bottom: old.scrollTop + old.clientHeight >= old.scrollHeight - 8 } : null;
    render.lastKey = route.key;
    const activeCh = route.t ? st().thread(route.t)?.channel_path : route.ch;
    const unseenMentions = mentions().filter((m) => m.created_at > (seenOf('@') || '9')).length;
    const item = (icon, label, count, r, active) => h('a', { class: `t-nav${active ? ' on' : ''}${count ? ' t-unread' : ''}`, 'data-control': href({ board: cur.boardId, ...r }), href: href({ board: cur.boardId, ...r }), onclick: () => { if (sideOpen) setSideOpen(false); } },
      h('span', { class: 't-mute' }, icon), ' ', h('span', { class: 't-navlabel' }, label), count ? h('span', { class: 't-pill' }, String(count)) : null);
    const side = h('nav', { class: 't-side', id: 'chat-channels', 'aria-label': 'Channels' },
      h('button', { type: 'button', class: 't-side-close', 'data-control': 'close-channels', onclick: () => setSideOpen(false) }, 'Close channels'),
      boards.length > 1
        ? h('select', { class: 't-boardsel', 'aria-label': 'Board', 'data-control': 'board', onchange: (e) => { location.hash = href({ board: e.target.value, v: 'activity' }); } },
          boards.map((b) => h('option', { value: b.id, selected: b.id === cur.boardId }, b.name)))
        : h('b', { class: 't-boardname' }, S.board.name),
      h('div', { class: 't-me' }, S.me?.handle),
      h('button', { type: 'button', class: 't-jump', 'data-control': 'jump', onclick: (e) => palette.open(e.currentTarget) }, 'Jump to…', h('kbd', {}, '^K')),
      item('@', 'Mentions', unseenMentions, { v: 'mentions' }, route.v === 'mentions'),
      item('~', 'Activity', activityNew, { v: 'activity' }, route.v === 'activity'),
      h('div', { class: 't-sec' }, 'Channels'),
      item('#', 'all channels', 0, {}, !route.v && !activeCh),
      S.channels.map((c) => item(c.access_mode === 'private' ? '◆' : '#', c.path, unread(c.path), { ch: c.path }, !route.v && activeCh === c.path)),
      h('div', { class: 't-sidefoot' },
        h('div', {}, h('a', { 'data-control': 'lanes', href: `#/board?b=${encodeURIComponent(cur.boardId)}` }, 'Lanes'), ' · ', h('a', { 'data-control': 'forum', href: `#/forum?b=${encodeURIComponent(cur.boardId)}` }, 'Forum'), ' · ', h('a', { 'data-control': 'switch-board', href: '#/boards' }, 'Switch board')),
        h('div', {}, h('button', { type: 'button', 'data-control': 'theme', onclick: () => { theme = theme === 'dark' ? 'light' : 'dark'; store_.set(THEME_KEY, theme); render(); } }, theme === 'dark' ? '◐ light' : '◑ dark'),
          ' ', h('button', { type: 'button', 'data-control': 'forget', onclick: onForget }, document.body.dataset.operator ? 'Logout (revokes token everywhere)' : 'Forget token'))));
    const scroll = h('div', { class: `t-scroll${view.activity ? ' t-activity' : ''}`, role: 'log' }, view.body);
    activity = createAgentActivity(st(), href, status, { open: ownershipOpen, toggle: () => { ownershipOpen = !ownershipOpen; render(); } });
    const main = h('section', { class: 't-main', inert: sideOpen },
      h('header', { class: 't-head' }, h('button', { type: 'button', class: 't-menu', 'aria-label': 'Channels', 'data-control': 'channels', 'aria-controls': side.id,
        'aria-expanded': String(sideOpen), onclick: () => setSideOpen(true) }, '☰'), view.head),
      activity.root, scroll,
      view.composer ? h('div', { class: 't-composer' }, document.body.dataset.operator ? h('button', { type: 'button', 'data-control': 'compose', onclick: () => window.dispatchEvent(new CustomEvent('yak-operator-open', { detail: { board: cur.boardId, ch: activeCh, t: route.t } })) }, route.t ? 'Reply / task actions' : 'New thread / task') : h('input', { type: 'text', disabled: true, 'aria-label': `${view.composer} (read-only)`, placeholder: `${view.composer} (read-only viewer)` })) : null);
    const hints = route.t ? [['↑↓', 'move'], ['esc', 'close'], ['^K', 'jump'], ['?', 'help']] : [['↑↓', 'move'], ['⏎', 'open'], ['^K', 'jump'], ['?', 'help']];
    const foot = h('footer', { class: 't-foot' }, h('span', { class: `t-${status}` }, status),
      h('span', { class: 't-mute' }, `${S.me?.handle} on ${S.board.name}`), h('span', { class: 't-spacer' }),
      hints.map(([k, v]) => h('span', { class: 't-hint' }, h('kbd', {}, k), ` ${v}`)));
    root().replaceChildren(h('div', { class: `t-app${sideOpen ? ' t-sideopen' : ''}`, 'data-theme': theme }, side,
      h('div', { class: 't-side-backdrop', 'aria-hidden': 'true', onclick: () => setSideOpen(false) }), main, foot));
    if (view.activity && liveEl) { liveEl.hidden = false; scroll.prepend(liveEl); }
    activity.root.scrollTop = activityTop;
    side.scrollTop = sideTop;
    restoreFocus?.();
    paintSel();
    if (keep) scroll.scrollTop = keep.bottom ? scroll.scrollHeight : keep.top;
    else if (route.m && scrolledTo !== route.m && document.getElementById(`tm-${route.m}`)) { document.getElementById(`tm-${route.m}`).scrollIntoView({ block: 'start' }); scrolledTo = route.m; }
    else if (!view.activity) scroll.scrollTop = scroll.scrollHeight; // chat: newest at the bottom
  }

  // ---------- keyboard ----------
  // Keep the modal outside the subtree replaced by live Chat renders.
  let helpDialog = null;
  function openHelp() {
    const restoreFocus = rememberFocus(document.activeElement);
    function close() {
      helpDialog.close();
      helpDialog.remove();
      helpDialog = null;
      restoreFocus();
    }
    helpDialog = h('dialog', { class: 't-help', 'data-theme': theme, 'aria-labelledby': 'chat-keys',
      oncancel: (e) => { e.preventDefault(); close(); },
      onkeydown: (e) => {
        e.stopPropagation();
        if (e.key === 'Tab') e.preventDefault(); // Close is the only tab stop.
        if (e.key === '?') { e.preventDefault(); close(); }
      } },
      h('header', {}, h('b', { id: 'chat-keys' }, 'Keys'),
        h('button', { type: 'button', autofocus: true, onclick: close }, 'Close')),
      [['↑ / ↓  or  k / j', 'move the selection'], ['Enter', 'open the selected thread'], ['Esc', 'close the thread or this help'],
        ['Ctrl-K / Cmd-K', 'jump to a channel or thread'], ['g a / g m', 'go to Activity / Mentions'], ['?', 'show or hide this help']]
        .map(([k, v]) => h('div', {}, h('kbd', {}, k), ` ${v}`)), h('p', { class: 't-mute' }, document.body.dataset.operator ? 'Use Operator tools for messages and confirmed task actions.' : 'This viewer is read-only.'));
    document.body.append(helpDialog);
    helpDialog.showModal();
  }
  const items = () => [...root().querySelectorAll('.t-scroll .t-item')];
  function paintSel() { items().forEach((e, i) => e.classList.toggle('t-sel', i === sel)); }
  function move(d) {
    const list = items();
    if (!list.length) return;
    sel = sel < 0 ? (d > 0 ? 0 : list.length - 1) : Math.max(0, Math.min(list.length - 1, sel + d));
    paintSel();
    list[sel].scrollIntoView({ block: 'nearest' });
  }
  let gPending = false;
  function onKey(e) {
    if (!cur || document.getElementById('term').hidden || document.querySelector(':modal')) return;
    if (sideOpen) {
      if (e.key === 'Escape') {
        e.preventDefault();
        setSideOpen(false);
      }
      return;
    }
    if (e.key === 'Enter' && e.target.closest?.('button, a')) return;
    if (e.target.closest?.('.t-agent-activity, input, select, textarea') || e.ctrlKey || e.metaKey || e.altKey) return;
    const k = e.key;
    if (gPending) { gPending = false; if (k === 'a') return go({ v: 'activity' }); if (k === 'm') return go({ v: 'mentions' }); }
    if (k === 'ArrowDown' || k === 'j') { e.preventDefault(); move(1); }
    else if (k === 'ArrowUp' || k === 'k') { e.preventDefault(); move(-1); }
    else if (k === 'Enter' && sel >= 0) { const it = items()[sel]; if (it?.dataset.thread) go({ ch: st().thread(it.dataset.thread)?.channel_path, t: it.dataset.thread, m: it.dataset.msg }); }
    else if (k === 'Escape' && route.t) go({ ch: route.ch || st().thread(route.t)?.channel_path });
    else if (k === '?') { e.preventDefault(); openHelp(); }
    else if (k === 'g') gPending = true;
  }
  document.addEventListener('keydown', onKey);

  function paletteItems() {
    if (!cur?.store.S.board) return [];
    const S = st().S;
    return [{ label: '~ Activity', go: () => go({ v: 'activity' }) }, { label: '@ Mentions', go: () => go({ v: 'mentions' }) },
      ...S.channels.map((c) => ({ label: `# ${c.path}`, hint: unread(c.path) ? `${unread(c.path)} new` : '', go: () => go({ ch: c.path }) })),
      ...[...S.threads.values()].map((t) => ({ label: `» ${t.title}`, hint: `#${t.channel_path}`, go: () => go({ ch: t.channel_path, t: t.id }) }))];
  }

  return { mount, unmount, activity: () => route.v === 'activity' };
}
