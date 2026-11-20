import { h } from './skin-kit.js';
import { approvalRecord, taskIntent } from './operator.js';
const enc = encodeURIComponent;
const button = (text, click) => h('button', { type: 'button', onclick: click }, text);
const field = (label, node) => h('label', {}, label, node);

// A shared, explicit workspace keeps the source skins and their lifecycle intact.
// Draft nodes survive live store redraws, navigation and conflict refreshes.
export function createOperatorUI({ session, read, route }) {
  let panel, content, status, current, opener, refresh, sequence = 0;
  const drafts = new Map();
  const draftEdits = new WeakMap();
  const receipts = h('div', { class: 'op-receipts' });
  const guards = new WeakMap();
  const valid = (snapshot, version) => current === snapshot && sequence === version && snapshot?.generation === session.generation && session.active && !session.ending;
  function dismissConfirmations() { document.querySelectorAll('.op-confirm').forEach((d) => { d.close(); d.remove(); }); }
  function writeButton(text, click) {
    const snapshot = current, version = sequence, guard = () => valid(snapshot, version);
    const b = button(text, (...args) => { if (guard()) return click(...args); });
    guards.set(b, guard); b.dataset.opWrite = 'true';
    b.disabled = session.pending.size > 0 || session.ending; return b;
  }
  function say(text) { if (status) status.textContent = text; }
  function close() { sequence++; dismissConfirmations(); panel?.remove(); panel = null; current = null; opener?.focus(); }
  async function all(path, key, params = {}) {
    const out = []; let page_token;
    do { const p = await read('GET', path, { ...params, page_token, limit: 200 }); out.push(...(p[key] || [])); page_token = p.next_page_token; } while (page_token);
    return out;
  }
  function actor(id, actors = current?.actors || []) {
    const a = actors.find((a) => a.id === id);
    return a ? `@${a.handle} (${a.kind})` : id ? `${id.slice(0, 16)} (unknown)` : 'none';
  }
  function provenance(m) { return `${actor(m.author_actor_id || m.actor_id)}${m.on_behalf_of_actor_id ? ` for ${actor(m.on_behalf_of_actor_id)}` : ''}`; }
  function draft(label, kind = 'textarea') {
    const key = JSON.stringify([current.B, current.channel, current.thread?.id || null, label]);
    if (!drafts.has(key)) {
      const node = h(kind, { 'aria-label': label });
      draftEdits.set(node, 0);
      node.addEventListener('input', () => draftEdits.set(node, draftEdits.get(node) + 1));
      drafts.set(key, node);
    }
    return drafts.get(key);
  }
  function submittedDrafts(...nodes) {
    const sent = nodes.map((node) => ({ node, value: node.value, edits: draftEdits.get(node) }));
    return () => {
      for (const { node, value, edits } of sent) {
        if (node.value === value && draftEdits.get(node) === edits) node.value = '';
      }
    };
  }
  function rights(cap) { return session.me.credential.scopes.includes('read') && current.permissions.includes('read') && current.permissions.includes(cap) && session.me.credential.scopes.includes(cap); }
  function manager(t) { return rights('task_admin') || (t.owner_actor_id === session.me.actor.id && rights('task_manage')); }
  async function refreshTask() {
    if (!current?.task) return;
    const snapshot = current, version = sequence;
    const task = await read('GET', `${snapshot.B}/tasks/${enc(snapshot.task.id)}`);
    const th = await read('GET', `${snapshot.B}/threads/${enc(task.thread_id)}`);
    if (!valid(snapshot, version)) throw new Error('View changed. Review the task again.');
    return { task, thread: th.thread };
  }
  function pendingButtons(i, source, done) {
    const snapshot = current, version = sequence;
    source.disabled = true;
    const box = h('div', { class: 'op-pending', role: 'status' }); receipts.append(box);
    const execute = async () => {
      // A receipt owns an existing intent, independently of the visible view.
      if (!session.active || session.ending || i.generation !== session.generation || i.busy || i.done) return;
      if (!session.pending.has(i) && (!i.continuation || session.pending.size)) return;
      box.replaceChildren('Sending…');
      try {
        await session.perform(i, { requeue: true });
        if (!session.active || session.ending || i.generation !== session.generation || !i.done) return;
        box.replaceChildren('Saved.');
        if (valid(snapshot, version)) { source.disabled = false; done?.(); await loadChannel(snapshot.channel, snapshot.thread?.id); }
      } catch (e) {
        if (!session.active || i.generation !== session.generation) return;
        const cosmetic = i.index > 0;
        const message = e.status === 404 ? 'Item gone or hidden.' : `${e.problem?.code || 'Request failed'}: ${e.problem?.reason || e.message}${e.problem?.current_state || e.problem?.state ? `; state ${e.problem.current_state || e.problem.state}` : ''}`;
        box.replaceChildren(cosmetic ? `${i.kind === 'approve' ? 'Approved; label not updated' : 'Reply posted; label not updated'}. ${message}` : message);
        box.append(h('p', {}, `Pending destination: ${i.steps[i.index].method} ${i.steps[i.index].path}. Retry sends the original values.`));
        const ambiguous = [0, 502, 503].includes(e.status);
        if (cosmetic || ambiguous) box.append(button(cosmetic ? 'Retry label only' : 'Retry same intent', execute));
        else {
          session.pending.delete(i); source.disabled = false;
          if ([409, 412].includes(e.status)) {
            if (!valid(snapshot, version)) { box.append(' Return to the original destination and review its current state before confirming a new action. No retry was sent.'); return; }
            const before = i.task;
            try {
              const refreshed = await refreshTask(), after = refreshed?.task;
              const values = (t) => t ? JSON.stringify({ state: t.state, assignee: t.assignee_actor_id, claim: t.current_claim?.actor_id || null, brief_revision: t.brief_revision, revision: t.revision, direct_action_required: t.direct_action_required }) : 'none';
              box.append(h('pre', {}, `Previously read: ${values(before)}\nNow: ${values(after)}\nLabels previously: ${JSON.stringify(i.labels || {})}\nLabels now: ${JSON.stringify(refreshed?.thread.labels || {})}`), button('Review current state', () => valid(snapshot, version) && loadChannel(snapshot.channel, snapshot.thread?.id)));
              box.append(' Review the current state and confirm a new action. No retry was sent.');
            } catch { box.append(' Could not refresh; refresh before acting again.'); }
          }
        }
      }
    };
    execute();
  }
  function send(source, spec, done) { if (!guards.get(source)?.()) return; if (source.disabled || session.pending.size) { say('Resolve the pending intent before starting another write.'); return; } pendingButtons(session.intent(spec), source, done); }
  function confirmation(name, details, spec, source, done) {
    if (!guards.get(source)?.()) return;
    if (source.disabled || session.pending.size) { say('Resolve the pending intent before starting another write.'); return; }
    const prior = document.activeElement;
    const cancel = button('Cancel', () => dialog.close());
    const confirm = button(name, () => { dialog.close(); send(source, spec, done); });
    const dialog = h('dialog', { class: 'op-confirm', 'aria-label': name }, h('h2', {}, name), h('pre', {}, details), cancel, confirm);
    dialog.addEventListener('keydown', (e) => { e.stopPropagation(); if (e.key === 'Enter' && !e.target.closest('button')) { e.preventDefault(); e.stopPropagation(); } });
    dialog.addEventListener('close', () => { dialog.remove(); prior?.focus(); });
    document.body.append(dialog); dialog.showModal(); cancel.focus();
  }
  async function confirmTask(kind, name, source, values = {}) {
    const snapshot = current, version = sequence;
    if (!guards.get(source)?.()) return;
    try {
      const refreshed = await refreshTask(); if (!refreshed || !valid(snapshot, version)) return;
      const { task: t, thread } = refreshed;
      // Recheck current eligibility, rather than using a stale enabled button.
      if (['approve', 'flag', 'assign'].includes(kind) && !manager(t)) throw new Error('Manager permission is required.');
      if (['approve', 'changes'].includes(kind) && (t.state !== 'blocked' || t.direct_action_required !== true)) throw new Error('This task is no longer a gate.');
      if (kind === 'changes' && !values.note?.trim()) throw new Error('Describe the requested changes.');
      if (kind === 'claim' && (t.state !== 'ready' || (t.assignee_actor_id && t.assignee_actor_id !== session.me.actor.id))) throw new Error('This task is not eligible to claim.');
      const next = ({ approve: 'ready or waiting (prerequisites decide)', claim: 'claimed', release: 'ready' })[kind] || t.state;
      const extra = kind === 'flag' ? `\nNeeds my approval: ${t.direct_action_required} → ${values.value}` : kind === 'assign' ? `\nNew assignee: ${actor(values.assignee)}` : ['approve', 'changes'].includes(kind) ? `\nGate reason: ${t.block?.reason || ''}\nNote: ${values.note || ''}` : '';
      const details = `Acting as ${actor(session.me.actor.id)}\nTask: ${t.title}\nChannel: ${t.channel_path}\nState: ${t.state} → ${next}\nAssignee: ${actor(t.assignee_actor_id)}\nClaim holder: ${actor(t.current_claim?.actor_id)}\nRevision: ${t.revision}; brief: ${t.brief_revision}${extra}`;
      const spec = taskIntent(kind, snapshot.B, t, { ...values, root: thread.root_message_id }); spec.labels = structuredClone(thread.labels || {});
      confirmation(name, details, spec, source);
    } catch (e) { say(e.status === 404 ? 'Item gone or hidden.' : e.message); }
  }
  function action(name, kind, values) { const b = writeButton(name, () => { const v = typeof values === 'function' ? values() : values; confirmTask(kind, kind === 'claim' ? 'Claim' : kind === 'assign' ? `Assign to @${session.me.actor.handle}` : name, b, v); }); return b; }
  function taskControls(t) {
    const view = current;
    const out = h('section', { 'aria-label': 'Task actions' }, h('h3', {}, `${t.title} — ${t.state}`));
    const path = `${view.B}/tasks/${enc(t.id)}`;
    const lease = session.claims.get(path);
    if (lease) out.append(h('p', { class: 'op-lease', 'data-task-path': path }, 'You hold this claim in this tab; renewing while the tab is open.'));
    else if (t.current_claim?.actor_id === session.me.actor.id) out.append(h('p', {}, 'Your claim belongs to another session or a reload lost its proof. It will lapse; this tab cannot release it.'));
    if (rights('claim') && session.server.capabilities?.includes('yak.tasks.claims') && t.state === 'ready' && (!t.assignee_actor_id || t.assignee_actor_id === session.me.actor.id)) out.append(action('Work on this now (while this tab is open)', 'claim', { lease: Math.min(300, session.server.limits?.claim_lease_max_seconds || 300) }));
    if (lease) out.append(action('Release', 'release'));
    if (manager(t)) {
      if (!t.current_claim) {
        out.append(action('Assign to myself', 'assign', { assignee: session.me.actor.id }));
        const select = h('select', { 'aria-label': 'New assignee' }, h('option', { value: '' }, 'Unassigned'), view.actors.map((a) => h('option', { value: a.id, selected: a.id === t.assignee_actor_id }, `@${a.handle} (${a.kind})`)));
        const assign = writeButton('Assign / unassign', () => confirmTask('assign', select.value ? `Assign to ${actor(select.value)}` : 'Unassign', assign, { assignee: select.value || null }));
        out.append(field('New assignee', select), assign, h('p', {}, 'Assignment is suitable for work longer than this tab session.'));
      }
      if (typeof t.direct_action_required === 'boolean') out.append(action(t.direct_action_required ? 'Remove approval requirement' : 'Require my approval', 'flag', { value: !t.direct_action_required }));
      if (t.state === 'blocked' && t.direct_action_required === true && rights('label')) {
        const note = draft(`Gate note ${t.id}`);
        out.append(field('Approval note or requested changes', note), action('Approve and unblock', 'approve', () => ({ note: note.value })));
      }
    }
    if (t.state === 'blocked' && t.direct_action_required === true && rights('post') && rights('label')) {
      const note = draft(`Gate note ${t.id}`);
      if (!out.contains(note)) out.append(field('Requested changes', note));
      out.append(action('Request changes', 'changes', () => ({ note: note.value })));
    }
    if (!t.direct_action_required && view.thread.labels?.gate) out.append(h('p', {}, 'label only; not a gate'));
    else if (t.state === 'blocked' && view.thread.labels?.gate === 'approved') out.append(h('p', {}, 'label says approved; task is still blocked'));
    return out;
  }
  async function timeline(t, target, version) {
    const snapshot = current;
    let cursor, events = [], caught = false;
    try {
      while (!caught) {
        const p = await read('GET', `${snapshot.B}/events`, { thread_id: t.thread_id, ...(cursor ? { cursor } : { from: 'earliest' }), limit: 200 });
        events.push(...p.events); cursor = p.next_cursor; caught = p.caught_up;
        if (!valid(snapshot, version) || !panel) return;
      }
      const approval = approvalRecord(events, snapshot.actors, t.id);
      target.replaceChildren(h('h3', {}, 'Task event history'), h('p', {}, approval && t.state !== 'blocked' ? `Approved by ${provenance(approval)} at ${approval.recorded_at}; ${approval.payload.reason}; credential ${approval.credential_id}` : 'not approved'));
      for (const e of events.filter((e) => e.type.startsWith('task.'))) target.append(h('p', {}, `${e.type} · ${provenance(e)} · ${e.recorded_at} · ${e.payload.reason || ''} · credential ${e.credential_id || 'unknown'}${e.type === 'task.unblocked' && (e.on_behalf_of_actor_id || e.delegation_id || snapshot.actors.find((a) => a.id === e.actor_id)?.kind !== 'human') ? `; unblocked by ${provenance(e)}, not a person's approval` : ''}${e.run_id?.startsWith('operator/') ? ' · operator client (unverified)' : ''}`));
    } catch (e) { if (valid(snapshot, version)) target.textContent = `Approval history unavailable; no approval inferred. ${e.status === 404 ? 'Item gone or hidden.' : e.message}`; }
  }
  function messageControls(m) {
    const view = current;
    const box = h('article', { class: 'op-message' }, h('p', {}, `${provenance(m)} · ${m.created_at}`), h('pre', {}, m.text || '(deleted)'));
    if (m.deleted) return box;
    if (m.author_actor_id === session.me.actor.id && !m.on_behalf_of_actor_id && !(m.is_root && view.thread?.task_id) && rights('post')) {
      const edit = draft(`Edit ${m.id}`); if (!edit.dataset.loaded) { edit.value = m.text; edit.dataset.loaded = 'true'; }
      const save = writeButton('Save edit', () => send(save, { steps: [{ method: 'PATCH', path: `${view.B}/messages/${enc(m.id)}`, body: { text: edit.value }, headers: { 'If-Match': `"${m.revision}"` } }] }));
      box.append(field('Edit your message', edit), save);
    }
    if (rights('label') && session.server.capabilities?.includes('yak.labels')) {
      const key = draft(`Label key ${m.id}`, 'input'), val = draft(`Label value ${m.id}`, 'input');
      const label = (op) => {
        const k = key.value.trim().toLowerCase();
        if (!/^[a-z0-9][a-z0-9._-]{0,63}$/.test(k) || k === 'gate' || /^(yak|abby)\./.test(k)) { say('Choose an ordinary label key. Gate changes use the confirmed gate actions.'); return; }
        const old = m.labels?.[k];
        const b = op === 'set' ? set : remove;
        send(b, { steps: [{ method: 'PATCH', path: `${view.B}/messages/${enc(m.id)}/labels`, body: { operations: [{ op, key: k, ...(op === 'set' ? { value: val.value } : {}), expect: old === undefined ? { absent: true } : { value: old } }] } }] });
      };
      const set = writeButton('Set label', () => label('set')), remove = writeButton('Remove label', () => label('unset'));
      box.append(field('Label key', key), field('Label value', val), set, remove);
    }
    if (rights('react') && session.server.capabilities?.includes('yak.reactions')) {
      const key = draft(`Reaction ${m.id}`, 'input');
      for (const method of ['PUT', 'DELETE']) { const b = writeButton(method === 'PUT' ? 'React' : 'Remove reaction', () => { if (key.value.trim()) send(b, { steps: [{ method, path: `${view.B}/messages/${enc(m.id)}/reactions/${enc(key.value.trim())}`, body: method === 'PUT' ? {} : null }] }); }); box.append(b); }
      box.append(field('Reaction key', key));
    }
    return box;
  }
  function composer() {
    const view = current;
    if (!rights('post')) return h('p', {}, 'Read-only here: post permission is missing.');
    const text = draft(`Message ${view.B} ${view.thread?.id || view.channel}`), title = draft(`Title ${view.B} ${view.channel}`, 'input');
    const isReply = !!view.thread;
    const sendMessage = writeButton(isReply ? 'Send reply' : 'Start thread', () => {
      if (!text.value.trim() || (!isReply && !title.value.trim())) { say('Enter text and a title for a new thread.'); return; }
      const mentions = view.actors.filter((a) => new RegExp(`(?:^|[^A-Za-z0-9_])@${a.handle.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}(?![A-Za-z0-9_-])`).test(text.value)).map((a) => a.id);
      const clearSubmitted = submittedDrafts(...(isReply ? [text] : [text, title]));
      send(sendMessage, { steps: [{ method: 'POST', path: isReply ? `${view.B}/threads/${enc(view.thread.id)}/messages` : `${view.B}/threads`, body: { text: text.value, mentions, ...(!isReply ? { channel: view.channel, title: title.value } : {}) } }] }, clearSubmitted);
    });
    const box = h('section', {}, h('h3', {}, isReply ? 'Reply' : 'New thread'), !isReply ? field('Title', title) : null, field('Message (mentions notify; they grant no authority)', text), sendMessage);
    if (rights('task_manage') && session.server.capabilities?.includes('yak.tasks')) {
      const taskTitle = draft(`Task title ${view.B} ${view.channel}`, 'input'), gate = draft(`Needs my approval ${view.B} ${view.channel}`, 'input'); gate.type = 'checkbox';
      const assignee = h('select', { 'aria-label': 'Task assignee' }, h('option', { value: '' }, 'Unassigned'), view.actors.map((a) => h('option', { value: a.id }, `@${a.handle}`)));
      const create = writeButton('Create task', () => {
        if (!taskTitle.value.trim()) { say('Enter a task title.'); return; }
        const values = { title: taskTitle.value, text: text.value, channel: view.channel, gate: gate.checked, assignee: assignee.value || null };
        const clearSubmitted = submittedDrafts(taskTitle, text);
        confirmation('Create task', `Acting as ${actor(session.me.actor.id)}\nChannel: ${values.channel}\nTitle: ${values.title}\nAssignee: ${actor(values.assignee)}\nNeeds my approval: ${values.gate}\nText: ${values.text}`, taskIntent('create', view.B, null, values), create, clearSubmitted);
      });
      box.append(h('h3', {}, 'New task'), field('Task title', taskTitle), field('Task assignee', assignee), field('Needs my approval', gate), create);
    }
    return box;
  }
  async function loadChannel(channel, threadId) {
    const version = ++sequence;
    dismissConfirmations(); panel.querySelector('.op-channel').replaceChildren();
    const snapshot = current = Object.freeze({ ...current, channel, thread: null, task: null });
    refresh.disabled = false;
    const B = snapshot.B; content.replaceChildren('Reading current permissions and state…');
    try {
      const [perm, threads, actors] = await Promise.all([read('GET', `${B}/permissions`, { channel }), all(`${B}/threads`, 'threads', { channel, subtree: false }), all(`${B}/actors`, 'actors')]);
      if (!valid(snapshot, version) || !panel) return;
      let thread = null, task = null, messages = [];
      if (threadId) {
        const th = await read('GET', `${B}/threads/${enc(threadId)}`);
        if (!valid(snapshot, version) || !panel) return;
        thread = th.thread;
        messages = await all(`${B}/threads/${enc(threadId)}/messages`, 'messages');
        if (!valid(snapshot, version) || !panel) return;
        if (th.thread.task_id) {
          task = await read('GET', `${B}/tasks/${enc(th.thread.task_id)}`);
          if (!valid(snapshot, version) || !panel) return;
        }
      }
      const view = current = Object.freeze({ ...snapshot, permissions: perm.capabilities, actors, thread, task });
      const select = h('select', { 'aria-label': 'Thread', onchange: () => valid(view, version) && loadChannel(channel, select.value) }, h('option', { value: '' }, 'New thread / task'), threads.map((t) => h('option', { value: t.id, selected: t.id === threadId }, t.title)));
      content.replaceChildren(field('Thread', select));
      const sel = h('select', { 'aria-label': 'Channel', onchange: () => valid(view, version) && loadChannel(sel.value, '') }, view.channels.map((c) => h('option', { value: c.path, selected: c.path === channel }, c.path)));
      panel.querySelector('.op-channel').replaceChildren(field('Channel', sel));
      if (task) { content.append(taskControls(task)); const history = h('section', {}, 'Reading task history…'); content.append(history); timeline(task, history, version); }
      content.append(...messages.map(messageControls), composer());
    } catch (e) { if (version === sequence) say(e.status === 404 ? 'Item gone or hidden.' : `Read failed: ${e.message}`); }
  }
  async function loadBoard(id, channel, thread) {
    const version = ++sequence;
    dismissConfirmations();
    refresh.disabled = true;
    content.replaceChildren('Reading board…'); panel.querySelector('.op-channel').replaceChildren();
    const snapshot = current = Object.freeze({ B: `/v1/boards/${enc(id)}`, generation: session.generation, actors: [], permissions: [] });
    try {
      const B = snapshot.B;
      const [channels, detail] = await Promise.all([all(`${B}/channels`, 'channels'), thread ? read('GET', `${B}/threads/${enc(thread)}`) : null]);
      if (!panel || !valid(snapshot, version)) return;
      channel = detail?.thread.channel_path || (channels.some((c) => c.path === channel) ? channel : channels[0]?.path);
      if (!channel) { say('No readable channel is available.'); return; }
      current = Object.freeze({ ...snapshot, channels });
      await loadChannel(channel, thread);
    } catch (e) { if (valid(snapshot, version)) say(e.status === 404 ? 'Item gone or hidden.' : e.message); }
  }
  async function open(context = route()) {
    if (!session.active || session.ending) return;
    close(); opener = document.activeElement;
    status = h('p', { role: 'status' }); content = h('div', {});
    refresh = button('Refresh permissions and state', () => current?.channel && loadChannel(current.channel, current.thread?.id));
    refresh.disabled = true;
    panel = h('dialog', { class: 'op-workspace', 'aria-label': 'Operator tools' }, h('h2', {}, 'Operator tools'), button('Close', close), status, h('div', { class: 'op-board' }), h('div', { class: 'op-channel' }), refresh, content, receipts);
    const openedPanel = panel, version = sequence;
    panel.addEventListener('keydown', (e) => e.stopPropagation());
    panel.addEventListener('cancel', (e) => { e.preventDefault(); close(); });
    document.body.append(panel); panel.show();
    try {
      const boards = await all('/v1/boards', 'boards');
      if (panel !== openedPanel || sequence !== version) return;
      const selected = context.board || boards[0]?.id;
      const sel = h('select', { 'aria-label': 'Board', onchange: () => loadBoard(sel.value) }, boards.map((b) => h('option', { value: b.id, selected: b.id === selected }, b.name)));
      panel.querySelector('.op-board').append(field('Board', sel));
      await loadBoard(selected, context.ch, context.t);
    } catch (e) { if (panel === openedPanel && sequence === version) say(e.message); }
  }
  return { open, close, update: () => { for (const b of document.querySelectorAll('[data-op-write]')) b.disabled = session.pending.size > 0 || session.ending; for (const node of document.querySelectorAll('.op-lease')) { const c = session.claims.get(node.dataset.taskPath); node.textContent = c ? `You hold this claim in this tab; ${Math.max(0, Math.ceil((c.deadline - session.mono()) / 1000))} seconds remaining. Renewing while this tab is open.` : 'Your claim ended. Refresh before acting again.'; } }, clear: () => { close(); drafts.clear(); receipts.replaceChildren(); document.querySelectorAll('.op-confirm').forEach((d) => d.remove()); } };
}
