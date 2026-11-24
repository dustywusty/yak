// Read-only Lanes: task state is the list, not a guess about progress.
import { h, avatar, gateHint } from './skin-kit.js';
import { createAgentActivity, waitingReasons, elapsed, activityClock } from './agent-activity.js';

const COLUMNS = [
  ['ready', 'Ready'], ['claimed', 'Claimed'], ['waiting', 'Waiting'], ['blocked', 'Blocked'],
  ['completed', 'Completed'], ['failed', 'Failed'], ['canceled', 'Canceled'],
];
const TOASTS = { 'task.claimed': 'claimed', 'task.completed': 'completed', 'task.failed': 'failed', 'task.blocked': 'blocked', 'task.released': 'released' };

export function wall(root, { boardId, route, threadHref }) {
  const filters = r => ({ ch: r.ch, agent: r.agent, label: r.label });
  let f = filters(route), restore = history.state?.lanes;
  function setFilters(changes, st) {
    const [path] = location.hash.split('?', 1);
    const query = new URLSearchParams(location.hash.slice(path.length + 1));
    query.set('b', boardId);
    for (const [key, value] of Object.entries(changes)) {
      f[key] = value;
      if (value) query.set(key, value);
      else query.delete(key);
    }
    history.replaceState({ ...history.state, lanes: null }, '', path + '?' + query);
    render(st);
  }

  function rememberCard(event) {
    const link = event.target.closest('a');
    const card = link?.closest('[data-task]');
    if (!card || event.defaultPrevented || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    const list = card.closest('.w-list');
    history.replaceState({ ...history.state, lanes: { task: card.dataset.task, link: [...card.querySelectorAll('a')].indexOf(link),
      left: frame.cols.scrollLeft, top: list.scrollTop } }, '');
  }

  let frame = null, filterKey = '', status = 'resyncing', activity = null, clocks = [];

  function laneMenu() {
    const jump = h('select', { class: 'w-jump', 'aria-label': 'Jump to lane' },
      h('option', { value: '', disabled: true, selected: true, hidden: true }, 'Jump to lane'),
      COLUMNS.map(([key]) => h('option', { value: key })));
    jump.addEventListener('change', () => {
      const lane = frame.cols.children[jump.selectedIndex - 1];
      if (!lane) return;
      frame.cols.scrollLeft += lane.getBoundingClientRect().left - frame.cols.getBoundingClientRect().left;
      lane.querySelector('h2').focus({ preventScroll: true });
      jump.value = '';
    });
    return jump;
  }

  function controls(st) {
    const sel = (key, first, options) => {
      const s = h('select', { 'aria-label': first, onchange: e => setFilters({ [key]: e.target.value }, st) },
        h('option', { value: '' }, first), options.map(([v, l]) => h('option', { value: v }, l)));
      s.value = f[key];
      return s;
    };
    return h('div', { class: 'w-filters' },
      sel('ch', 'All channels', st.S.channels.map((c) => [c.path, `${c.path}/`])),
      sel('agent', 'All agents', [...st.S.actors.values()].map((a) => [a.id, a.handle])),
      h('input', { type: 'search', placeholder: 'Label filter', title: 'Filter by label key or key:value', 'aria-label': 'Label filter', value: f.label,
        oninput: e => setFilters({ label: e.target.value.trim() }, st) }));
  }

  function keep(st, t) {
    if (f.ch && !(t.channel_path === f.ch || t.channel_path.startsWith(`${f.ch}/`))) return false;
    if (f.agent && ![t.current_claim?.actor_id, t.assignee_actor_id, t.outcome?.by_actor_id, t.last_claim?.actor_id].includes(f.agent)) return false;
    if (f.label) {
      const [k, ...v] = f.label.toLowerCase().split(':');
      const labels = st.labels(t);
      if (!(k in labels) || (v.length && labels[k] !== v.join(':'))) return false;
    }
    return true;
  }

  const person = (st, id) => id ? h('span', { class: 'w-person' }, avatar(st.actor(id), id), h('b', {}, st.handle(id))) : null;
  function card(st, t) {
    const c = t.current_claim;
    const working = t.state === 'claimed' && c?.status === 'active';
    const waiting = ['waiting', 'blocked'].includes(t.state);
    const since = working ? c.acquired_at : waiting ? t.state_changed_at : null;
    const time = since ? h('time', { datetime: since, title: `${since} (UTC)` }) : null;
    const notice = h('span', { class: 'w-unconfirmed' });
    if (time) clocks.push({ t, time, notice, working });
    const reasons = waiting ? waitingReasons(st, t).map((r) => r.task
      ? h('a', { href: threadHref(r.task.thread_id) }, r.text, r.number ? ` · ${r.number} ${r.task.title}` : '')
      : h('span', {}, r.text, r.number ? ` · ${r.number}` : '')) : [];
    const labels = Object.entries(st.labels(t)).filter(([k]) => !k.startsWith('yak.') && !k.startsWith('plan.'));
    const label = ([k, v]) => h('span', { class: 'w-label' }, `${k}: ${v}`);
    return h('article', { class: `w-card w-card-${t.state}`, 'data-task': t.id },
      labels.length ? h('div', { class: 'w-labels' }, labels.map(label)) : null,
      h('a', { class: 'w-title', href: threadHref(t.thread_id) }, t.title),
      h('div', { class: 'w-meta' }, `${st.num(t.id)} · #${t.channel_path}`),
      h('div', { class: 'w-foot' },
        person(st, working ? c.actor_id : st.settled(t) ? t.outcome?.by_actor_id : t.assignee_actor_id || t.block?.by_actor_id),
        time, notice,
        !working && t.state === 'claimed' ? h('span', {}, 'Claim status unconfirmed') : null,
        t.state === 'ready' ? h('span', {}, 'Ready · no active claim') : null,
        waiting ? h('div', { class: 'w-reason' }, reasons) : null,
        st.gated(t) ? h('span', {}, 'Approval pending') : null,
        gateHint(t, st.labels(t)) ? h('span', {}, gateHint(t, st.labels(t))) : null,
        t.outcome ? h('span', { class: 'w-outcome' }, `${t.state}: ${t.outcome.summary || t.outcome.reason || 'See task discussion'}`) : null));
  }

  function tick(st) {
    activity?.update();
    const { at, stale, confirmedAt } = activityClock(st, status);
    for (const { t, time, notice, working } of clocks) {
      const expiry = working ? Date.parse(t.current_claim.expires_at) : Infinity;
      const displayAt = Math.min(confirmedAt, expiry);
      time.textContent = working ? `Claim held for ${elapsed(t.current_claim.acquired_at, displayAt)}` : `${elapsed(t.state_changed_at, displayAt)} in ${t.state} state`;
      time.classList.toggle('w-long', !working && displayAt - Date.parse(t.state_changed_at) >= 3600000);
      time.classList.toggle('w-stale', stale || expiry <= at);
      notice.textContent = working && expiry <= at ? 'Status unconfirmed · last reported claim; checking for updates.' : '';
    }
  }

  function render(st) {
    if (!st.S.board) return;
    if (!frame) {
      const clear = h('button', { type: 'button', '--panel': '#fff', '--line': 'var(--w-line)', onclick: () => {
        filterKey = '';
        setFilters({ ch: '', agent: '', label: '' }, st);
        frame.bar.querySelector('.w-filters select').focus({ preventScroll: true });
      } }, 'Clear filters');
      frame = { clear, head: h('header', { class: 'w-head' }), summary: h('div', { class: 'w-summary' }), bar: h('div', { class: 'w-bar' }), jump: laneMenu(), count: h('span', { class: 'w-count' }), cols: h('div', { class: 'w-cols', onclick: rememberCard, tabindex: '0', 'aria-label': 'Task lists; scroll horizontally for more states' }), toasts: h('div', { class: 'w-toasts', 'aria-live': 'polite' }) };
      root.replaceChildren(h('div', { class: 'w-wrap' }, frame.head, frame.summary, frame.bar, frame.cols), frame.toasts);
    }
    const active = document.activeElement;
    const focusRow = active?.closest('[data-task], [data-activity-task]');
    const focusTask = focusRow?.dataset.task || focusRow?.dataset.activityTask;
    const inSummary = !!active?.closest('.w-summary');
    const linkIndex = focusRow ? [...focusRow.querySelectorAll('a')].indexOf(active) : -1;
    const summaryFocus = active === activity?.root;
    const headingFocus = active?.matches('.w-col h2') ? active.closest('.w-col').dataset.state : null;
    const listFocus = [...frame.cols.querySelectorAll('.w-list')].indexOf(active);
    const filterFocus = active?.closest('.w-filters') ? active.getAttribute('aria-label') : null;
    const selection = filterFocus && active.tagName === 'INPUT' ? [active.selectionStart, active.selectionEnd] : null;
    const summaryTop = activity?.root.scrollTop || 0;
    const scrolls = [...frame.cols.children].map((col) => col.querySelector('.w-list').scrollTop);
    // Rebuild controls only when their option lists change; typing keeps focus.
    const key = `${st.S.channels.map((c) => c.path).join(',')}|${[...st.S.actors.keys()].join(',')}`;
    if (key !== filterKey) {
      filterKey = key;
      frame.bar.replaceChildren(controls(st), frame.count, frame.clear, frame.jump);
      if (active === frame.jump || active === frame.clear) active.focus({ preventScroll: true });
    }
    const all = st.tasks(), shown = all.filter((t) => keep(st, t));
    frame.head.replaceChildren(h('h1', {}, `${st.S.board.name} · Lanes`), h('span', {}, 'Read-only'));
    frame.count.textContent = `Showing ${shown.length} of ${all.length} tasks`;
    frame.clear.hidden = !(f.ch || f.agent || f.label);
    activity = createAgentActivity(st, ({ t }) => threadHref(t), status);
    frame.summary.replaceChildren(activity.root);
    activity.root.scrollTop = summaryTop;
    clocks = [];
    frame.cols.replaceChildren(...COLUMNS.map(([key, title], i) => {
      const list = shown.filter((t) => t.state === key);
      const option = frame.jump.options[i + 1];
      const caption = `${title} (${list.length})`;
      if (option.textContent !== caption) option.textContent = caption;
      if (['completed', 'failed', 'canceled'].includes(key)) list.sort((a, b) => (b.outcome?.at || '').localeCompare(a.outcome?.at || ''));
      const items = h('div', { class: 'w-list', tabindex: '0', 'aria-label': `${title} tasks` }, list.map((t) => card(st, t)),
        !list.length ? h('p', { class: 'w-empty' }, 'No tasks') : null);
      const col = h('section', { class: `w-col w-col-${key}`, 'data-state': key }, h('h2', { tabindex: '-1' }, title, h('span', {}, String(list.length))), items);
      return col;
    }));
    // Size clock text before restoring offsets so scroll anchoring cannot move the focused card.
    tick(st);
    // Detached lists cannot take their old scroll offset until they have a layout.
    [...frame.cols.children].forEach((col, i) => { col.querySelector('.w-list').scrollTop = scrolls[i] || 0; });
    if (filterFocus) {
      const control = [...frame.bar.querySelectorAll('input,select')].find((e) => e.getAttribute('aria-label') === filterFocus);
      control?.focus({ preventScroll: true });
      if (selection) control?.setSelectionRange(...selection);
    } else if (focusTask && linkIndex >= 0) {
      const selector = inSummary ? '[data-activity-task]' : '[data-task]';
      const row = [...root.querySelectorAll(selector)].find((e) => (e.dataset.task || e.dataset.activityTask) === focusTask);
      (row?.querySelectorAll('a')[linkIndex] || (inSummary ? activity.root : frame.cols)).focus({ preventScroll: true });
    } else if (summaryFocus) activity.root.focus({ preventScroll: true });
    else if (headingFocus) frame.cols.querySelector(`.w-col-${headingFocus} h2`).focus({ preventScroll: true });
    else if (listFocus >= 0) frame.cols.children[listFocus]?.querySelector('.w-list')?.focus({ preventScroll: true });
    if (restore && st.S.cursor) {
      const row = frame.cols.querySelector(`[data-task="${CSS.escape(restore.task)}"]`);
      frame.cols.scrollLeft = restore.left;
      if (row) {
        row.closest('.w-list').scrollTop = restore.top;
        row.querySelectorAll('a')[restore.link]?.focus({ preventScroll: true });
      }
      restore = null;
    }
  }

  function event(ev, st) {
    const verb = TOASTS[ev.type], t = ev.payload?.task;
    if (!verb || !t || !frame) return;
    const toast = h('div', { class: 'w-toast' }, `${st.handle(ev.actor_id)} ${verb} ${st.num(t.id)}`);
    frame.toasts.append(toast);
    const timer = setTimeout(() => { toast.remove(); timers.delete(timer); }, 6000);
    timers.add(timer);
  }
  const timers = new Set();
  function navigate(route, st) {
    f = filters(route);
    filterKey = '';
    restore = history.state?.lanes;
    render(st);
  }
  return { navigate, render, event, tick, status: (value, st) => { if (status !== value) { status = value; render(st); } }, dispose: () => timers.forEach(clearTimeout) };
}
