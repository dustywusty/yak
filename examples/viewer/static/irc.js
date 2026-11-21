// Retro IRC: board channels as tabs, thread messages as chat lines, task
// changes as action lines, a nick list, and a read-only input line.

import { h, gateHint, lease, jumpPalette } from './skin-kit.js';

const hhmm = (ts) => new Date(ts).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' });

// Task history comes only from events received during this view; no snapshot backfill.
const LOGGED = {
  'task.created': () => 'opened task', 'task.claimed': () => 'claimed task',
  'task.completed': (p) => `completed task${p.outcome?.summary ? `: ${p.outcome.summary}` : ''}`,
  'task.failed': (p) => `failed task${p.outcome?.reason ? `: ${p.outcome.reason}` : ''}`,
  'task.blocked': (p) => `blocked task: ${p.reason ?? ''}`, 'task.unblocked': (p) => `unblocked task: ${p.reason ?? ''}`,
  'task.released': (p) => `released task (${p.reason ?? 'no reason'})`, 'task.claim_expired': () => 'let the claim on task expire',
  'task.reopened': (p) => `reopened task: ${p.reason ?? ''}`, 'task.became_ready': () => 'task is ready (dependencies satisfied)',
};

export function irc(root, { threadHref }) {
  let tab = null, topicThread = null, palette = null, store = null, leaseEl = null;
  const unread = new Map(), log = [];
  const chrome = document.querySelector('.top');
  const chromeHome = document.createComment('viewer navigation');
  chrome.before(chromeHome);
  let connection = 'resyncing';

  const nick = (st, id, principal) => {
    const a = st.actor(id);
    const mode = a?.kind === 'human' ? '@' : '+';
    const via = st.via(id);
    return h('span', { class: `i-nick ${mode === '@' ? 'i-person' : 'i-agent'}` }, `<${mode}${st.handle(id)}${via ? `|${via}` : ''}> (${a?.kind || 'unknown'})${principal ? ` for ${st.handle(principal)} (${st.actor(principal)?.kind || 'unknown'})` : ''}`);
  };

  // Message text with highlighted mentions; text nodes only.
  function body(st, m) {
    const handles = m.mentions.map((id) => st.actor(id)?.handle).filter(Boolean);
    if (!handles.length) return [m.text];
    const re = new RegExp(`(@?(?:${handles.map((x) => x.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')).join('|')}))\\b`, 'g');
    return m.text.split(re).map((part, i) => (i % 2 ? h('span', { class: 'i-hl' }, part) : part));
  }

  function lines(st) {
    const out = [];
    const threads = [...st.S.threads.values()].filter((t) => t.channel_path === tab);
    for (const t of threads) {
      for (const m of st.messages(t.id)) {
        out.push({ ts: m.created_at, thread: t.id, el: m.deleted
          ? [nick(st, m.author_actor_id, m.on_behalf_of_actor_id), h('span', { class: 'i-act' }, ' message was deleted')]
          : [nick(st, m.author_actor_id, m.on_behalf_of_actor_id), ' ', body(st, m)] });
      }
    }
    for (const x of log) if (x.channel === tab) out.push(x);
    out.sort((a, b) => Date.parse(a.ts) - Date.parse(b.ts));
    // Insert a marker when the conversation moves to another thread.
    const rows = [];
    let last = null;
    for (const x of out) {
      if (x.thread !== last) {
        const t = st.thread(x.thread);
        rows.push(h('div', { class: 'i-line i-join' }, h('button', { type: 'button', class: 'i-link', 'data-thread': x.thread, onclick: () => { topicThread = x.thread; render(st); } },
          `-- thread: ${t?.title || '?'}`)));
        last = x.thread;
      }
      rows.push(h('div', { class: 'i-line' }, h('span', { class: 'i-ts' }, `[${hhmm(x.ts)}] `), x.el));
    }
    return { rows, lastThread: last };
  }

  function statusLines(st) {
    const S = st.S;
    const items = [`*** ${connection} · ${S.board.name} as ${S.me?.handle}`,
      `*** ${S.channels.length} channels, ${S.threads.size} threads, ${S.tasks.size} tasks`,
      document.body.dataset.operator ? '*** Use Operator tools for messages and confirmed task actions. /commands are disabled.' : '*** This window is read-only. /commands are disabled.',
      ...log.map((x) => `[${hhmm(x.ts)}] #${x.channel} ${x.text}`)];
    return items.map((t) => h('div', { class: 'i-line i-status' }, t));
  }

  function topic(st, threadId) {
    const t = st.thread(threadId);
    if (!t) return tab === 'status' ? 'Status window' : `#${tab}`;
    const task = st.tasks().find((x) => x.thread_id === t.id);
    const c = task?.current_claim;
    leaseEl = c ? h('span', {}) : null;
    if (c) leaseEl.dataset.exp = c.expires_at;
    return [`Topic: ${t.title} | ${task ? `state=${task.state}` : `${t.message_count} messages`}`, task && gateHint(task, st.labels(task)) ? ` | ${gateHint(task, st.labels(task))}` : '',
      c ? [` | ${st.num(task.id)} claimed by ${st.handle(c.actor_id)} (`, leaseEl, ')'] : '',
      ' | ', h('a', { href: threadHref(t.id), 'data-focus': 'topic' }, 'open thread')];
  }

  function paintLease() {
    if (!leaseEl) return;
    const task = [...store.S.tasks.values()].find((x) => x.current_claim?.expires_at === leaseEl.dataset.exp);
    leaseEl.textContent = task ? `${lease(task.current_claim).text} left` : 'ended';
  }

  function render(st) {
    store = st;
    const S = st.S;
    if (!S.board) return;
    // Start in the channel with the newest message.
    tab ??= [...S.threads.values()].sort((a, b) => b.updated_at.localeCompare(a.updated_at))[0]?.channel_path || 'status';
    palette ??= jumpPalette(() => [{ label: 'Status', go: () => select('status') },
      ...S.channels.map((c) => ({ label: `#${c.path}`, hint: unread.get(c.path) ? `${unread.get(c.path)} new` : '', go: () => select(c.path) }))], 'Jump to a channel');
    const focused = document.activeElement;
    const focusChannel = focused?.dataset.channel;
    const focusControl = focused?.dataset.focus;
    const focusTab = focused?.getAttribute('role') === 'tab';
    const transcriptFocused = focused?.classList.contains('i-text');
    const focusedThread = focused?.dataset.thread;
    const textArea = root.querySelector('.i-text');
    const textTop = textArea?.scrollTop || 0;
    const paneScroll = ['.i-tree', '.i-nicks', '.i-tabs'].map((s) => root.querySelector(s)?.scrollTop || 0);
    const tabsLeft = root.querySelector('.i-tabs')?.scrollLeft || 0;
    const atBottom = !textArea || textArea.scrollTop + textArea.clientHeight >= textArea.scrollHeight - 8;
    const { rows, lastThread } = tab === 'status' ? { rows: statusLines(st), lastThread: null } : lines(st);
    const th = topicThread && st.thread(topicThread)?.channel_path === tab ? topicThread : lastThread;
    const actors = [...S.actors.values()].sort((a, b) => a.handle.localeCompare(b.handle));
    const people = actors.filter((a) => a.kind === 'human'), agents = actors.filter((a) => a.kind !== 'human');
    const operator = !!document.body.dataset.operator;
    const mode = operator ? 'Operator mode' : 'Read-only viewer';
    const win = h('div', { class: 'i-win' },
      h('div', { class: 'i-title' }, h('span', { class: 'i-icon', 'aria-hidden': 'true' }, 'Y'),
        h('span', { class: 'i-titletext' }, `Yak IRC - ${S.board.name} - ${tab === 'status' ? 'Status' : `#${tab}`} - ${mode}`)),
      chrome,
      h('div', { class: 'i-tools' }, h('button', { type: 'button', 'data-focus': 'jump', onclick: () => palette.open() }, 'Jump to channel…'),
        h('span', {}, `Ctrl-K · Alt-1…9 · ${mode}${operator ? ' · Use Operator tools for messages and confirmed task actions. /commands are disabled.' : ''}`)),
      h('div', { class: 'i-tabs', role: 'tablist' }, [['status', 'Status'], ...S.channels.map((c) => [c.path, `#${c.path}`])].map(([key, label]) =>
        h('button', { type: 'button', role: 'tab', 'aria-selected': String(key === tab), 'data-channel': key, class: `i-tab${key === tab ? ' on' : ''}${unread.get(key) ? ' unread' : ''}`,
          onclick: () => select(key) }, label, unread.get(key) ? ` (${unread.get(key)})` : ''))),
      h('div', { class: 'i-topic' }, topic(st, th)),
      h('div', { class: 'i-body' },
        h('nav', { class: 'i-tree', 'aria-label': 'Board channels' }, h('b', {}, S.board.name),
          [['status', 'Status'], ...S.channels.map((c) => [c.path, `#${c.path}`])].map(([key, label]) =>
            h('button', { type: 'button', 'data-channel': key, 'aria-current': key === tab ? 'page' : null,
              onclick: () => select(key) }, label, unread.get(key) ? ` (${unread.get(key)})` : ''))),
        h('div', { class: 'i-text', role: 'log', tabindex: '0', 'aria-label': 'Channel transcript' }, rows),
        h('div', { class: 'i-nicks' }, people.map((a) => h('div', { class: 'i-person' }, `@${a.handle}`)),
          agents.map((a) => h('div', { class: 'i-agent' }, `+${a.handle}`)),
          h('div', { class: 'i-count' }, `${actors.length} board actors`), h('div', { class: 'i-count' }, ` ${people.length} people (@)`),
          h('div', { class: 'i-count' }, ` ${agents.length} agents/services (+)`))),
      document.body.dataset.operator ? h('button', { type: 'button', class: 'i-input', onclick: () => window.dispatchEvent(new CustomEvent('yak-operator-open', { detail: { board: S.board.id, ch: tab, t: th } })) }, 'Reply / task actions') : h('input', { class: 'i-input', type: 'text', disabled: true, 'aria-label': 'message input (read-only)', placeholder: 'read-only viewer: /commands and messages are disabled' }),
      h('div', { class: 'i-statusbar' }, h('span', {}, `${connection} · ${S.board.name} as ${S.me?.handle}`),
        h('span', {}, 'Ctrl-K jump'), h('span', {}, `${S.channels.length} channels`)));
    root.replaceChildren(h('div', { class: 'i-desk' }, win));
    paintLease();
    const text = root.querySelector('.i-text');
    text.scrollTop = atBottom ? text.scrollHeight : textTop;
    ['.i-tree', '.i-nicks', '.i-tabs'].forEach((s, i) => { root.querySelector(s).scrollTop = paneScroll[i]; });
    root.querySelector('.i-tabs').scrollLeft = tabsLeft;
    if (focusChannel) [...root.querySelectorAll(focusTab ? '.i-tab' : '.i-tree button')].find((e) => e.dataset.channel === focusChannel)?.focus({ preventScroll: true });
    else if (focusControl) [...root.querySelectorAll('[data-focus]')].find((e) => e.dataset.focus === focusControl)?.focus({ preventScroll: true });
    else if (transcriptFocused) text.focus({ preventScroll: true });
    else if (focusedThread) [...root.querySelectorAll('.i-link')].find((e) => e.dataset.thread === focusedThread)?.focus({ preventScroll: true });
    else if (chrome.contains(focused)) focused.focus({ preventScroll: true });
  }

  function select(key) {
    tab = key;
    topicThread = null;
    unread.delete(key);
    render(store);
  }

  function event(ev, st) {
    const p = ev.payload || {};
    const channel = p.message?.channel_path || p.thread?.channel_path || p.task?.channel_path;
    if (!channel) return;
    if (LOGGED[ev.type]) {
      if (ev.id && log.some((x) => x.id === ev.id)) return;
      const actor = `${st.handle(ev.actor_id)} (${st.actor(ev.actor_id)?.kind || 'unknown'})`;
      const principal = ev.on_behalf_of_actor_id;
      const provenance = `${actor}${principal ? ` for ${st.handle(principal)} (${st.actor(principal)?.kind || 'unknown'})` : ''}`;
      const text = `* ${provenance} ${LOGGED[ev.type](p)} ${st.num(p.task.id)} · credential ${ev.credential_id || 'unknown'}${ev.type === 'task.unblocked' && (principal || ev.delegation_id || st.actor(ev.actor_id)?.kind !== 'human') ? ", not a person's approval" : ''}`;
      log.push({ id: ev.id, ts: ev.recorded_at, thread: ev.thread_id || p.task.thread_id, channel, text,
        el: [h('span', { class: 'i-act' }, text)] });
      if (log.length > 300) log.shift();
    }
    if ((ev.type === 'message.created' || ev.type === 'thread.created' || LOGGED[ev.type]) && channel !== tab) unread.set(channel, (unread.get(channel) || 0) + 1);
  }

  function onKey(e) {
    if (!e.altKey || !/^[1-9]$/.test(e.key) || !store) return;
    const keys = ['status', ...store.S.channels.map((c) => c.path)];
    if (keys[e.key - 1]) { e.preventDefault(); select(keys[e.key - 1]); }
  }
  document.addEventListener('keydown', onKey);

  return {
    render, event, tick: paintLease,
    status: (value, st) => { if (connection !== value) { connection = value; render(st); } },
    dispose: () => { chromeHome.replaceWith(chrome); palette?.dispose(); document.removeEventListener('keydown', onKey); },
  };
}
