// Retro IRC: board channels as tabs, thread messages as chat lines, task
// changes as action lines, a nick list, and a read-only input line.

import { h, lease, jumpPalette } from './skin-kit.js';

const hhmm = (ts) => new Date(ts).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' });

const MENUS = ['File', 'View', 'Board', 'Commands', 'Window', 'Help'];
const TOOLS = ['⌁', '▭', '✉', '@', '☑', '⌗', '⎘', 'ⓘ'];
// Task events that the current task view cannot show later; logged as they arrive.
const LOGGED = {
  'task.blocked': (p) => `blocked task: ${p.reason ?? ''}`, 'task.unblocked': (p) => `unblocked task: ${p.reason ?? ''}`,
  'task.released': (p) => `released task (${p.reason ?? 'no reason'})`, 'task.claim_expired': () => 'let the claim on task expire',
  'task.reopened': (p) => `reopened task: ${p.reason ?? ''}`, 'task.became_ready': () => 'task is ready (dependencies satisfied)',
};

export function irc(root, { threadHref }) {
  let tab = null, topicThread = null, palette = null, store = null, leaseEl = null;
  const unread = new Map(), log = [];

  const nick = (st, id) => {
    const a = st.actor(id);
    const mode = a?.kind === 'human' ? '@' : '+';
    const via = st.via(id);
    return h('span', { class: `i-nick ${mode === '@' ? 'i-person' : 'i-agent'}` }, `<${mode}${st.handle(id)}${via ? `|${via}` : ''}>`);
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
          ? [h('span', { class: 'i-act' }, `* ${st.handle(m.author_actor_id)}'s message was deleted`)]
          : [nick(st, m.author_actor_id), ' ', body(st, m)] });
      }
    }
    for (const task of st.tasks().filter((t) => t.channel_path === tab)) {
      const act = (ts, who, text) => out.push({ ts, thread: task.thread_id, el: [h('span', { class: 'i-act' }, `* ${st.handle(who)} ${text}`)] });
      act(task.created_at, task.created_by_actor_id, `opened task ${st.num(task.id)}`);
      const c = task.current_claim;
      if (c) act(c.acquired_at, c.actor_id, `claimed task ${st.num(task.id)} (lease ${Math.round(c.lease_seconds / 60)}m)`);
      if (task.outcome) act(task.outcome.at, task.outcome.by_actor_id, `${task.outcome.kind} task ${st.num(task.id)}${task.outcome.summary ? `: ${task.outcome.summary}` : task.outcome.reason ? `: ${task.outcome.reason}` : ''}`);
    }
    for (const x of log) if (x.channel === tab) out.push(x);
    out.sort((a, b) => Date.parse(a.ts) - Date.parse(b.ts));
    // Insert a marker when the conversation moves to another thread.
    const rows = [];
    let last = null;
    for (const x of out) {
      if (x.thread !== last) {
        const t = st.thread(x.thread);
        rows.push(h('div', { class: 'i-line i-join' }, h('button', { type: 'button', class: 'i-link', onclick: () => { topicThread = x.thread; render(st); } },
          `-- thread: ${t?.title || '?'}`)));
        last = x.thread;
      }
      rows.push(h('div', { class: 'i-line' }, h('span', { class: 'i-ts' }, `[${hhmm(x.ts)}] `), x.el));
    }
    return { rows, lastThread: last };
  }

  function statusLines(st) {
    const S = st.S;
    const items = [`*** Connected to ${S.board.name} as ${S.me?.handle}`,
      `*** ${S.channels.length} channels, ${S.threads.size} threads, ${S.tasks.size} tasks`,
      '*** This window is read-only. /commands are disabled.',
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
    return [`Topic: ${t.title} | ${task ? `state=${task.state}` : `${t.message_count} messages`}`,
      c ? [` | ${st.num(task.id)} claimed by ${st.handle(c.actor_id)} (`, leaseEl, ')'] : '',
      ' | ', h('a', { href: threadHref(t.id) }, 'open thread')];
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
    const textArea = root.querySelector('.i-text');
    const atBottom = !textArea || textArea.scrollTop + textArea.clientHeight >= textArea.scrollHeight - 8;
    const { rows, lastThread } = tab === 'status' ? { rows: statusLines(st), lastThread: null } : lines(st);
    const th = topicThread && st.thread(topicThread)?.channel_path === tab ? topicThread : lastThread;
    const actors = [...S.actors.values()].sort((a, b) => a.handle.localeCompare(b.handle));
    const people = actors.filter((a) => a.kind === 'human'), agents = actors.filter((a) => a.kind !== 'human');
    const threadsHere = [...S.threads.values()].filter((t) => t.channel_path === tab).length;
    const ro = { disabled: true, title: 'read-only viewer' };
    const win = h('div', { class: 'i-win' },
      h('div', { class: 'i-title' }, h('span', { class: 'i-icon' }, 'A'),
        h('span', { class: 'i-titletext' }, `Abby IRC - [${tab === 'status' ? 'Status' : `#${tab} [${threadsHere}]`}]${st.thread(th) ? `: ${st.thread(th).title}` : ''}`),
        ['_', '□', '×'].map((x) => h('button', { type: 'button', class: 'i-wbtn', ...ro }, x))),
      h('div', { class: 'i-menu' }, MENUS.map((m) => h('span', { title: 'read-only viewer' }, h('u', {}, m[0]), m.slice(1)))),
      h('div', { class: 'i-tools' }, TOOLS.map((x) => h('button', { type: 'button', class: 'i-tool', ...ro }, x))),
      h('div', { class: 'i-tabs', role: 'tablist' }, [['status', 'Status'], ...S.channels.map((c) => [c.path, `#${c.path}`])].map(([key, label]) =>
        h('button', { type: 'button', role: 'tab', 'aria-selected': String(key === tab), class: `i-tab${key === tab ? ' on' : ''}${unread.get(key) ? ' unread' : ''}`,
          onclick: () => select(key) }, label, unread.get(key) ? ` (${unread.get(key)})` : ''))),
      h('div', { class: 'i-topic' }, topic(st, th)),
      h('div', { class: 'i-body' }, h('div', { class: 'i-text', role: 'log' }, rows),
        h('div', { class: 'i-nicks' }, people.map((a) => h('div', { class: 'i-person' }, `@${a.handle}`)),
          agents.map((a) => h('div', { class: 'i-agent' }, `+${a.handle}`)),
          h('div', { class: 'i-count' }, `--- ${actors.length} here`), h('div', { class: 'i-count' }, ` ${people.length} people (@)`),
          h('div', { class: 'i-count' }, ` ${agents.length} agents (+)`))),
      h('input', { class: 'i-input', type: 'text', disabled: true, 'aria-label': 'message input (read-only)', placeholder: 'read-only viewer: /commands and messages are disabled' }),
      h('div', { class: 'i-statusbar' }, h('span', {}, `Connected to ${S.board.name} as ${S.me?.handle}`),
        h('span', {}, 'Ctrl-K jump'), h('span', {}, `${S.channels.length} channels`)));
    root.replaceChildren(h('div', { class: 'i-desk' }, win));
    paintLease();
    const text = root.querySelector('.i-text');
    if (atBottom) text.scrollTop = text.scrollHeight;
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
    if (LOGGED[ev.type]) log.push({ ts: ev.recorded_at, thread: ev.thread_id, channel, text: `* ${st.handle(ev.actor_id)} ${LOGGED[ev.type](p)} ${st.num(p.task.id)}`,
      el: [h('span', { class: 'i-act' }, `* ${st.handle(ev.actor_id)} ${LOGGED[ev.type](p)} ${st.num(p.task.id)}`)] });
    if (log.length > 300) log.shift();
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
    dispose: () => { palette?.dispose(); document.removeEventListener('keydown', onKey); },
  };
}
