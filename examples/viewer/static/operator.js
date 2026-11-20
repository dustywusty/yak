// Operator contract state. Credentials and proofs belong only to this page's memory.
const scopes = new Set(['read', 'post', 'react', 'label', 'claim', 'task_manage', 'task_admin']);
export function credentialRefusal(me, server, date) {
  const c = me?.credential;
  if (me?.actor?.kind !== 'human') return 'Operator mode requires a human actor.';
  if (me.delegation != null || c?.delegation_id != null) return 'Delegated credentials cannot enter operator mode.';
  if (!me.actor.id || !c?.id) return 'Missing actor or credential identity.';
  if (!c || !Array.isArray(c.scopes)) return 'Missing credential scopes.';
  for (const s of c.scopes) if (!scopes.has(s)) return `Refused credential scope: ${s}`;
  if (!Array.isArray(c.board_ids) || !c.board_ids.length) return 'Refused credential board_ids: a board restriction is required.';
  const expiry = Date.parse(c.expires_at);
  if (!Number.isFinite(date) || !Number.isFinite(expiry) || expiry <= date || expiry - date > 43200000) return 'Refused expires_at: valid server time and a remaining lifetime of at most 12 hours are required.';
  if ((server.capabilities || []).includes('yak.credentials.restrictions')) {
    for (const flag of ['may_manage_credentials', 'may_delegate']) if (c[flag] !== false) return `Refused credential ${flag}: must be false.`;
  }
  return '';
}
const boardRead = /^\/v1\/boards\/[^/]+(?:\/(?:actors|channels|permissions|events|threads|tasks)|\/(?:threads\/[^/]+(?:\/messages)?|messages\/[^/]+|tasks\/[^/]+(?:\/briefs(?:\/[^/]+)?)?|snapshots\/[^/]+))?$/;
export function allowedRequest(method, path, ownId) {
  if (/[?#]/.test(path) || path.includes('..') || path.includes('//')) return false;
  let strictPath = path;
  if ((method === 'PUT' || method === 'DELETE') && /^\/v1\/boards\/[^/]+\/messages\/[^/]+\/reactions\/[^/]+$/.test(path)) {
    const end = path.lastIndexOf('/');
    strictPath = path.slice(0, end);
    // Decode only the terminal key, once; '+' remains a literal plus.
    let key;
    try { key = decodeURIComponent(path.slice(end + 1)); } catch { return false; }
    if (!key || key === '.' || key.includes('..') || /[/\\%\u0000-\u001f\u007f-\u009f\uD800-\uDFFF]/u.test(key)) return false;
  }
  if (strictPath.includes('%')) return false;
  if (method === 'GET') return ['/v1/server', '/v1/me', '/v1/boards'].includes(path) || boardRead.test(path);
  if (method === 'POST' && /^\/v1\/boards\/[^/]+\/snapshots$/.test(path)) return true;
  if (method === 'DELETE' && /^\/v1\/boards\/[^/]+\/snapshots\/[^/]+$/.test(path)) return true;
  if (method === 'DELETE' && path === `/v1/credentials/${ownId}` && ownId) return true;
  const tail = path.replace(/^\/v1\/boards\/[^/]+\//, '');
  if (tail === path) return false;
  if (method === 'POST') return /^(threads|threads\/[^/]+\/messages|tasks|tasks\/[^/]+\/(unblock|claims|release)|tasks\/[^/]+\/claims\/[^/]+\/renew)$/.test(tail);
  if (method === 'PATCH') return /^(messages\/[^/]+(\/labels)?|tasks\/[^/]+)$/.test(tail);
  if (method === 'PUT') return /^(tasks\/[^/]+\/assignee|messages\/[^/]+\/reactions\/[^/]+)$/.test(tail);
  return method === 'DELETE' && /^messages\/[^/]+\/reactions\/[^/]+$/.test(tail);
}
export function randomKey() {
  return [...crypto.getRandomValues(new Uint8Array(16))].map((x) => x.toString(16).padStart(2, '0')).join('');
}
export function taskIntent(kind, B, t, values = {}) {
  const path = `${B}/tasks/${t?.id}`, match = { 'If-Match': `"${t?.revision}"` };
  const step = (method, path, body, headers = {}) => ({ method, path, body, headers });
  const label = (value) => step('PATCH', `${B}/messages/${values.root}/labels`, { operations: [{ op: 'set', key: 'gate', value, expect: { value: 'pending' } }] });
  let steps;
  switch (kind) {
    case 'approve': steps = [step('POST', `${path}/unblock`, { reason: `gate approved${values.note ? `: ${values.note}` : ''}` }, match), label('approved')]; break;
    case 'changes': steps = [step('POST', `${B}/threads/${t.thread_id}/messages`, { text: values.note }), label('changes_requested')]; break;
    case 'flag': steps = [step('PATCH', path, { direct_action_required: values.value }, match)]; break;
    case 'assign': steps = [step('PUT', `${path}/assignee`, { assignee_actor_id: values.assignee }, match)]; break;
    case 'claim': steps = [step('POST', `${path}/claims`, { lease_seconds: values.lease || 300, expect_brief_revision: t.brief_revision })]; break;
    case 'release': steps = [step('POST', `${path}/release`, { reason: 'released by operator' })]; break;
    case 'create': steps = [step('POST', `${B}/tasks`, { thread: { channel: values.channel, title: values.title, text: values.text }, assignee_actor_id: values.assignee || null, direct_action_required: values.gate })]; break;
    default: throw new Error('Unknown task action');
  }
  return { kind, steps, task: t ? structuredClone(t) : null };
}
// Event-time task evidence is required; historical missing flags are not gates.
export function isGateApprovalEvent(e, actors) {
  return e.type === 'task.unblocked' && e.payload?.task?.direct_action_required === true &&
    e.on_behalf_of_actor_id == null && e.delegation_id == null &&
    actors.find((a) => a.id === e.actor_id)?.kind === 'human';
}
export function approvalRecord(events, actors, taskId) {
  let result = null, lastState, lastBlock;
  for (const e of events) {
    if ((e.payload?.task?.id || e.task_id) !== taskId) continue;
    const task = e.payload?.task;
    // Ignore ordinary revisions while blocked; only a new block generation
    // invalidates approval. Projections are authoritative in recorded order.
    const block = task && JSON.stringify([task.brief_revision, task.block?.at,
      task.block?.cause, task.block?.by_actor_id, task.block?.reason]);
    if (e.type === 'task.blocked' || e.type === 'task.reopened' ||
        (task?.state === 'blocked' && (lastState !== 'blocked' || block !== lastBlock))) result = null;
    if (task?.state != null) { lastState = task.state; lastBlock = block; }
    if (isGateApprovalEvent(e, actors)) result = e;
  }
  return result;
}
export class OperatorSession {
  constructor({ send, changed = () => {}, mono = () => performance.now(), wall = () => Date.now(), schedule = (fn, ms) => setTimeout(fn, ms), unschedule = (id) => clearTimeout(id) }) {
    Object.assign(this, { send, changed, mono, wall, schedule, unschedule });
    this.claims = new Map(); this.pending = new Set(); this.active = false; this.ending = false;
  }
  start(me, server, serverDate, serverMono = this.mono()) {
    const why = credentialRefusal(me, server, serverDate); if (why) throw new Error(why);
    this.me = me; this.server = server; this.run = `operator/${randomKey()}`; this.active = true; this.generation = Symbol();
    this.notice = null; this.lastInput = this.mono(); this.lastInputWall = this.wall();
    // Anchor to the discovery request start. Date has only second precision;
    // end conservatively rather than granting a late response extra lifetime.
    this.expiry = serverMono + Date.parse(me.credential.expires_at) - serverDate - 1000;
    this.expiryWall = this.wall() + Math.max(0, this.expiry - this.mono());
    this.timer = setInterval(() => this.tick(), 1000); this.timer.unref?.(); this.changed();
  }
  idleLeft() { return Math.min(900000 - this.mono() + this.lastInput, 900000 - this.wall() + this.lastInputWall); }
  lifeLeft() { return Math.min(this.expiry - this.mono(), this.expiryWall - this.wall()); }
  input() { if (!this.active || this.ending) return; if (this.idleLeft() <= 0 || this.lifeLeft() <= 0) { this.end(); return; } this.lastInput = this.mono(); this.lastInputWall = this.wall(); }
  async tick() {
    if (!this.active || this.ending) return;
    for (const [path, c] of this.claims) if (this.mono() >= c.deadline) this.lose(path);
    if (this.lifeLeft() <= 0 || this.idleLeft() <= 0) return this.end();
    this.changed();
  }
  intent(spec) {
    if (!this.active || this.ending) throw new Error('Operator session ended.');
    const i = { ...spec, steps: spec.steps.map((s) => ({ ...s, headers: { ...s.headers, 'Idempotency-Key': randomKey(), 'Yak-Run-Id': this.run } })), index: 0, busy: false, generation: this.generation };
    this.pending.add(i); this.changed(); return i;
  }
  async perform(i, { requeue = false } = {}) {
    if (i.busy || !this.active || this.ending || i.generation !== this.generation) return;
    // Only a known-failed cosmetic continuation may rejoin the global lock.
    // Keep the receipt's generation, index and original requests unchanged.
    if (!this.pending.has(i)) {
      if (!requeue || !i.continuation || i.done || this.pending.size) return;
      this.pending.add(i);
    }
    i.busy = true; this.changed();
    try {
      while (i.index < i.steps.length) {
        const s = i.steps[i.index], c = this.claims.get(s.path.replace(/\/release$/, ''));
        const headers = { ...s.headers };
        if (s.path.endsWith('/release')) {
          if (!c) throw new Error('The claim proof is no longer available.');
          headers['Yak-Claim-Proof'] = c.proof;
        }
        let result;
        s.started ??= this.mono();
        try { result = await this.send(s.method, s.path, s.body, headers); }
        catch (e) {
          // A label receipt may already exist; a conditional "approved" is done.
          if (i.kind === 'approve' && i.index === 1 && e.status === 412 && e.problem?.key === 'gate' && (e.problem?.current_value === 'approved' || e.problem?.current === 'approved')) result = null;
          else { if (!this.active || this.ending || i.generation !== this.generation) throw e; if (e.problem?.code === 'claim_lost') this.lose(s.path.replace(/\/release$/, '')); if (e.status === 401) await this.end({ unauthorized: true }); throw e; }
        }
        if (!this.active || this.ending || i.generation !== this.generation) return;
        if (i.kind === 'claim' && result?.proof) this.hold(s.path.replace(/\/claims$/, ''), result, s.started);
        if (i.kind === 'claim' && !result?.proof) this.notice = 'Claim accepted but proof unavailable; this tab cannot renew or release it. The lease will lapse.';
        if (i.kind === 'release') this.drop(s.path.replace(/\/release$/, ''));
        i.index++;
      }
      this.pending.delete(i); i.done = true;
    } catch (e) {
      i.error = e;
      if (['approve', 'changes'].includes(i.kind) && i.index === 1 &&
          e.status >= 400 && e.status < 500 &&
          this.active && !this.ending && i.generation === this.generation) {
        i.continuation = true;
        this.pending.delete(i);
      }
      throw e;
    }
    finally { i.busy = false; this.changed(); }
  }
  hold(path, result, started = this.mono()) {
    this.drop(path);
    const c = { proof: result.proof, id: result.claim.id, lease: result.claim.lease_seconds, deadline: started + result.claim.lease_seconds * 1000 };
    this.claims.set(path, c); this.renewLater(path, c);
  }
  renewLater(path, c) {
    c.timer = this.schedule(() => this.renew(path, c), c.lease * 1000 / 3 * (0.9 + Math.random() * 0.2));
    c.timer?.unref?.();
  }
  async renew(path, c) {
    if (!this.active || this.ending || this.claims.get(path) !== c) return;
    if (this.mono() >= c.deadline) return this.lose(path);
    // Keep this exact receipt across ambiguous network failures.
    if (!c.key) { c.key = randomKey(); c.started = this.mono(); }
    try {
      const view = await this.send('POST', `${path}/claims/${c.id}/renew`, { lease_seconds: c.lease }, { 'Yak-Claim-Proof': c.proof, 'Idempotency-Key': c.key, 'Yak-Run-Id': this.run });
      if (!this.active || this.ending || this.claims.get(path) !== c) return;
      const timestamp = (value) => {
        const parts = typeof value === 'string' && /^(\d{4})-(\d\d)-(\d\d)T(\d\d):(\d\d):(\d\d)(?:\.\d+)?(?:Z|[+-](\d\d):(\d\d))$/.exec(value);
        if (!parts) return NaN;
        const [year, month, day, hour, minute, second, offsetHour, offsetMinute] = parts.slice(1).map(part => Number(part ?? 0));
        const leap = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0);
        const days = [31, leap ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31];
        // Date.parse normalizes impossible dates and 24:00; reject them first.
        if (month < 1 || month > 12 || day < 1 || day > days[month - 1] ||
            hour > 23 || minute > 59 || second > 59 || offsetHour > 23 || offsetMinute > 59) return NaN;
        return Date.parse(value);
      };
      const acquired = timestamp(view?.acquired_at), expires = timestamp(view?.expires_at);
      const lease = view?.lease_seconds, maxLease = this.server.limits?.claim_lease_max_seconds;
      if (view?.id !== c.id || view.status !== 'active' || !Number.isSafeInteger(lease) || lease <= 0 ||
          !Number.isSafeInteger(lease * 1000) || (Number.isFinite(maxLease) && lease > maxLease) ||
          !Number.isFinite(acquired) || !Number.isFinite(expires) || acquired >= expires ||
          view.ended_at !== null || view.end_recorded_at !== null) return this.lose(path);
      const timing = view.__responseTiming, now = this.mono();
      let deadline = c.started + lease * 1000;
      if (Number.isFinite(timing?.serverDate) && Number.isFinite(timing?.requestMono) &&
          timing.requestMono >= c.started && timing.requestMono <= now) {
        if (acquired > timing.serverDate + 1000) return this.lose(path);
        // Date has second precision. Anchor at dispatch, never response receipt
        // or the browser wall clock, including an idempotent replay's expiry.
        deadline = Math.min(deadline, timing.requestMono + Math.max(0, expires - timing.serverDate - 1000));
      } else {
        deadline = Math.min(deadline, c.deadline);
        this.notice = 'Claim renewal time unavailable. Refresh the task before acting again.';
      }
      if (!Number.isFinite(deadline) || deadline <= now) return this.lose(path);
      c.deadline = deadline; c.lease = lease; c.key = null; this.renewLater(path, c);
    } catch (e) {
      if (!this.active || this.ending || this.claims.get(path) !== c) return;
      if (e.status === 401) return this.end({ unauthorized: true });
      if ([403, 404, 409].includes(e.status) || this.mono() >= c.deadline) return this.lose(path);
      c.timer = this.schedule(() => this.renew(path, c), Math.min(1000, Math.max(0, c.deadline - this.mono())));
    }
  }
  drop(path) { const c = this.claims.get(path); if (c) { this.unschedule(c.timer); c.proof = null; this.claims.delete(path); } }
  lose(path) { this.drop(path); this.notice = 'Your claim ended. Refresh the task before acting again.'; this.changed(); }
  async end({ unauthorized = false, keepalive = false } = {}) {
    if (!this.active || this.ending) return;
    const run = this.run;
    this.ending = true; clearInterval(this.timer);
    for (const c of this.claims.values()) this.unschedule(c.timer);
    try {
      if (keepalive && !unauthorized) {
        // A closing document may never run a response continuation. Capture
        // each request and dispatch all of them while the transport is active.
        const dispatch = (method, path, body, headers) => {
          try { Promise.resolve(this.send(method, path, body, headers, { keepalive: true })).catch(() => {}); }
          catch { /* Best effort even when transport dispatch throws. */ }
        };
        for (const [path, c] of this.claims) {
          dispatch('POST', `${path}/release`, { reason: 'operator session ended' }, { 'Yak-Claim-Proof': c.proof, 'Idempotency-Key': randomKey(), 'Yak-Run-Id': run });
        }
        dispatch('DELETE', `/v1/credentials/${this.me.credential.id}`, null, { 'Yak-Run-Id': run });
        return;
      }
      if (!unauthorized) {
        for (const [path, c] of this.claims) {
          try { await this.send('POST', `${path}/release`, { reason: 'operator session ended' }, { 'Yak-Claim-Proof': c.proof, 'Idempotency-Key': randomKey(), 'Yak-Run-Id': run }, { keepalive }); }
          catch (e) { if (e.status === 401) { unauthorized = true; break; } this.notice = 'Claim release failed; its lease will lapse.'; }
        }
        if (!unauthorized) try { await this.send('DELETE', `/v1/credentials/${this.me.credential.id}`, null, { 'Yak-Run-Id': run }, { keepalive }); }
        catch (e) { if (e.status !== 401) this.notice = 'Token revocation failed. The token may remain valid until expiry.'; }
      }
    } finally {
      for (const path of [...this.claims.keys()]) this.drop(path);
      this.pending.clear(); this.active = false; this.ending = false; this.me = null; this.run = null; this.generation = null; this.changed();
    }
  }
}
