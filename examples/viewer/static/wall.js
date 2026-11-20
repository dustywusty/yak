// Tasks wall: a light kanban of tasks with filters, toasts, and cards that
// move between columns on live events. Read-only (no drag-to-claim).

import { h, avatar, lease, ago, snip } from './skin-kit.js';

const COLUMNS = [
  ['ready', 'Ready'], ['claimed', 'Claimed'], ['completed', 'Completed'],
  ['waiting', 'Waiting'], ['blocked', 'Blocked'],
];
const column = (t) => (t.state === 'failed' || t.state === 'canceled' ? 'completed' : t.state);
const TOASTS = { 'task.claimed': 'claimed', 'task.completed': 'completed', 'task.failed': 'failed', 'task.blocked': 'blocked', 'task.released': 'released' };

export function wall(root, { threadHref }) {
  const f = { channel: '', agent: '', label: '' };
  let bars = [], frame = null, filterKey = '';

  function controls(st) {
    const sel = (key, first, options) => {
      const s = h('select', { 'aria-label': first, onchange: (e) => { f[key] = e.target.value; render(st); } },
        h('option', { value: '' }, first), options.map(([v, l]) => h('option', { value: v }, l)));
      s.value = f[key];
      return s;
    };
    const label = h('input', { type: 'search', placeholder: 'Label (key or key:value)', 'aria-label': 'Label filter', value: f.label,
      oninput: (e) => { f.label = e.target.value.trim(); render(st); } });
    return h('div', { class: 'w-filters' },
      sel('channel', 'All channels', st.S.channels.map((c) => [c.path, `${c.path}/`])),
      sel('agent', 'All agents', [...st.S.actors.values()].map((a) => [a.id, a.handle])),
      label);
  }

  function keep(st, t) {
    if (f.channel && !(t.channel_path === f.channel || t.channel_path.startsWith(`${f.channel}/`))) return false;
    if (f.agent && ![t.current_claim?.actor_id, t.assignee_actor_id, t.outcome?.by_actor_id, t.last_claim?.actor_id].includes(f.agent)) return false;
    if (f.label) {
      const [k, ...v] = f.label.toLowerCase().split(':');
      const labels = st.labels(t);
      if (!(k in labels) || (v.length && labels[k] !== v.join(':'))) return false;
    }
    return true;
  }

  const person = (st, id, text) => h('span', { class: 'w-person' }, avatar(st.actor(id), id), h('b', {}, st.handle(id)), text);

  function card(st, t) {
    const c = t.current_claim;
    const body = [h('div', { class: 'w-num' }, st.num(t.id)), h('a', { class: 'w-title', href: threadHref(t.thread_id) }, t.title)];
    let foot;
    if (t.state === 'claimed') {
      const fill = h('i');
      bars.push({ c, fill });
      fill.style.width = `${lease(c).frac * 100}%`;
      foot = [person(st, c.actor_id, ` · ${ago(c.acquired_at)}`), h('div', { class: 'w-bar', title: 'lease left' }, fill)];
    } else if (t.state === 'completed' || t.state === 'failed' || t.state === 'canceled') {
      foot = h('span', { class: `w-meta w-${t.state}` }, t.state === 'completed' ? '✓ ' : '✗ ', st.handle(t.outcome?.by_actor_id),
        t.state === 'completed' ? '' : ` · ${t.state}`);
    } else if (t.state === 'waiting') {
      const needs = st.blockers(t).map((d) => st.num(d.task_id));
      foot = h('span', { class: 'w-meta' }, '◷ Needs ', h('b', {}, needs.join(', ') || 'hidden'),
        t.assignee_actor_id ? ` · for ${st.handle(t.assignee_actor_id)}` : '');
    } else if (t.state === 'blocked') {
      foot = [h('p', { class: 'w-reason' }, snip(t.block?.reason, 120)),
        person(st, t.assignee_actor_id || t.block?.by_actor_id, st.gated(t) ? ' · needs approval' : ' · held')];
    } else {
      const ls = Object.entries(st.labels(t)).filter(([k]) => !k.startsWith('yak.') && !k.startsWith('plan.')).slice(0, 2);
      foot = h('span', { class: 'w-meta' }, '◌ ', t.assignee_actor_id ? `for ${st.handle(t.assignee_actor_id)}` : 'Unclaimed',
        ls.map(([k, v]) => ` · ${k}=${v}`).join(''));
    }
    return h('article', { class: `w-card w-card-${t.state}`, 'data-task': t.id }, body, h('div', { class: 'w-foot' }, foot));
  }

  function render(st) {
    const { S } = st;
    if (!S.board) return;
    if (!frame) {
      frame = { head: h('header', { class: 'w-head' }), bar: h('div'), cols: h('div', { class: 'w-cols' }), toasts: h('div', { class: 'w-toasts', 'aria-live': 'polite' }) };
      root.replaceChildren(h('div', { class: 'w-wrap' }, frame.head, frame.bar, frame.cols), frame.toasts);
    }
    // Rebuild the filter lists when channels or actors change (they can appear later).
    const key = `${S.channels.map((c) => c.path).join(',')}|${[...S.actors.keys()].join(',')}`;
    if (key !== filterKey) { filterKey = key; frame.bar.replaceChildren(controls(st)); }
    // Header: N of M working, with an avatar stack.
    const all = st.tasks();
    const working = new Set(all.filter((t) => t.state === 'claimed').map((t) => t.current_claim.actor_id));
    const agents = [...S.actors.values()].filter((a) => a.kind !== 'human' && a.id !== S.me?.id);
    frame.head.replaceChildren(h('h1', {}, 'Tasks wall'), h('span', { class: 'w-spacer' }),
      h('span', { class: 'w-working' }, `${agents.filter((a) => working.has(a.id)).length} of ${agents.length} working`),
      h('span', { class: 'w-stack' }, agents.map((a) => h('span', { class: working.has(a.id) ? 'on' : '' }, avatar(a)))));

    // FLIP: remember where each card was, rebuild, then animate from there.
    const before = new Map([...frame.cols.querySelectorAll('[data-task]')].map((e) => [e.dataset.task, e.getBoundingClientRect()]));
    bars = [];
    const shown = all.filter((t) => keep(st, t));
    frame.cols.replaceChildren(...COLUMNS.map(([key, title]) => {
      let list = shown.filter((t) => column(t) === key);
      if (key === 'completed') list = list.sort((a, b) => (b.outcome?.at || '').localeCompare(a.outcome?.at || ''));
      const extra = key === 'completed' && list.length > 12 ? list.length - 12 : 0;
      return h('section', { class: `w-col w-col-${key}` }, h('h2', {}, title, h('span', {}, String(list.length))),
        list.slice(0, key === 'completed' ? 12 : undefined).map((t) => card(st, t)),
        extra ? h('p', { class: 'w-more' }, `+${extra} more`) : null,
        list.length ? null : h('div', { class: 'w-slot' }));
    }));
    for (const el of frame.cols.querySelectorAll('[data-task]')) {
      const old = before.get(el.dataset.task);
      if (!old) { if (before.size) el.classList.add('w-new'); continue; }
      const now = el.getBoundingClientRect();
      const dx = old.left - now.left, dy = old.top - now.top;
      if (!dx && !dy) continue;
      el.animate([{ transform: `translate(${dx}px, ${dy}px)` }, { transform: 'none' }], { duration: 450, easing: 'cubic-bezier(.2,.7,.2,1)' });
    }
  }

  function event(ev, st) {
    const verb = TOASTS[ev.type];
    const t = ev.payload?.task;
    if (!verb || !t || !frame) return;
    const by = ev.actor_id, via = st.via(by);
    const leaseText = ev.type === 'task.claimed' && ev.payload.claim ? ` · ${Math.round(ev.payload.claim.lease_seconds / 60)} min lease` : '';
    const toast = h('div', { class: 'w-toast' }, avatar(st.actor(by), by),
      h('span', {}, h('b', {}, st.handle(by)), via ? ` via ${via}` : '', ` ${verb} ${st.num(t.id)}${leaseText}`));
    frame.toasts.append(toast);
    setTimeout(() => toast.remove(), 6000);
  }

  return {
    render, event,
    tick: () => bars.forEach(({ c, fill }) => { fill.style.width = `${lease(c).frac * 100}%`; }),
  };
}
