// Board, channel and thread links use the same live board store.
import { h } from './skin-kit.js';

const date = (ts) => h('time', { datetime: ts }, new Date(ts).toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' }));
const count = (n, noun) => n + ' ' + noun + (n === 1 ? '' : 's');

export function forum(root, { boardId, route }) {
  let request, loaded = false, error = '', ready = false, focusHeading = false;
  const href = (ch, t) => {
    const q = new URLSearchParams({ b: boardId });
    if (ch) q.set('ch', ch);
    if (t) q.set('t', t);
    return '#/forum?' + q;
  };
  function reset() {
    request?.abort();
    request = null;
    loaded = false;
    error = '';
  }
  function load(st, thread) {
    const controller = new AbortController();
    request = controller;
    st.loadMessages(thread.id, controller.signal).then(() => { if (!controller.signal.aborted) loaded = true; }).catch(e => {
      if (!controller.signal.aborted) error = e.status === 404 || e.status === 403 ? 'Thread unavailable.' : 'Could not load posts.';
    }).finally(() => {
      if (!controller.signal.aborted) render(st);
    });
  }
  function post(st, m, thread) {
    const actor = id => {
      const a = st.actor(id);
      return a ? '@' + a.handle + ' (' + a.kind + ')' : id;
    };
    const opening = m.id === thread.root_message_id || m.is_root;
    return h('article', { class: opening ? 'f-opening' : 'f-reply' },
      h('header', {}, h('strong', { class: 'f-author' }, actor(m.author_actor_id),
        m.on_behalf_of_actor_id ? ' for ' + actor(m.on_behalf_of_actor_id) : ''), date(m.created_at),
        h('span', { title: m.id }, 'No. ' + m.seq)),
      h('p', {}, m.deleted ? 'Message deleted.' : m.text));
  }
  function render(st) {
    if (!ready || !st.S.cursor) return;
    const focused = root.contains(document.activeElement) ? document.activeElement : null;
    const focusedCard = focused?.closest('.f-summary');
    const focusRegion = ['.f-head', '.f-directory', '.f-content'].find(selector => focused?.closest(selector));
    const channels = st.S.channels;
    const thread = st.thread(route.t);
    const ch = route.ch || thread?.channel_path;
    const validChannel = !ch || channels.some(c => c.path === ch);
    const threads = route.t ? [] : [...st.S.threads.values()].filter(t => !ch || t.channel_path === ch)
      .sort((a,b) => b.updated_at.localeCompare(a.updated_at) || a.id.localeCompare(b.id));
    const directory = h('nav', { class: 'f-directory', 'aria-label': 'Channel directory' },
      h('h2', {}, 'Channels'), h('ul', {}, channels.map(c => h('li', {},
        h('a', { href: href(c.path), 'aria-current': ch === c.path ? 'location' : null }, '#' + c.path)))),
      channels.length ? null : h('p', {}, 'No readable channels.'));
    const taskLink = t => {
      const task = st.S.tasks.get(t.task_id);
      return task && h('a', { class: 'f-task', 'data-control': 'task', href: href(t.channel_path, t.id) }, 'Task ' + st.num(task.id) + ' · ' + task.state);
    };
    const section = h('section', { class: 'f-content' });
    if (!validChannel || (route.t && (!thread || thread.channel_path !== ch))) {
      section.append(h('h2', {}, route.t ? 'Thread unavailable.' : 'Channel unavailable.'));
    } else if (route.t) {
      section.append(h('h2', {}, thread.title), h('div', { class: 'f-meta' },
        h('a', { href: href(ch) }, '#' + ch), taskLink(thread),
        document.body.dataset.operator && h('button', { type: 'button', onclick: () =>
          window.dispatchEvent(new CustomEvent('yak-operator-open', { detail: { board: boardId, ch, t: thread.id } })) }, 'Reply / task actions')));
      if (!request) load(st, thread);
      if (error) section.append(h('p', { role: 'status' }, error, ' ', h('button', { onclick: () => { reset(); render(st); } }, 'Retry')));
      else if (!loaded) section.append(h('p', { role: 'status' }, 'Loading posts…'));
      else {
        const posts = st.messages(thread.id);
        for (const m of posts) section.append(post(st, m, thread));
        if (!posts.length) section.append(h('p', {}, 'No posts.'));
      }
    } else {
      section.append(h('h2', {}, ch ? '#' + ch : 'All threads', h('small', {}, count(threads.length, 'thread'))));
      for (const t of threads) {
        const opening = st.S.msgs.get(t.id)?.get(t.root_message_id);
        section.append(h('article', { class: 'f-summary', 'data-thread': t.id },
          h('h3', {}, h('a', { 'data-control': 'title', href: href(t.channel_path, t.id) }, t.title)),
          h('div', { class: 'f-meta' }, h('a', { 'data-control': 'channel', href: href(t.channel_path) }, '#' + t.channel_path),
            h('span', {}, t.message_count === 2 ? '1 reply' : Math.max(0, t.message_count - 1) + ' replies'),
            date(t.updated_at), taskLink(t)),
          opening && !opening.deleted && h('p', {}, opening.text)));
      }
      if (!threads.length) section.append(h('p', {}, 'No threads.'));
    }
    section.firstChild.tabIndex = -1;
    root.replaceChildren(h('div', { class: 'f-page' },
      h('header', { class: 'f-head' }, h('h1', {}, st.S.board.name, h('span', {}, ' / Forum')),
        h('nav', { 'aria-label': 'Forum navigation' }, h('a', { href: href() }, 'Thread index'),
          ch && [' / ', h('a', { href: href(ch) }, '#' + ch)])),
      h('div', { class: 'f-columns' }, directory, section)));
    if (focusHeading) { section.firstChild.focus(); focusHeading = false; }
    else if (focusedCard) {
      const card = section.querySelector(`.f-summary[data-thread="${CSS.escape(focusedCard.dataset.thread)}"]`);
      const control = card?.querySelector(`[data-control="${focused.dataset.control}"]`);
      (control || section.firstChild).focus({ preventScroll: true });
    } else if (focusRegion) {
      const next = [...root.querySelector(focusRegion).querySelectorAll('a,button,h2')].find(el =>
        el.tagName === focused.tagName && el.textContent === focused.textContent && el.href === focused.href);
      next?.focus({ preventScroll: true });
    }
  }
  return {
    render,
    navigate(next, st) {
      if (next.t === route.t && next.ch === route.ch) return;
      reset(); route = next; focusHeading = true; root.scrollTop = 0; render(st);
    },
    status(state) {
      if (state === 'live') ready = true;
      if (state === 'resyncing' || state === 'unavailable' || (!ready && state === 'reconnecting')) {
        ready = false;
        reset();
        root.replaceChildren(h('p', { class: 'pad', role: 'status' }, state === 'unavailable' ? 'Board unavailable.' :
          state === 'reconnecting' ? 'Could not load board. Reconnecting…' : 'Loading board…'));
      }
    },
    dispose: reset,
  };
}
