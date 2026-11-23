// Board, channel and thread links use the same live board store.
import { elapsed, waitingReasons } from './agent-activity.js';
import { h } from './skin-kit.js';

const unavailableThread = 'Thread unavailable.';
const date = (ts) => h('time', { datetime: ts }, new Date(ts).toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' }));
const count = (n, noun) => n + ' ' + noun + (n === 1 ? '' : 's');

export function forum(root, { boardId, route }) {
  const targetsTask = () => new URLSearchParams(location.hash.split('?')[1]).get('section') === 'task';
  let taskTarget = targetsTask(), focusHeading = taskTarget;
  let request, loaded = false, error = '', ready = false;
  const href = (ch, t, task = false) => {
    const q = new URLSearchParams({ b: boardId });
    if (ch) q.set('ch', ch);
    if (t) q.set('t', t);
    if (task) q.set('section', 'task');
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
      if (!controller.signal.aborted) error = e.status === 404 || e.status === 403 ? unavailableThread : 'Could not load posts.';
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
  function taskDetails(st, thread) {
    if (!thread.task_id && !taskTarget) return null;
    const task = st.S.tasks.get(thread.task_id);
    const heading = h('h3', { id: 'forum-task-heading', tabindex: '-1' },
      task ? `Task ${st.num(task.id)} · ${task.title}` : 'Task details unavailable.');
    const panel = h('section', { class: 'f-summary', 'aria-labelledby': heading.id }, heading);
    if (!task) return panel;
    const person = id => {
      const actor = st.actor(id);
      return actor ? '@' + actor.handle : id;
    };
    panel.append(h('div', { class: 'f-meta' }, h('strong', {}, task.state),
      task.assignee_actor_id && h('span', {}, 'Assigned to ' + person(task.assignee_actor_id)),
      st.gated(task) && h('span', {}, 'Approval pending')));
    if (task.state === 'blocked' || task.state === 'waiting') {
      for (const reason of waitingReasons(st, task)) {
        const dep = reason.task;
        const target = dep && dep.id !== task.id && st.thread(dep.thread_id);
        panel.append(h('div', { class: 'f-meta' }, reason.text,
          target && h('a', { href: href(target.channel_path, target.id, true) }, reason.number + ' ' + dep.title)));
      }
    }
    const claim = task.current_claim;
    if (task.state === 'claimed' && claim?.status === 'active' && st.S.lastSync) {
      const at = st.S.lastSync;
      panel.append(h('div', { class: 'f-meta' }, Date.parse(claim.expires_at) > at
        ? [`Claim held by ${person(claim.actor_id)} for ${elapsed(claim.acquired_at, at)} as of `, date(new Date(at).toISOString())]
        : 'Claim status unconfirmed'));
    }
    return panel;
  }
  function render(st) {
    if (!ready || !st.S.cursor) return;
    const focused = root.contains(document.activeElement) ? document.activeElement : null;
    const focusedCard = focused?.closest('.f-summary[data-thread]');
    const focusRegion = ['.f-head', '.f-directory', '.f-content'].find(selector => focused?.closest(selector));
    const channels = st.S.channels;
    const thread = st.thread(route.t);
    const ch = route.ch || thread?.channel_path;
    const validChannel = !ch || channels.some(c => c.path === ch);
    const returnToIndex = () => h('a', { href: href(validChannel ? ch : null) },
      validChannel && ch ? `Return to #${ch} index` : 'Return to thread index');
    const threads = route.t ? [] : [...st.S.threads.values()].filter(t => !ch || t.channel_path === ch)
      .sort((a,b) => b.updated_at.localeCompare(a.updated_at) || a.id.localeCompare(b.id));
    const directory = h('nav', { class: 'f-directory', 'aria-label': 'Channel directory' },
      h('h2', {}, 'Channels'), h('ul', {}, channels.map(c => h('li', {},
        h('a', { href: href(c.path), 'aria-current': ch === c.path ? 'location' : null }, '#' + c.path)))),
      channels.length ? null : h('p', {}, 'No readable channels.'));
    const taskLink = t => {
      const task = st.S.tasks.get(t.task_id);
      return task && h('a', { class: 'f-task', 'data-control': 'task', href: href(t.channel_path, t.id, true) }, 'Task ' + st.num(task.id) + ' · ' + task.state);
    };
    const section = h('section', { class: 'f-content' });
    if (!validChannel || (route.t && (!thread || thread.channel_path !== ch))) {
      section.append(h('h2', {}, route.t ? unavailableThread : 'Channel unavailable.'), h('p', {}, returnToIndex()));
    } else if (route.t) {
      section.append(h('h2', {}, thread.title), h('div', { class: 'f-meta' },
        h('a', { href: href(ch) }, '#' + ch),
        document.body.dataset.operator && h('button', { type: 'button', onclick: () =>
          window.dispatchEvent(new CustomEvent('yak-operator-open', { detail: { board: boardId, ch, t: thread.id } })) }, 'Reply / task actions')));
      const details = taskDetails(st, thread);
      if (details) section.append(details);
      if (!request) load(st, thread);
      if (error) section.append(h('p', { role: 'status' }, error, ' ',
        error === unavailableThread && [returnToIndex(), ' · '], h('button', { onclick: () => { reset(); render(st); } }, 'Retry')));
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
          ch && [' / ', h('a', { href: href(ch) }, '#' + ch)]),
        h('small', {}, document.body.dataset.operator ? 'Read-only view · Operator tools available' : 'Read-only view')),
      h('div', { class: 'f-columns' }, directory, section)));
    const taskHeading = section.querySelector('#forum-task-heading');
    if (focusHeading) {
      ((taskTarget && taskHeading) || section.firstChild).focus();
      focusHeading = false;
    } else if (focused?.id === 'forum-task-heading') {
      (taskHeading || section.firstChild).focus({ preventScroll: true });
    } else if (focusedCard) {
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
      const target = targetsTask();
      if (next.t === route.t && next.ch === route.ch && taskTarget === target) return;
      taskTarget = target;
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
