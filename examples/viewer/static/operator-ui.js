import { h } from './skin-kit.js';
import { approvalRecord, taskIntent } from './operator.js';
const enc = encodeURIComponent;
const button = (text, click) => h('button', { type: 'button', onclick: click }, text);
const field = (label, node) => h('label', {}, label, node);

// A shared, explicit workspace keeps the source skins and their lifecycle intact.
// Draft nodes survive live store redraws, navigation and conflict refreshes.
export function createOperatorUI({ session, read, route }) {
  let panel, content, status, current, opener, sequence = 0;
  const drafts = new Map();
  const receipts = h('div', { class: 'op-receipts' });
  function writeButton(text, click) { const b = button(text, click); b.dataset.opWrite = 'true'; b.disabled = session.pending.size > 0 || session.ending; return b; }
  function say(text) { if (status) status.textContent = text; }
  function close() { sequence++; panel?.remove(); panel = null; current = null; opener?.focus(); }
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
  function draft(key, kind = 'textarea') {
    if (!drafts.has(key)) drafts.set(key, h(kind, { 'aria-label': key }));
    return drafts.get(key);
  }
  function rights(cap) { return session.me.credential.scopes.includes('read') && current.permissions.includes('read') && current.permissions.includes(cap) && session.me.credential.scopes.includes(cap); }
  function manager(t) { return rights('task_admin') || (t.owner_actor_id === session.me.actor.id && rights('task_manage')); }
  async function refreshTask() {
    if (!current?.task) return;
    const snapshot = current, version = sequence;
    const task = await read('GET', `${snapshot.B}/tasks/${enc(snapshot.task.id)}`);
    const th = await read('GET', `${snapshot.B}/threads/${enc(task.thread_id)}`);
    if (current !== snapshot || version !== sequence || !session.active) throw new Error('View changed. Review the task again.');
    current.task = task; current.thread = th.thread; return task;
  }
  function pendingButtons(i, source, done) {
    source.disabled = true;
    const box = h('div', { class: 'op-pending', role: 'status' }); receipts.append(box);
    const execute = async () => {
      box.replaceChildren('Sending…');
      try {
        await session.perform(i);
        if (!session.active || session.ending || i.generation !== session.generation || !i.done) return;
        box.replaceChildren('Saved.'); source.disabled = false; done?.();
        if (current) await loadChannel(current.channel, current.thread?.id);
      } catch (e) {
        if (!session.active || i.generation !== session.generation) return;
        const cosmetic = i.index > 0;
        const message = e.status === 404 ? 'Item gone or hidden.' : `${e.problem?.code || 'Request failed'}: ${e.problem?.reason || e.message}${e.problem?.current_state || e.problem?.state ? `; state ${e.problem.current_state || e.problem.state}` : ''}`;
        box.replaceChildren(cosmetic ? `${i.kind === 'approve' ? 'Approved; label not updated' : 'Reply posted; label not updated'}. ${message}` : message);
        const ambiguous = [0, 502, 503].includes(e.status);
        if (cosmetic || ambiguous) box.append(button(cosmetic ? 'Retry label only' : 'Retry same intent', execute));
        else {
          session.pending.delete(i); source.disabled = false;
          if ([409, 412].includes(e.status)) {
            const before = i.task;
            try {
              const after = await refreshTask();
              const values = (t) => t ? JSON.stringify({ state: t.state, assignee: t.assignee_actor_id, claim: t.current_claim?.actor_id || null, brief_revision: t.brief_revision, revision: t.revision, direct_action_required: t.direct_action_required }) : 'none';
              box.append(h('pre', {}, `Previously read: ${values(before)}\nNow: ${values(after)}\nLabels previously: ${JSON.stringify(i.labels || {})}\nLabels now: ${JSON.stringify(current.thread?.labels || {})}`), button('Review current state', () => loadChannel(current.channel, current.thread?.id)));
              box.append(' Review the current state and confirm a new action. No retry was sent.');
            } catch { box.append(' Could not refresh; refresh before acting again.'); }
          }
        }
      }
    };
    execute();
  }
  function send(source, spec, done) { if (!session.active || session.ending) return; if (source.disabled || session.pending.size) { say('Resolve the pending intent before starting another write.'); return; } pendingButtons(session.intent(spec), source, done); }
  function confirmation(name, details, spec, source, done) {
    if (!session.active || session.ending) return;
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
    try {
      const t = await refreshTask(); if (!t) return;
      // Recheck current eligibility, rather than using a stale enabled button.
      if (['approve', 'flag', 'assign'].includes(kind) && !manager(t)) throw new Error('Manager permission is required.');
      if (['approve', 'changes'].includes(kind) && (t.state !== 'blocked' || t.direct_action_required !== true)) throw new Error('This task is no longer a gate.');
      if (kind === 'changes' && !values.note?.trim()) throw new Error('Describe the requested changes.');
      if (kind === 'claim' && (t.state !== 'ready' || (t.assignee_actor_id && t.assignee_actor_id !== session.me.actor.id))) throw new Error('This task is not eligible to claim.');
      const next = ({ approve: 'ready or waiting (prerequisites decide)', claim: 'claimed', release: 'ready' })[kind] || t.state;
      const extra = kind === 'flag' ? `\nNeeds my approval: ${t.direct_action_required} → ${values.value}` : kind === 'assign' ? `\nNew assignee: ${actor(values.assignee)}` : ['approve', 'changes'].includes(kind) ? `\nGate reason: ${t.block?.reason || ''}\nNote: ${values.note || ''}` : '';
      const details = `Acting as ${actor(session.me.actor.id)}\nTask: ${t.title}\nChannel: ${t.channel_path}\nState: ${t.state} → ${next}\nAssignee: ${actor(t.assignee_actor_id)}\nClaim holder: ${actor(t.current_claim?.actor_id)}\nRevision: ${t.revision}; brief: ${t.brief_revision}${extra}`;
      const spec = taskIntent(kind, current.B, t, { ...values, root: current.thread.root_message_id }); spec.labels = structuredClone(current.thread.labels || {});
      confirmation(name, details, spec, source);
    } catch (e) { say(e.status === 404 ? 'Item gone or hidden.' : e.message); }
  }
  function action(name, kind, values) { const b = writeButton(name, () => { const v = typeof values === 'function' ? values() : values; confirmTask(kind, kind === 'claim' ? 'Claim' : kind === 'assign' ? `Assign to @${session.me.actor.handle}` : name, b, v); }); return b; }
  function taskControls(t) {
    const out = h('section', { 'aria-label': 'Task actions' }, h('h3', {}, `${t.title} — ${t.state}`));
    const path = `${current.B}/tasks/${enc(t.id)}`;
    const lease = session.claims.get(path);
    if (lease) out.append(h('p', { class: 'op-lease', 'data-task-path': path }, 'You hold this claim in this tab; renewing while the tab is open.'));
    else if (t.current_claim?.actor_id === session.me.actor.id) out.append(h('p', {}, 'Your claim belongs to another session or a reload lost its proof. It will lapse; this tab cannot release it.'));
    if (rights('claim') && session.server.capabilities?.includes('yak.tasks.claims') && t.state === 'ready' && (!t.assignee_actor_id || t.assignee_actor_id === session.me.actor.id)) out.append(action('Work on this now (while this tab is open)', 'claim', { lease: Math.min(300, session.server.limits?.claim_lease_max_seconds || 300) }));
    if (lease) out.append(action('Release', 'release'));
    if (manager(t)) {
      if (!t.current_claim) {
        out.append(action('Assign to myself', 'assign', { assignee: session.me.actor.id }));
        const select = h('select', { 'aria-label': 'New assignee' }, h('option', { value: '' }, 'Unassigned'), current.actors.map((a) => h('option', { value: a.id, selected: a.id === t.assignee_actor_id }, `@${a.handle} (${a.kind})`)));
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
    if (!t.direct_action_required && current.thread.labels?.gate) out.append(h('p', {}, 'label only; not a gate'));
    else if (t.state === 'blocked' && current.thread.labels?.gate === 'approved') out.append(h('p', {}, 'label says approved; task is still blocked'));
    return out;
  }
  async function timeline(t, target, version) {
    let cursor, events = [], caught = false;
    try {
      while (!caught) {
        const p = await read('GET', `${current.B}/events`, { thread_id: t.thread_id, ...(cursor ? { cursor } : { from: 'earliest' }), limit: 200 });
        events.push(...p.events); cursor = p.next_cursor; caught = p.caught_up;
        if (version !== sequence || !panel) return;
      }
      const approval = approvalRecord(events, current.actors, t.id);
      target.replaceChildren(h('h3', {}, 'Task event history'), h('p', {}, approval && t.state !== 'blocked' ? `Approved by ${provenance(approval)} at ${approval.recorded_at}; ${approval.payload.reason}; credential ${approval.credential_id}` : 'not approved'));
      for (const e of events.filter((e) => e.type.startsWith('task.'))) target.append(h('p', {}, `${e.type} · ${provenance(e)} · ${e.recorded_at} · ${e.payload.reason || ''} · credential ${e.credential_id || 'unknown'}${e.type === 'task.unblocked' && (e.on_behalf_of_actor_id || e.delegation_id || current.actors.find((a) => a.id === e.actor_id)?.kind !== 'human') ? `; unblocked by ${provenance(e)}, not a person's approval` : ''}${e.run_id?.startsWith('operator/') ? ' · operator client (unverified)' : ''}`));
    } catch (e) { if (version === sequence) target.textContent = `Approval history unavailable; no approval inferred. ${e.status === 404 ? 'Item gone or hidden.' : e.message}`; }
  }
  function messageControls(m) {
    const box = h('article', { class: 'op-message' }, h('p', {}, `${provenance(m)} · ${m.created_at}`), h('pre', {}, m.text || '(deleted)'));
    if (m.deleted) return box;
    if (m.author_actor_id === session.me.actor.id && !m.on_behalf_of_actor_id && !(m.is_root && current.thread?.task_id) && rights('post')) {
      const edit = draft(`Edit ${m.id}`); if (!edit.dataset.loaded) { edit.value = m.text; edit.dataset.loaded = 'true'; }
      const save = writeButton('Save edit', () => send(save, { steps: [{ method: 'PATCH', path: `${current.B}/messages/${enc(m.id)}`, body: { text: edit.value }, headers: { 'If-Match': `"${m.revision}"` } }] }));
      box.append(field('Edit your message', edit), save);
    }
    if (rights('label') && session.server.capabilities?.includes('yak.labels')) {
      const key = draft(`Label key ${m.id}`, 'input'), val = draft(`Label value ${m.id}`, 'input');
      const label = (op) => {
        const k = key.value.trim().toLowerCase();
        if (!/^[a-z0-9][a-z0-9._-]{0,63}$/.test(k) || k === 'gate' || /^(yak|abby)\./.test(k)) { say('Choose an ordinary label key. Gate changes use the confirmed gate actions.'); return; }
        const old = m.labels?.[k];
        const b = op === 'set' ? set : remove;
        send(b, { steps: [{ method: 'PATCH', path: `${current.B}/messages/${enc(m.id)}/labels`, body: { operations: [{ op, key: k, ...(op === 'set' ? { value: val.value } : {}), expect: old === undefined ? { absent: true } : { value: old } }] } }] });
      };
      const set = writeButton('Set label', () => label('set')), remove = writeButton('Remove label', () => label('remove'));
      box.append(field('Label key', key), field('Label value', val), set, remove);
    }
    if (rights('react') && session.server.capabilities?.includes('yak.reactions')) {
      const key = draft(`Reaction ${m.id}`, 'input');
      for (const method of ['PUT', 'DELETE']) { const b = writeButton(method === 'PUT' ? 'React' : 'Remove reaction', () => { if (key.value.trim()) send(b, { steps: [{ method, path: `${current.B}/messages/${enc(m.id)}/reactions/${enc(key.value.trim())}`, body: method === 'PUT' ? {} : null }] }); }); box.append(b); }
      box.append(field('Reaction key', key));
    }
    return box;
  }
  function composer() {
    if (!rights('post')) return h('p', {}, 'Read-only here: post permission is missing.');
    const text = draft(`Message ${current.B} ${current.thread?.id || current.channel}`), title = draft(`Title ${current.B} ${current.channel}`, 'input');
    const isReply = !!current.thread;
    const sendMessage = writeButton(isReply ? 'Send reply' : 'Start thread', () => {
      if (!text.value.trim() || (!isReply && !title.value.trim())) { say('Enter text and a title for a new thread.'); return; }
      const mentions = current.actors.filter((a) => new RegExp(`(?:^|[^A-Za-z0-9_])@${a.handle.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}(?![A-Za-z0-9_-])`).test(text.value)).map((a) => a.id);
      send(sendMessage, { steps: [{ method: 'POST', path: isReply ? `${current.B}/threads/${enc(current.thread.id)}/messages` : `${current.B}/threads`, body: { text: text.value, mentions, ...(!isReply ? { channel: current.channel, title: title.value } : {}) } }] }, () => { text.value = ''; });
    });
    const box = h('section', {}, h('h3', {}, isReply ? 'Reply' : 'New thread'), !isReply ? field('Title', title) : null, field('Message (mentions notify; they grant no authority)', text), sendMessage);
    if (rights('task_manage') && session.server.capabilities?.includes('yak.tasks')) {
      const taskTitle = draft(`Task title ${current.B} ${current.channel}`, 'input'), gate = draft(`Needs my approval ${current.B} ${current.channel}`, 'input'); gate.type = 'checkbox';
      const assignee = h('select', { 'aria-label': 'Task assignee' }, h('option', { value: '' }, 'Unassigned'), current.actors.map((a) => h('option', { value: a.id }, `@${a.handle}`)));
      const create = writeButton('Create task', () => {
        if (!taskTitle.value.trim()) { say('Enter a task title.'); return; }
        const values = { title: taskTitle.value, text: text.value, channel: current.channel, gate: gate.checked, assignee: assignee.value || null };
        confirmation('Create task', `Acting as ${actor(session.me.actor.id)}\nChannel: ${values.channel}\nTitle: ${values.title}\nAssignee: ${actor(values.assignee)}\nNeeds my approval: ${values.gate}\nText: ${values.text}`, taskIntent('create', current.B, null, values), create, () => { taskTitle.value = ''; text.value = ''; });
      });
      box.append(h('h3', {}, 'New task'), field('Task title', taskTitle), field('Task assignee', assignee), field('Needs my approval', gate), create);
    }
    return box;
  }
  async function loadChannel(channel, threadId) {
    const version = ++sequence;
    const B = current.B; current.channel = channel; content.replaceChildren('Reading current permissions and state…');
    try {
      const [perm, threads, actors] = await Promise.all([read('GET', `${B}/permissions`, { channel }), all(`${B}/threads`, 'threads', { channel, subtree: false }), all(`${B}/actors`, 'actors')]);
      if (version !== sequence || !panel) return;
      current.permissions = perm.capabilities; current.actors = actors;
      current.thread = null; current.task = null;
      const select = h('select', { 'aria-label': 'Thread', onchange: () => loadChannel(channel, select.value) }, h('option', { value: '' }, 'New thread / task'), threads.map((t) => h('option', { value: t.id, selected: t.id === threadId }, t.title)));
      content.replaceChildren(field('Thread', select));
      if (threadId) {
        const th = await read('GET', `${B}/threads/${enc(threadId)}`);
        if (version !== sequence || !panel) return;
        current.thread = th.thread;
        const messages = await all(`${B}/threads/${enc(threadId)}/messages`, 'messages');
        if (version !== sequence || !panel) return;
        if (th.thread.task_id) {
          const task = await read('GET', `${B}/tasks/${enc(th.thread.task_id)}`);
          if (version !== sequence || !panel) return;
          current.task = task; content.append(taskControls(task));
          const history = h('section', {}, 'Reading task history…'); content.append(history); timeline(task, history, version);
        }
        content.append(...messages.map(messageControls));
      }
      content.append(composer());
    } catch (e) { if (version === sequence) say(e.status === 404 ? 'Item gone or hidden.' : `Read failed: ${e.message}`); }
  }
  async function loadBoard(id, channel, thread) {
    const version = ++sequence; current = { B: `/v1/boards/${enc(id)}`, actors: [], permissions: [] };
    try {
      const B = current.B;
      const [channels, detail] = await Promise.all([all(`${B}/channels`, 'channels'), thread ? read('GET', `${B}/threads/${enc(thread)}`) : null]);
      if (!panel || version !== sequence) return;
      channel = detail?.thread.channel_path || (channels.some((c) => c.path === channel) ? channel : channels[0]?.path);
      if (!channel) { say('No readable channel is available.'); return; }
      const sel = h('select', { 'aria-label': 'Channel', onchange: () => loadChannel(sel.value, '') }, channels.map((c) => h('option', { value: c.path, selected: c.path === channel }, c.path)));
      panel.querySelector('.op-channel').replaceChildren(field('Channel', sel));
      await loadChannel(channel, thread);
    } catch (e) { say(e.status === 404 ? 'Item gone or hidden.' : e.message); }
  }
  async function open(context = route()) {
    if (!session.active || session.ending) return;
    close(); opener = document.activeElement;
    status = h('p', { role: 'status' }); content = h('div', {});
    panel = h('dialog', { class: 'op-workspace', 'aria-label': 'Operator tools' }, h('h2', {}, 'Operator tools'), button('Close', close), status, h('div', { class: 'op-board' }), h('div', { class: 'op-channel' }), button('Refresh permissions and state', () => current && loadChannel(current.channel, current.thread?.id)), content, receipts);
    panel.addEventListener('keydown', (e) => e.stopPropagation());
    panel.addEventListener('cancel', (e) => { e.preventDefault(); close(); });
    document.body.append(panel); panel.show();
    try {
      const boards = await all('/v1/boards', 'boards');
      if (!panel) return;
      const selected = context.board || boards[0]?.id;
      const sel = h('select', { 'aria-label': 'Board', onchange: () => loadBoard(sel.value) }, boards.map((b) => h('option', { value: b.id, selected: b.id === selected }, b.name)));
      panel.querySelector('.op-board').append(field('Board', sel));
      await loadBoard(selected, context.ch, context.t);
    } catch (e) { say(e.message); }
  }
  return { open, close, update: () => { for (const b of document.querySelectorAll('[data-op-write]')) b.disabled = session.pending.size > 0 || session.ending; for (const node of document.querySelectorAll('.op-lease')) { const c = session.claims.get(node.dataset.taskPath); node.textContent = c ? `You hold this claim in this tab; ${Math.max(0, Math.ceil((c.deadline - session.mono()) / 1000))} seconds remaining. Renewing while this tab is open.` : 'Your claim ended. Refresh before acting again.'; } }, clear: () => { close(); drafts.clear(); receipts.replaceChildren(); document.querySelectorAll('.op-confirm').forEach((d) => d.remove()); } };
}
