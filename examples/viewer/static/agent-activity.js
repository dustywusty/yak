// Board activity from task views, not process presence or inferred progress.
// Kept separate so other views can reuse the same waiting reasons and clocks.
import { h, now } from './skin-kit.js';

export function elapsed(since, at) {
  const ms = at - Date.parse(since);
  if (!Number.isFinite(ms)) return 'Time unknown';
  const s = Math.max(0, Math.floor(ms / 1000));
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ${Math.floor(s % 3600 / 60)}m`;
  return `${Math.floor(s / 86400)}d ${Math.floor(s % 86400 / 3600)}h`;
}

export function waitingReasons(store, task) {
  const reasons = [];
  if (task.block?.reason) reasons.push({ text: task.block.reason, task });
  for (const dep of task.dependencies || []) {
    const target = store.S.tasks.get(dep.task_id);
    if ((target?.state || dep.state) === 'completed') continue;
    const labels = target ? store.labels(target) : {};
    const assignee = target?.assignee_actor_id;
    const person = assignee ? `@${store.handle(assignee)}${assignee === store.S.me?.id ? ' (you)' : ''}` : null;
    const need = labels.needs;
    const reason = need === 'owner' ? `Waiting on owner${person ? ` ${person}` : ''}`
      : need ? `Waiting on ${need}${person ? ` · assigned to ${person}` : ''}`
        : labels.kind === 'review' ? `Waiting on review${person ? ` by ${person}` : ''}`
          : `Waiting on prerequisite${person ? ` assigned to ${person}` : ''}`;
    // The dependency projection has no thread/title; never invent one.
    reasons.push({ text: reason, task: target, number: store.num(dep.task_id) });
  }
  if (task.unreadable_dependencies_pending) reasons.push({ text: 'Prerequisite pending (details not visible)' });
  if (!reasons.length) reasons.push({ text: task.state === 'blocked' ? 'Blocked; see task discussion' : 'Prerequisite pending', task });
  return reasons;
}

// Shared freshness boundary for summaries and Lanes card clocks.
export function activityClock(store, status, at = now()) {
  const stale = status !== 'live' || !store.S.lastSync || at - store.S.lastSync > 60000 || navigator.onLine === false;
  return { at, stale, confirmedAt: stale ? Math.min(at, store.S.lastSync || at) : at };
}

export function createAgentActivity(store, href, status) {
  const tasks = store.tasks();
  const claimed = tasks.filter((t) => t.state === 'claimed' && t.current_claim?.status === 'active');
  const groups = new Map();
  for (const t of claimed) {
    const id = t.current_claim.actor_id;
    groups.set(id, [...(groups.get(id) || []), t]);
  }
  const waiting = tasks.filter((t) => ['waiting', 'blocked'].includes(t.state))
    .sort((a, b) => a.state_changed_at.localeCompare(b.state_changed_at));
  const noClaim = [...store.S.actors.values()].filter((a) => a.kind === 'agent' && !groups.has(a.id));
  const link = (task, text) => h('a', { href: href({ board: task.board_id, ch: task.channel_path, t: task.thread_id }) }, text);
  const duration = (since, prefix, suffix = '') => h('time', { 'data-since': since, 'data-prefix': prefix, 'data-suffix': suffix,
    datetime: since, title: `${since} (UTC)` });
  const counts = h('span', { class: 'a-counts' });
  const freshness = h('p', { class: 'a-freshness', role: 'status' });
  const workingRows = [...groups].map(([id, held]) => {
    const rows = held.map((t) => h('li', { 'data-activity-task': t.id, 'data-expires': t.current_claim.expires_at },
      link(t, `${store.num(t.id)} ${t.title}`), duration(t.current_claim.acquired_at, 'Working for '), h('span', { class: 'a-unconfirmed' })));
    return h('div', { class: 'a-agent' }, h('b', {}, store.handle(id)), h('ul', {}, rows));
  });
  const waitingRows = waiting.map((t) => {
    const reasons = waitingReasons(store, t).map((r) => r.task
      ? link(r.task, `${r.text}${r.number ? ` · ${r.number} ${r.task.title}` : ''}`)
      : h('span', {}, `${r.text}${r.number ? ` · ${r.number}` : ''}`));
    return h('li', { 'data-activity-task': t.id }, link(t, `${store.num(t.id)} ${t.title}`),
      h('span', { class: 'a-assignee' }, t.assignee_actor_id ? `Assigned to @${store.handle(t.assignee_actor_id)}` : 'Unassigned'),
      duration(t.state_changed_at, '', ` in ${t.state} state`), h('div', { class: 'a-reasons' }, reasons));
  });
  const root = h('section', { class: 't-agent-activity', 'aria-label': 'Agent activity', tabindex: '0' },
    h('header', { class: 'a-heading' }, h('b', {}, `${store.S.board.name} · Agent activity`), counts), freshness,
    h('div', { class: 'a-columns' },
      h('section', {}, h('h2', {}, 'Working now'), workingRows,
        !groups.size ? h('p', { class: 't-mute' }, 'No active claimed tasks.') : null,
        noClaim.length ? h('p', { class: 'a-unclaimed t-mute' }, `No active claimed task: ${noClaim.map((a) => a.handle).join(', ')}`) : null),
      h('section', {}, h('h2', {}, 'Waiting'), h('ul', {}, waitingRows),
        !waiting.length ? h('p', { class: 't-mute' }, 'No waiting or blocked tasks.') : null)));

  function update() {
    const { at, stale, confirmedAt } = activityClock(store, status);
    root.classList.toggle('a-stale', stale);
    const freshnessText = stale ? `Last known data · ${status === 'live' ? 'connection not confirmed' : status}. Activity may have changed.`
      : 'Current task claims and waits. Times show time in each task state.';
    if (freshness.textContent !== freshnessText) freshness.textContent = freshnessText;
    const active = claimed.filter((t) => Date.parse(t.current_claim.expires_at) > at);
    const actors = new Set(active.map((t) => t.current_claim.actor_id)).size;
    const plural = (n, singular) => `${n} ${singular}${n === 1 ? '' : 's'}`;
    counts.textContent = `${stale ? 'Last known: ' : ''}${plural(actors, 'working actor')} · ${plural(active.length, 'claimed task')} · ${plural(waiting.length, 'waiting task')}${active.length < claimed.length ? ` · ${claimed.length - active.length} to confirm` : ''}`;
    for (const el of root.querySelectorAll('time[data-since]')) {
      const row = el.closest('[data-expires]');
      const expires = row ? Date.parse(row.dataset.expires) : Infinity;
      const unconfirmed = expires <= at;
      const displayAt = Math.min(confirmedAt, expires);
      const text = `${el.dataset.prefix}${elapsed(el.dataset.since, displayAt)}${el.dataset.suffix}`;
      el.textContent = text;
      el.classList.toggle('a-long', !row && displayAt - Date.parse(el.dataset.since) >= 3600000);
      if (row) {
        row.classList.toggle('a-pending', unconfirmed);
        row.querySelector('.a-unconfirmed').textContent = unconfirmed ? 'Status unconfirmed · last reported claim; checking for updates.' : '';
      }
    }
  }
  update();
  return { root, update };
}
