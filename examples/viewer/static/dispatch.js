// Dispatch: a dark ops board of tasks. Read-only.

import { h, avatar, readOnly, lease, ago, snip, isToday } from './skin-kit.js';

const SEGMENTS = 20;

export function dispatch(root, { threadHref }) {
  let bars = [];

  const chips = (st, t) => {
    const all = Object.entries(st.labels(t)).filter(([k]) => !k.startsWith('abby.'));
    return [all.slice(0, 3).map(([k, v]) => h('span', { class: 'd-chip' }, k, v === '' ? '' : h('b', {}, `=${v}`))),
      all.length > 3 ? h('span', { class: 'd-chip', title: all.slice(3).map(([k, v]) => `${k}=${v}`).join(' ') }, `+${all.length - 3}`) : null];
  };
  const who = (st, id, cls = '') => h('span', { class: `d-who ${cls}` }, avatar(st.actor(id), id), h('b', {}, st.handle(id)),
    st.via(id) ? ` via ${st.via(id)}` : '');
  const replies = (st, t) => { const n = (st.thread(t.thread_id)?.message_count || 1) - 1; return n ? h('span', { class: 'd-muted' }, `${n} repl${n === 1 ? 'y' : 'ies'}`) : null; };

  function head(st, t, ts) {
    return [
      h('div', { class: 'd-row' }, h('span', { class: 'd-num' }, st.num(t.id)), h('span', { class: 'd-chan' }, `#${t.channel_path}`),
        h('span', { class: 'd-spacer' }), h('span', { class: 'd-muted' }, ago(ts || t.state_changed_at))),
      h('a', { class: 'd-title', href: threadHref(t.thread_id) }, t.title),
    ];
  }

  function leaseBox(st, t) {
    const c = t.current_claim;
    const time = h('span', { class: 'd-left' });
    const segs = Array.from({ length: SEGMENTS }, () => h('i'));
    bars.push({ c, time, segs });
    paint(bars.at(-1));
    return h('div', { class: 'd-lease' }, h('div', { class: 'd-row' }, who(st, c.actor_id), h('span', { class: 'd-spacer' }), time),
      h('div', { class: 'd-bar', role: 'progressbar', 'aria-label': 'lease left' }, segs));
  }

  function paint({ c, time, segs }) {
    const l = lease(c);
    time.textContent = l.left <= 0 ? 'LAPSING' : `${l.text} LEFT`;
    time.classList.toggle('d-lapsing', l.left <= 0);
    const on = Math.round(l.frac * SEGMENTS);
    segs.forEach((s, i) => s.classList.toggle('on', i < on));
  }

  const card = (cls, ...kids) => h('article', { class: `d-card ${cls}` }, ...kids);

  function claimedCard(st, t) {
    const m = st.latest(t.thread_id);
    return card('', head(st, t, t.current_claim.acquired_at), leaseBox(st, t),
      m && !m.is_root ? h('p', { class: 'd-progress' }, h('b', {}, `${st.handle(m.author_actor_id)}: `), snip(m.text, 220)) : null,
      h('div', { class: 'd-row d-foot' }, chips(st, t), h('span', { class: 'd-spacer' }), replies(st, t)));
  }

  function gateCard(st, t) {
    const n = st.thread(t.thread_id)?.message_count || 1;
    return card('d-gate', h('div', { class: 'd-row d-gatehead' }, 'AWAITING APPROVAL // GATE', h('span', { class: 'd-spacer' }),
      t.assignee_actor_id ? `${st.handle(t.assignee_actor_id)}` : ''),
      head(st, t, t.block?.at), h('p', {}, h('b', { class: 'd-tag' }, `@${st.handle(t.block?.by_actor_id)}`), ' ', t.block?.reason || ''),
      h('div', { class: 'd-row' }, readOnly('✓ Approve & unblock', 'd-approve'), h('span', { class: 'd-spacer' }),
        h('a', { class: 'd-btn', href: threadHref(t.thread_id) }, `Thread (${n})`)));
  }

  const blockedCard = (st, t) => card('', head(st, t, t.block?.at),
    h('div', { class: 'd-note d-note-blue' }, h('div', { class: 'd-row' }, h('b', {}, (t.block?.cause || 'blocked').toUpperCase()),
      h('span', { class: 'd-spacer' }), who(st, t.block?.by_actor_id)), t.block?.reason || ''),
    h('div', { class: 'd-row d-foot' }, chips(st, t), h('span', { class: 'd-spacer' }), replies(st, t)));

  const failedCard = (st, t) => card('', head(st, t, t.outcome?.at),
    h('div', { class: 'd-note d-note-red d-row' }, snip(t.outcome?.reason || t.outcome?.summary || 'failed', 160), h('span', { class: 'd-spacer' }), readOnly('Reopen')),
    h('div', { class: 'd-row d-foot' }, who(st, t.outcome?.by_actor_id), h('span', { class: 'd-spacer' }), `retries ${t.retry_count}`));

  const readyCard = (st, t) => card('', head(st, t, t.created_at),
    h('div', { class: 'd-row d-foot' }, chips(st, t), t.assignee_actor_id ? h('span', { class: 'd-chip d-for' }, `for ${st.handle(t.assignee_actor_id)}`) : null,
      h('span', { class: 'd-spacer' }), who(st, t.owner_actor_id)));

  const waitingCard = (st, t) => card('', head(st, t, t.created_at),
    h('div', { class: 'd-row d-foot' }, st.blockers(t).map((d) => h('span', { class: 'd-chip d-needs' }, `needs ${st.num(d.task_id)}`)),
      t.unreadable_dependencies_pending ? h('span', { class: 'd-chip d-needs' }, 'needs (hidden)') : null,
      t.assignee_actor_id ? h('span', { class: 'd-chip d-for' }, `for ${st.handle(t.assignee_actor_id)}`) : null));

  const doneCard = (st, t) => card('', head(st, t, t.outcome?.at),
    t.outcome?.summary ? h('div', { class: 'd-note d-note-green' }, snip(t.outcome.summary, 160)) : null,
    h('div', { class: 'd-row d-foot' }, who(st, t.outcome?.by_actor_id), h('span', { class: 'd-spacer' }),
      t.outcome?.evidence?.length ? h('span', { class: 'd-ok' }, `✓ ${t.outcome.evidence.length}`) : null));

  const lane = (no, title, sub, count, cls, cards, empty = 'none') => h('section', { class: `d-lane ${cls}` },
    h('header', { class: 'd-lanehead' }, h('span', { class: 'd-no' }, no), h('b', {}, title), h('span', { class: 'd-muted' }, sub),
      h('span', { class: 'd-spacer' }), h('span', { class: 'd-count' }, String(count).padStart(2, '0'))),
    cards.flat(Infinity).filter(Boolean).length ? cards : h('p', { class: 'd-empty' }, empty));
  const sub = (title, count, cls, cards) => cards.length ? [h('div', { class: `d-sub ${cls}` }, h('span', {}, title), h('span', {}, String(count).padStart(2, '0'))), cards] : [];

  function render(st) {
    const { S } = st;
    if (!S.board) return;
    const all = st.tasks();
    const by = (s) => all.filter((t) => t.state === s);
    const claimed = by('claimed'), gates = all.filter(st.gated), blocked = by('blocked').filter((t) => !st.gated(t));
    const failed = by('failed'), ready = by('ready'), waiting = by('waiting');
    const done = by('completed').filter((t) => isToday(t.outcome?.at || t.state_changed_at)).reverse();
    const settledToday = all.filter((t) => st.settled(t) && isToday(t.state_changed_at)).length;
    const holders = new Map(claimed.map((t) => [t.current_claim.actor_id, t]));
    const agents = [...S.actors.values()].filter((a) => a.kind !== 'human' && a.id !== S.me?.id);
    const edges = all.filter((t) => !st.settled(t) && t.dependencies.length);
    const stat = (n, label, cls = '') => h('div', { class: `d-stat ${cls}` }, h('b', {}, String(n).padStart(2, '0')), label);
    bars = [];
    root.replaceChildren(h('div', { class: 'd-wrap' },
      h('header', { class: 'd-top' },
        h('div', { class: 'd-brand' }, h('b', {}, h('i', { class: 'd-sq' }), 'BOARD // DISPATCH'),
          h('div', { class: 'd-muted' }, `${S.board.name} / `, h('span', { class: 'd-live' }, `LIVE · ${S.events} events`))),
        stat(all.length - all.filter(st.settled).length, 'OPEN'), stat(claimed.length, 'LEASED'),
        stat(gates.length, 'HUMAN GATE', gates.length ? 'd-hot' : ''), stat(settledToday, 'SETTLED TODAY', 'd-good')),
      h('div', { class: 'd-agents' }, agents.map((a) => {
        const t = holders.get(a.id);
        return h('div', { class: 'd-agent' }, avatar(a), h('div', {},
          h('b', {}, h('i', { class: t ? 'd-dot on' : 'd-dot' }), a.handle.toUpperCase()),
          h('div', { class: 'd-muted' }, a.owner_actor_id ? `via ${st.handle(a.owner_actor_id)} · ` : '', t ? st.num(t.id) : 'idle')));
      })),
      h('div', { class: 'd-grid' },
        lane('01', 'CLAIMED', 'active leases', claimed.length, 'd-hotlane', claimed.map((t) => claimedCard(st, t)), 'no active leases'),
        lane('02', 'BLOCKED', 'human gates', gates.length + blocked.length, '', [
          gates.map((t) => gateCard(st, t)), blocked.map((t) => blockedCard(st, t)),
          ...sub('FAILED // RETRYABLE', failed.length, 'd-sub-red', failed.map((t) => failedCard(st, t)))], 'nothing blocked'),
        lane('03', 'READY & WAITING', 'oldest first', ready.length + waiting.length, '', [
          ready.map((t) => readyCard(st, t)),
          ...sub('WAITING // DEPENDENCY LOCK', waiting.length, '', waiting.map((t) => waitingCard(st, t)))]),
        lane('04', 'COMPLETED', 'settled today', done.length, 'd-goodlane', [
          done.slice(0, 6).map((t) => doneCard(st, t)),
          done.length > 6 ? h('p', { class: 'd-empty' }, `+${done.length - 6} more`) : null], 'nothing settled today')),
      h('footer', { class: 'd-footer' },
        h('div', { class: 'd-row d-wrapline' }, h('b', { class: 'd-muted' }, 'DEPENDENCIES'),
          edges.length ? edges.map((t) => h('span', { class: 'd-edge' },
            t.dependencies.map((d) => h('span', { class: d.state === 'completed' ? 'd-ok' : 'd-red' }, st.num(d.task_id))).flatMap((x, i) => (i ? [' + ', x] : [x])),
            ` → ${st.num(t.id)}`)) : h('span', { class: 'd-muted' }, 'none open')),
        h('div', { class: 'd-row d-wrapline' }, h('b', { class: 'd-good' }, 'WATCHERS'),
          h('span', { class: 'd-muted' }, 'saved filters are not in the protocol yet (P3 subscriptions)')))));
  }

  return { render, tick: () => bars.forEach(paint) };
}
