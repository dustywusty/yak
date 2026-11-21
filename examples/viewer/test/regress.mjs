// Viewer regression check. Needs Node 22+ and Chrome; no Yak server, no Go
// proxy, no npm packages. It serves examples/viewer/static itself, answers
// every /v1 request inside the page with a fake API, drives headless Chrome
// over the DevTools protocol, and exits 1 if any check fails.
//
//   node examples/viewer/test/regress.mjs
//   CHROME=/path/to/chrome node examples/viewer/test/regress.mjs
//
// The token is a fake ("yak.test.token"); nothing leaves 127.0.0.1.

import { createServer } from 'node:http';
import { readFile, mkdtemp, rm } from 'node:fs/promises';
import { existsSync, realpathSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, extname, dirname, basename, delimiter } from 'node:path';
import { spawn } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const STATIC = process.env.VIEWER_STATIC || join(dirname(fileURLToPath(import.meta.url)), '..', 'static');
const CHROME = process.env.CHROME || ['/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  '/usr/bin/google-chrome', '/usr/bin/chromium', '/usr/bin/chromium-browser'].find(existsSync);
const TYPES = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css' };
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
if (!CHROME && !process.env.VIEWER_PREVIEW) { console.error('Chrome not found; set CHROME=/path/to/chrome'); process.exit(2); }

// ---------- fake Yak API, installed in the page before any script runs ----------
const FAKE = () => {
  const B = 'brd_test', now = Date.now(), iso = (ms) => new Date(now + ms).toISOString();
  const actor = (id, handle, kind, owner = null) => ({ id, kind, handle, display_name: handle, owner_actor_id: owner, server_admin: false, created_at: iso(-9e6) });
  const actors = [actor('act_alice', 'alice', 'human'), actor('act_bot', 'bot-a', 'agent', 'act_alice'), actor('act_view', 'viewer', 'service'), actor('act_bot_b', 'bot-b', 'agent')];
  const chan = (path) => ({ id: `chn_${path.replace('/', '_')}`, board_id: B, path, access_mode: 'inherited', created_by_actor_id: 'act_alice', created_at: iso(-9e6), caller_capabilities: ['read'] });
  const msg = (id, thread, path, seq, text, labels = {}) => ({ id, board_id: B, thread_id: thread, channel_id: `chn_${path.replace('/', '_')}`, channel_path: path, seq, is_root: seq === 1,
    author_actor_id: seq === 1 ? 'act_alice' : 'act_bot', attribution: { credential_id: 'c', run_id: null }, reply_to_message_id: null, text, labels,
    mentions: [], extensions: null, revision: 1, created_at: iso(-8e6 + seq * 1000 + thread.length), edited_at: null, deleted: null });
  const thread = (id, path, title, labels, n) => ({ id, board_id: B, channel_id: `chn_${path.replace('/', '_')}`, channel_path: path, title, root_message_id: `${id}_m1`,
    created_by_actor_id: 'act_alice', labels, revision: 1, message_count: n, last_message_id: `${id}_m${n}`, created_at: iso(-8e6), updated_at: iso(-7e6 + id.length), task_id: null });
  const threads = [thread('thr_ops', 'ops', 'Ops thread', {}, 2), thread('thr_deploy', 'ops/deploy', 'Deploy thread', {}, 1),
    thread('thr_review', 'review', 'Review thread', { status: 'open' }, 1)];
  const msgs = [msg('thr_ops_m1', 'thr_ops', 'ops', 1, 'ops root'), msg('thr_ops_m2', 'thr_ops', 'ops', 2, 'ops reply'),
    msg('thr_deploy_m1', 'thr_deploy', 'ops/deploy', 1, 'deploy root'), msg('thr_review_m1', 'thr_review', 'review', 1, 'review root', { status: 'open' })];
  const claim = (id, minutes) => ({ id: `clm_${id}`, actor_id: 'act_bot', status: 'active', acquired_at: iso(-minutes * 60000), expires_at: iso(120000), lease_seconds: 300 });
  const taskTitles = { task_work: 'Build the viewer bundle', task_second: 'Check browser keyboard access', task_wait: 'Publish the release', task_review: 'Review the browser changes', task_owner: 'Approve production rollout', task_hidden: 'Check a private prerequisite' };
  const task = (id, state, minutes, extra = {}) => ({ id, board_id: B, title: taskTitles[id] || id.replace('task_', ''), thread_id: 'thr_ops', channel_path: 'ops', revision: 1,
    state, created_by_actor_id: 'act_alice', created_at: iso(-9e6), state_changed_at: iso(-minutes * 60000), updated_at: iso(0), assignee_actor_id: 'act_bot', current_claim: null, dependencies: [], ...extra });
  const tasks = [task('task_work', 'claimed', 8, { current_claim: claim('work', 8) }),
    task('task_second', 'claimed', 3, { current_claim: claim('second', 3) }),
    task('task_wait', 'waiting', 20, { dependencies: [{ task_id: 'task_review', state: 'ready' }, { task_id: 'task_owner', state: 'blocked' }] }),
    task('task_review', 'ready', 5, { assignee_actor_id: 'act_alice', thread_id: 'thr_review' }),
    task('task_owner', 'blocked', 90, { assignee_actor_id: 'act_alice', thread_id: 'thr_deploy', block: { reason: 'waiting on @alice' } }),
    task('task_hidden', 'waiting', 2, { assignee_actor_id: null, unreadable_dependencies_pending: true })];
  // Each task has its own thread, just as on a real board. Keep the old
  // ops/review fixtures for the channel-filter regression checks below.
  for (const t of tasks) {
    if (!['task_owner', 'task_review'].includes(t.id)) {
      t.thread_id = `thr_${t.id.slice(5)}`;
      t.channel_path = 'activity';
      threads.push(thread(t.thread_id, 'activity', t.title, {}, 1));
      msgs.push(msg(`${t.thread_id}_m1`, t.thread_id, 'activity', 1, t.title));
    }
    threads.find((th) => th.id === t.thread_id).task_id = t.id;
  }
  threads.find((t) => t.id === 'thr_review').labels.kind = 'review';
  threads.find((t) => t.id === 'thr_deploy').labels.needs = 'owner';
  const T = window.__fake = { channels: ['ops', 'ops/deploy', 'review', 'activity'], queue: [], calls: {}, posts: 0, deletes: 0, slowSnapshot: 0, resync: false, tasks, disconnected: false, taskReads: 0, holdTask: null, heldTasks: [] };
  const boards = ['demo', 'Release planning'].map((name, i) => ({ id: i ? 'brd_planning' : B, name, owner_actor_id: 'act_alice', revision: 1, policy_revision: 1, created_at: iso(-9e6), updated_at: iso(-9e6), caller_is_board_admin: false }));
  const json = (body, status = 200) => new Response(body == null ? null : JSON.stringify(body), { status, headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json' } });
  const count = (k) => { T.calls[k] = (T.calls[k] || 0) + 1; };
  window.fetch = async (url, opts = {}) => {
    const u = new URL(url, location.href), p = u.pathname, m = opts.method || 'GET';
    if (opts.signal?.aborted) throw new DOMException('aborted', 'AbortError'); // as the real fetch does
    if (p === '/v1/server') return new Response(JSON.stringify({ limits: { max_page_size: 200, max_wait_ms: 30000 } }), { headers: { Date: new Date().toUTCString() } });
    if (!/^Bearer yak\.test\.token$/.test(opts.headers?.Authorization || '')) return json({ code: 'unauthenticated', status: 401, detail: 'no token' }, 401);
    count(`${m} ${p.replace(B, 'B').replace(/snp_\w+/, 'S')}`);
    if (p === '/v1/me') return json({ actor: actors[2], credential: { id: 'cred_v', actor_id: 'act_view', scopes: ['read'], board_ids: boards.map((b) => b.id) } });
    if (p === '/v1/boards') return json({ boards });
    if (p === `/v1/boards/${B}`) return json(boards[0]);
    // A second, empty board makes the picker useful in repeatable previews.
    if (p.startsWith('/v1/boards/brd_planning')) {
      if (p === '/v1/boards/brd_planning') return json(boards[1]);
      if (p.endsWith('/actors')) return json({ actors });
      if (p.endsWith('/channels')) return json({ channels: [] });
      if (p.endsWith('/snapshots') && m === 'POST') return json({ id: 'snp_planning', board_id: boards[1].id, item_count: 0, created_at: iso(0), expires_at: iso(6e5) }, 201);
      if (p.endsWith('/snapshots/snp_planning')) return m === 'DELETE' ? new Response(null, { status: 204 }) : json({ snapshot: { id: 'snp_planning' }, items: [], next_page_token: null, continuation_cursor: 'c1.planning' });
      if (p.endsWith('/events')) {
        await new Promise((r) => setTimeout(r, 300));
        return json({ events: [], next_cursor: 'c1.planning', caught_up: true, timed_out: true });
      }
    }
    if (p.endsWith('/actors')) return json({ actors });
    if (p.endsWith('/channels')) return json({ channels: T.channels.map(chan) });
    if (p.endsWith('/snapshots') && m === 'POST') { T.posts++; return json({ id: `snp_${T.posts}`, board_id: B, item_count: 7, created_at: iso(0), expires_at: iso(6e5) }, 201); }
    if (/\/snapshots\/snp_\w+$/.test(p) && m === 'DELETE') { T.deletes++; return new Response(null, { status: 204 }); }
    if (/\/snapshots\/snp_\w+$/.test(p)) {
      if (T.slowSnapshot) await new Promise((r) => setTimeout(r, T.slowSnapshot));
      const items = threads.flatMap((t) => [{ type: 'thread', id: t.id, data: t }, ...msgs.filter((x) => x.thread_id === t.id).map((x) => ({ type: 'message', id: x.id, data: x }))]);
      items.push(...T.tasks.map((t) => ({ type: 'task', id: t.id, data: t })));
      return json({ snapshot: { id: 'snp' }, items, next_page_token: null, continuation_cursor: 'c1.store' });
    }
    if (/\/tasks\/task_\w+$/.test(p)) {
      T.taskReads++;
      const body = structuredClone(T.tasks.find((t) => p.endsWith(t.id)));
      // Hold an already-produced response; deliberately ignore a later abort.
      // This also exercises callers when response parsing outlives cancellation.
      if (p.endsWith(T.holdTask || '!')) return new Promise((resolve, reject) => {
        T.heldTasks.push({ release: (status = 200) => resolve(json(status === 200 ? body : { code: 'unauthenticated' }, status)), reject });
      });
      return json(body);
    }
    if (p.endsWith('/events')) {
      if (T.disconnected) throw new TypeError('synthetic disconnect');
      if (T.resync && u.searchParams.get('cursor') !== 'c1.feed') { T.resync = false; return json({ code: 'cursor_expired' }, 410); }
      const cursor = u.searchParams.get('cursor');
      if (!cursor) return json({ events: [], next_cursor: 'c1.feed', caught_up: true, timed_out: false });
      const who = cursor === 'c1.feed' ? 'feed' : 'store';
      const wait = Math.min(+u.searchParams.get('wait_ms') || 0, 300);
      await new Promise((r, j) => { const t = setTimeout(r, wait); opts.signal?.addEventListener('abort', () => { clearTimeout(t); j(new DOMException('aborted', 'AbortError')); }); });
      const events = T.queue.filter((e) => !e.__for || e.__for === who).map(({ __for, ...e }) => e);
      if (who === 'feed') T.queue = T.queue.filter((e) => e.__for === 'store');
      if (who === 'store') T.queue = T.queue.filter((e) => e.__for === 'feed');
      return json({ events, next_cursor: cursor, caught_up: true, timed_out: !events.length });
    }
    if (/\/threads\/[\w]+$/.test(p)) { const t = threads.find((x) => p.endsWith(x.id)); return t ? json({ thread: t, root_message: msgs.find((x) => x.id === t.root_message_id) }) : json({ code: 'not_found', status: 404, detail: 'x' }, 404); }
    return json({ code: 'not_found', status: 404, detail: `fake: no route ${m} ${p}` }, 404);
  };
  window.__addTask = (id, title, state = 'ready') => {
    const t = task(id, state, 5, { title, thread_id: `thr_${id}`, channel_path: 'activity' });
    threads.push({ ...thread(t.thread_id, 'activity', title, { area: 'viewer' }, 1), task_id: id });
    msgs.push(msg(`${t.thread_id}_m1`, t.thread_id, 'activity', 1, title));
    T.tasks.push(t);
    T.queue.push({ __for: 'store', id: `evt_${id}`, type: 'task.created', payload: { task: t } });
  };
  window.__taskEvent = (id, patch, type = 'task.updated') => {
    const i = T.tasks.findIndex((t) => t.id === id);
    T.tasks[i] = { ...T.tasks[i], ...patch, revision: T.tasks[i].revision + 1 };
    T.queue.push({ __for: 'store', id: `evt_${id}_${T.tasks[i].revision}`, type, payload: { task: T.tasks[i] } });
  };
  // Helper for checks: a message.created event for both loops.
  window.__msgEvent = (id, text, forWho) => ({ __for: forWho, id, cursor: 'c1.x', type: 'message.created', board_id: B, actor_id: 'act_bot', credential_id: null, run_id: null,
    resource: { type: 'message', id, revision: 1 }, channel_id: 'chn_ops', thread_id: 'thr_ops', recorded_at: new Date().toISOString(), effective_at: new Date().toISOString(),
    payload: { message: { ...msgs[1], id, seq: 9, text, created_at: new Date().toISOString() }, thread_title: 'Ops thread', task_id: null } });
};

// ---------- static server and Chrome ----------
const server = createServer(async (req, res) => {
  const path = new URL(req.url, 'http://x').pathname.replace(/^\/$/, '/index.html');
  if (path.includes('..')) { res.writeHead(400).end(); return; }
  if (process.env.VIEWER_PREVIEW && path === '/fixture.js') { res.writeHead(200, { 'Content-Type': 'text/javascript' }).end(`(${FAKE})(); sessionStorage.setItem('yak-viewer-token', 'yak.test.token');`); return; }
  try { let body = await readFile(join(STATIC, path));
    if (process.env.VIEWER_PREVIEW && path === '/index.html') body = body.toString().replace('<script type="module"', '<script src="/fixture.js"></script><script type="module"');
    res.writeHead(200, { 'Content-Type': TYPES[extname(path)] || 'application/octet-stream' }).end(body); }
  catch { res.writeHead(404).end(); }
});
// Diagnostics never feed the readiness buffer or change its 15000 ms decision.
const STARTUP_PREFIX = 'VIEWER_CHROME_STARTUP ';
let spawnClock;
const elapsed = () => spawnClock === undefined ? 'not_observed' : Number((performance.now() - spawnClock).toFixed(3));
const sanitize = (value) => String(value)
  .replace(/\b(?:https?|wss?):\/\/[^\s"'<>]+/gi, '<url>')
  .replace(/\b(?:authorization\s*[:=]\s*(?:bearer\s+)?|bearer\s+)[^\r\n,;]+/gi, 'authorization=<redacted>')
  .replace(/\b(?:[\w-]*(?:token|password|credential|secret|api[_-]?key)[\w-]*)["']?\s*[:=]\s*(?:"[^"]*"|'[^']*'|[^\s,;]+)/gi, 'credential=<redacted>')
  .replace(/[A-Za-z]:\\[^\s"'<>]+|\/[^\s"'<>]+/g, '<path>');
const safeCode = (value) => value == null ? null : String(value).replace(/[^A-Z0-9_]/gi, '').slice(0, 80);
const emit = (marker, fields = {}) => {
  const record = { marker, elapsed_ms: elapsed(), ...fields };
  const encode = () => STARTUP_PREFIX + JSON.stringify(record).replace(/::/g, '\\u003a\\u003a').replace(/##\[/g, '\\u0023\\u0023[');
  let line = encode();
  // JSON escaping can expand a byte-bounded tail by up to six times.
  while (Buffer.byteLength(line) + 1 > 32768 && record.sanitized_stderr_tail) {
    record.sanitized_stderr_tail = record.sanitized_stderr_tail.slice(1024);
    record.emitted_tail_shortened = true;
    line = encode();
  }
  console.error(line);
};
const binaryLabel = (binary) => process.env.CHROME ? `<custom-path>/${sanitize(basename(binary)).slice(0, 256)}` : binary;
const resolveBinary = () => {
  try {
    const candidate = CHROME.includes('/') ? CHROME : (process.env.PATH || '').split(delimiter).map((p) => join(p, CHROME)).find(existsSync);
    return { resolved_binary: binaryLabel(realpathSync(candidate || CHROME)) };
  } catch (e) { return { resolution_error: safeCode(e.code) || 'unavailable' }; }
};
const versionProbe = () => new Promise((resolve) => {
  const start = performance.now();
  const child = spawn(CHROME, ['--version'], { stdio: ['ignore', 'pipe', 'pipe'] });
  let output = Buffer.alloc(0), total = 0, error = null, closed = false, observed = false, code = null, signal = null, timedOut = false;
  const signals = [];
  const collect = (chunk) => { total += chunk.length; output = Buffer.concat([output, chunk.subarray(0, Math.max(0, 1024 - output.length))]); };
  child.stdout.on('data', collect);
  child.stderr.on('data', collect);
  child.on('error', (e) => { error = safeCode(e.code); });
  child.on('exit', (c, s) => { observed = true; code = c; signal = s; });
  const timers = [];
  const finish = () => {
    timers.forEach(clearTimeout);
    resolve({ status: closed && !timedOut && !error && code === 0 ? 'available' : 'unavailable',
      elapsed_ms: Number((performance.now() - start).toFixed(3)), reaped: closed, timed_out: timedOut,
      exit_observed: observed, exit_code_or_null: code, signal_or_null: signal, spawn_error_code_or_null: error,
      retained_bytes: output.length, total_bytes: total, truncated: total > output.length,
      sanitized_output: sanitize(output.toString()), signals_sent: signals });
  };
  child.on('close', () => { closed = true; finish(); });
  const stop = (s) => { if (!closed && !observed && child.pid) { signals.push(s); child.kill(s); } };
  // Reserve part of the independent 2000 ms total budget for termination/reaping.
  timers.push(setTimeout(() => { timedOut = true; stop('SIGTERM'); }, 1600));
  timers.push(setTimeout(() => stop('SIGKILL'), 1750));
  timers.push(setTimeout(finish, 2000));
});
await new Promise((r) => server.listen(0, '127.0.0.1', r));
emit('server_listening');
const ORIGIN = `http://127.0.0.1:${server.address().port}`;
if (process.env.VIEWER_PREVIEW) { console.log(`Synthetic viewer: ${ORIGIN}`); await new Promise(() => {}); }
let profile, chrome, browser;
let exitObserved = false, exitCode = null, exitSignal = null, spawnError = null, childClosed = false;
let totalBytes = 0, tail = Buffer.alloc(0), collecting = true, firstByte = false, startupReady = false;
let failureStage = 'profile_creation';
const cleanupSignals = [];
try {
  profile = await mkdtemp(join(tmpdir(), 'viewer-regress-'));
  emit('profile_created');
  const launchArgs = ['--headless=new', '--remote-debugging-port=0', `--user-data-dir=${profile}`, '--no-first-run', 'about:blank'];
  failureStage = 'devtools_announcement';
  spawnClock = performance.now();
  emit('spawn_requested');
  chrome = spawn(CHROME, launchArgs, { stdio: ['ignore', 'ignore', 'pipe'] });
  chrome.on('spawn', () => emit('spawned', { pid_or_null: chrome.pid ?? null }));
  chrome.on('error', (e) => { spawnError = safeCode(e.code); });
  chrome.on('exit', (code, signal) => {
    exitObserved = true; exitCode = code; exitSignal = signal;
    emit('child_exit', { exit_observed: true, exit_code_or_null: code, signal_or_null: signal,
      cleanup_induced: cleanupSignals.length > 0 ? true : (startupReady ? 'not_observed' : false), signals_sent: [...cleanupSignals] });
  });
  chrome.on('close', () => { childClosed = true; });
  const collect = (d) => {
    if (!collecting) return;
    if (!firstByte) { firstByte = true; emit('stderr_first_byte'); }
    totalBytes += d.length;
    tail = Buffer.concat([tail, d.subarray(Math.max(0, d.length - 16384))]).subarray(-16384);
  };
  chrome.stderr.on('data', collect);
  const wsUrl = await new Promise((resolve, reject) => {
    let buf = '';
    chrome.stderr.on('data', (d) => { buf += d; const m = /DevTools listening on (ws:\S+)/.exec(buf); if (m) resolve(m[1]); });
    setTimeout(() => reject(new Error('Chrome did not start')), 15000);
  });
  collecting = false;
  chrome.stderr.removeListener('data', collect);
  tail = Buffer.alloc(0);
  startupReady = true;
  emit('devtools_announced');
  failureStage = 'websocket_open';
  browser = new WebSocket(wsUrl);
  await new Promise((r) => browser.addEventListener('open', r));
  emit('websocket_open');
} catch (originalError) {
  collecting = false;
  // Freeze natural observations before either the probe or cleanup sends signals.
  const snapshot = { failure_stage: failureStage, elapsed_ms: elapsed(), chosen_binary: binaryLabel(CHROME),
    resolved_binary_or_resolution_error: resolveBinary(),
    sanitized_launch_args: ['--headless=new', '--remote-debugging-port=0', '--user-data-dir=<profile>', '--no-first-run', 'about:blank'],
    pid_or_null: chrome?.pid ?? null, node_version: process.version, exit_observed: exitObserved,
    exit_code_or_null: exitCode, signal_or_null: exitSignal, spawn_error_code_or_null: spawnError,
    stderr_total_bytes: totalBytes, stderr_retained_bytes: tail.length, stderr_truncated: totalBytes > tail.length,
    // A truncated first line may start midway through a credential or path.
    sanitized_stderr_tail: sanitize((totalBytes > tail.length ? tail.subarray(tail.indexOf(10) < 0 ? tail.length : tail.indexOf(10) + 1) : tail).toString()) };
  let probe;
  try { probe = await versionProbe(); }
  catch { probe = { status: 'unavailable', reaped: false, error: 'probe_failed' }; }
  emit('startup_failed', { ...snapshot, runtime_version_probe_result: probe });
  let cleanupError = null;
  try {
    if (browser) browser.close();
    await new Promise((resolve) => server.close(resolve));
    if (chrome && !childClosed) {
      if (!exitObserved && chrome.pid) { cleanupSignals.push('SIGTERM'); chrome.kill('SIGTERM'); }
      await Promise.race([new Promise((r) => chrome.once('close', r)), sleep(300)]);
      if (!childClosed) {
        if (!exitObserved && chrome.pid) { cleanupSignals.push('SIGKILL'); chrome.kill('SIGKILL'); }
        await Promise.race([new Promise((r) => chrome.once('close', r)), sleep(1000)]);
      }
    }
    if (chrome && !childClosed) cleanupError = 'direct_child_close_not_observed';
    else if (profile) await rm(profile, { recursive: true, force: true });
  } catch (e) { cleanupError = safeCode(e.code) || 'cleanup_failed'; }
  emit('cleanup_complete', { complete: !cleanupError && probe.reaped === true, error: cleanupError,
    direct_child_closed: chrome ? childClosed : 'not_spawned', profile_removed: !cleanupError,
    version_probe_reaped: probe.reaped === true, signals_sent: cleanupSignals, descendants: 'not_asserted' });
  // Do not serialize arbitrary error messages (they can contain custom paths).
  console.error('Chrome startup failed');
  process.exit(1);
}
let id = 0; const pending = new Map(); const errors = [];
browser.addEventListener('message', (m) => {
  const d = JSON.parse(m.data);
  if (d.id && pending.has(d.id)) { pending.get(d.id)(d); pending.delete(d.id); }
  if (d.method === 'Runtime.exceptionThrown') errors.push(d.params.exceptionDetails.exception?.description || d.params.exceptionDetails.text);
  if (d.method === 'Runtime.consoleAPICalled' && d.params.type === 'error') errors.push(`console.error: ${d.params.args.map((a) => a.description || a.value).join(' ')}`);
});
let session;
const cmd = (method, params = {}) => new Promise((r) => { const i = ++id; pending.set(i, r); browser.send(JSON.stringify({ id: i, method, params, ...(session ? { sessionId: session } : {}) })); });
session = undefined;
const { result: { targetId } } = await cmd('Target.createTarget', { url: 'about:blank' });
session = (await cmd('Target.attachToTarget', { targetId, flatten: true })).result.sessionId;
const ev = async (expr) => {
  const r = await cmd('Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true });
  if (r.result.exceptionDetails) throw new Error(`eval failed: ${r.result.exceptionDetails.exception?.description}`);
  return r.result.result.value;
};
await cmd('Runtime.enable');
await cmd('Page.enable');
await cmd('Page.addScriptToEvaluateOnNewDocument', { source: `(${FAKE})(); sessionStorage.setItem('yak-viewer-token', 'yak.test.token');
  addEventListener('DOMContentLoaded', () => { window.__live = document.getElementById('live'); });` });

// ---------- checks ----------
let failed = 0;
const check = (name, ok, detail = '') => { console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${ok ? '' : `  (${detail})`}`); if (!ok) failed++; };
const go = async (hash, wait = 800) => { await ev(`location.hash = ${JSON.stringify(hash)}; 1`); await sleep(wait); };
// The feed element is read through a reference taken at load: it may be out of the document.
const state = () => ev(`JSON.stringify({ hash: location.hash, liveInDoc: document.contains(window.__live), status: window.__live.querySelector('#feed-status').textContent,
  rows: [...window.__live.querySelectorAll('#feed li')].map((l) => l.textContent), banner: document.getElementById('banner').hidden ? '' : document.getElementById('banner').textContent,
  term: !document.getElementById('term').hidden, calls: window.__fake.calls })`).then(JSON.parse);

try {
  await cmd('Page.navigate', { url: `${ORIGIN}/#/` });
  await sleep(1500);
  let s = await state();
  check('lands on Activity with the feed in the page', s.term && s.liveInDoc, JSON.stringify(s));
  check('feed status is live', s.status.startsWith('live'), s.status);

  const activity = () => ev(`document.querySelector('.t-agent-activity')?.textContent || ''`);
  const until = async (expr) => {
    for (let n = 0; n < 50; n++) { if (await ev(expr)) return; await sleep(100); }
    throw new Error(`condition not reached: ${expr}`);
  };

  check('prominent ownership and waiting summary', (await activity()).includes('Claimed tasks') && (await activity()).includes('Waiting'), await activity());
  const windowOwnership = await activity();
  check('ownership does not imply worker activity', ['Task ownership', 'Worker activity unavailable. A claim does not confirm worker activity.'].every((t) => (windowOwnership || '').includes(t)) && !/Working now|Working for|working actors?/i.test(windowOwnership), windowOwnership);
  check('counts actors separately from multiple claims', (await activity()).includes('1 claiming actor') && (await activity()).includes('2 claimed tasks'), await activity());
  check('unclaimed agents are named without a process-status guess', (await activity()).includes('No active claimed task: bot-b'), await activity());
  check('longest waits stand out', await ev(`getComputedStyle(document.querySelector('[data-activity-task="task_owner"] time')).color !== getComputedStyle(document.querySelector('[data-activity-task="task_wait"] time')).color`));
  check('claim clock uses acquired_at', (await activity()).includes('Claim held for 8m'), await activity());
  const windowActivity = await activity();
  check('waits show assignee, reason, dependency and time in state', ['bot-a', 'review', 'waiting on @alice', '20m in waiting'].every((t) => (windowActivity || '').includes(t)), '');
  await ev(`window.__taskEvent('task_work', { current_claim: { ...window.__fake.tasks[0].current_claim, expires_at: new Date(Date.now() + 300000).toISOString() }, updated_at: new Date().toISOString() }, 'task.claim_renewed'); 1`);
  await sleep(700);
  check('renewal does not reset claim clock', (await activity()).includes('Claim held for 8m'), await activity());
  await ev(`window.__taskEvent('task_owner', { updated_at: new Date().toISOString(), block: { reason: 'Changed reason' } }); 1`);
  await sleep(700);
  check('reason update keeps duration labeled as time in state', (await activity()).includes('1h 30m in blocked') && (await activity()).includes('Changed reason'), await activity());
  await ev(`window.__taskEvent('task_work', { current_claim: { ...window.__fake.tasks[0].current_claim, id: 'clm_new', acquired_at: new Date(Date.now() - 120000).toISOString() } }, 'task.claimed'); 1`);
  await sleep(700);
  check('new claim uses new acquired_at', (await ev(`document.querySelector('[data-activity-task="task_work"] time').textContent`)).includes('Claim held for 2m'), await activity());
  await ev(`window.__taskEvent('task_wait', { state: 'blocked', state_changed_at: new Date().toISOString(), block: { reason: 'Check results' } }, 'task.blocked'); 1`);
  await sleep(700);
  check('state transition resets state clock and shows explicit reason', /[0-2]s in blocked/.test(await activity()) && (await activity()).includes('Check results'), await activity());
  await ev(`window.__taskEvent('task_review', { state: 'completed' }, 'task.completed'); 1`);
  await sleep(700);
  check('completed dependency no longer shown as a wait cause', !(await ev(`document.querySelector('[data-activity-task="task_wait"] .a-reasons').textContent`)).includes('review'), await activity());
  check('hidden prerequisite has no invented details', (await activity()).includes('Prerequisite pending (details not visible)'), await activity());
  await ev(`document.querySelector('[data-activity-task="task_work"] a').focus(); window.__taskEvent('task_second', { updated_at: new Date().toISOString() }); 1`);
  await sleep(700);
  check('keyboard link focus survives live updates', await ev(`document.activeElement?.closest('[data-activity-task]')?.dataset.activityTask === 'task_work'`));
  await cmd('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13 });
  await cmd('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13 });
  await sleep(400);
  check('Enter opens focused task thread', await ev(`location.hash.includes('t=thr_work')`));
  const ticking = await ev(`document.querySelector('[data-activity-task="task_wait"] time').textContent`);
  await sleep(1200);
  check('state timers tick without task events', ticking !== await ev(`document.querySelector('[data-activity-task="task_wait"] time').textContent`));
  await ev(`window.__fake.disconnected = true; 1`);
  await sleep(800);
  check('disconnect marks last known data', (await activity()).includes('Last known'), await activity());
  check('stale long waits use the last-known text color', await ev(`getComputedStyle(document.querySelector('[data-activity-task="task_owner"] time')).color === getComputedStyle(document.querySelector('.a-unclaimed')).color`));
  const frozen = await ev(`document.querySelector('[data-activity-task="task_wait"] time').textContent`);
  await sleep(1200);
  check('disconnected elapsed clocks freeze', frozen === await ev(`document.querySelector('[data-activity-task="task_wait"] time').textContent`));
  await ev(`window.__fake.disconnected = false; window.__taskEvent('task_second', { state: 'ready', current_claim: null }, 'task.released'); 1`);
  // Reconnect uses a jittered, exponential backoff; wait for its actual result.
  await until(`document.querySelector('#term .t-agent-activity').textContent.includes('1 claimed task') && !document.querySelector('#term .t-agent-activity').textContent.includes('Last known')`);
  check('reconnect removes released claim', (await activity()).includes('1 claimed task') && !(await activity()).includes('Last known'), await activity());
  await ev(`window.__taskEvent('task_work', { current_claim: { ...window.__fake.tasks[0].current_claim, expires_at: new Date(Date.now() - 1000).toISOString() } }); window.__fake.tasks[0] = { ...window.__fake.tasks[0], state: 'ready', current_claim: null, revision: 99 }; 1`);
  await sleep(1500);
  check('expired claim refresh uses authoritative task read', await ev(`window.__fake.taskReads > 0`) && (await activity()).includes('0 claimed tasks'), await activity());
  const snapshotsBeforeResync = await ev(`window.__fake.posts`);
  await ev(`window.__fake.resync = true; 1`);
  await sleep(900);
  check('resnapshot restores authoritative activity', await ev(`window.__fake.posts > ${snapshotsBeforeResync}`) && (await activity()).includes('0 claimed tasks') && !(await activity()).includes('Last known'), await activity());
  // Hold a valid renewal response across an authoritative snapshot that
  // removes the task. A revision cannot order a response against absence.
  await go('#/boards');
  await go('#/b/brd_test?v=activity');
  await ev(`window.__fake.holdTask = 'task_work'; window.__fake.heldTasks = [];
    window.__taskEvent('task_work', { state: 'claimed', current_claim: { id: 'clm_race', actor_id: 'act_bot', status: 'active', acquired_at: new Date(Date.now()-60000).toISOString(), expires_at: new Date(Date.now()-1000).toISOString() } });
    window.__fake.tasks[0] = { ...window.__fake.tasks[0], revision: window.__fake.tasks[0].revision+1, current_claim: { ...window.__fake.tasks[0].current_claim, expires_at: new Date(Date.now()+120000).toISOString() } }; 1`);
  await until(`window.__fake.heldTasks.length === 1`);
  const racePosts = await ev(`window.__fake.posts`);
  await ev(`window.__removedWork = window.__fake.tasks.shift(); window.__fake.resync = true; 1`);
  await until(`window.__fake.posts > ${racePosts} && !document.querySelector('[data-activity-task="task_work"]')`);
  await ev(`window.__fake.holdTask = null; window.__fake.heldTasks[0].release(); 1`);
  await sleep(700);
  check('old expiry success cannot resurrect a task removed by resnapshot', await ev(`!document.querySelector('[data-activity-task="task_work"]')`) && (await activity()).includes('0 claimed tasks'), await activity());

  // An old 401 must not forget the current token after view disposal.
  await ev(`window.__fake.tasks.unshift({ ...window.__removedWork, revision: 150, current_claim: { ...window.__removedWork.current_claim, expires_at: new Date(Date.now()-1000).toISOString() } }); window.__fake.holdTask='task_work'; window.__fake.heldTasks=[]; window.__fake.resync=true; 1`);
  await until(`window.__fake.heldTasks.length===1`);
  await go('#/boards');
  await ev(`window.__fake.tasks.shift(); window.__fake.holdTask=null; window.__fake.heldTasks[0].release(401); 1`);
  await sleep(300);
  check('disposed expiry HTTP error cannot forget current token', await ev(`sessionStorage.getItem('yak-viewer-token')==='yak.test.token' && !document.getElementById('boards').hidden && document.querySelector('#boards h1')?.textContent === 'Switch board'`));

  // Test lifecycle and bookkeeping directly through the same synthetic API.
  // Stop the mounted store so the harness owns the board's event queue.
  await go('#/boards');
  const lifecycle = await ev(`(async () => {
    const {createStore, clock} = await import('/skin-kit.js');
    const T = window.__fake, results = {};
    const wait = async (fn) => { for(let n=0;n<60;n++){ if(fn()) return; await new Promise(r=>setTimeout(r,100)); } throw Error('lifecycle condition timed out'); };
    const api = async (method,path,params={},signal) => {
      const url = new URL(path,location.href); for(const [k,v] of Object.entries(params)) if(v!=null) url.searchParams.set(k,v);
      const r = await fetch(url,{method,signal,headers:{Authorization:'Bearer yak.test.token'}});
      const body = r.status===204 ? null : await r.json();
      if(!r.ok) throw Object.assign(Error('synthetic HTTP error'),{status:r.status,problem:body});
      return body;
    };
    let changes=0, unauthorized=0, live=0;
    T.tasks.unshift({...window.__removedWork,state:'claimed',revision:200,current_claim:{...window.__removedWork.current_claim,expires_at:new Date(Date.now()-1000).toISOString()}});
    T.holdTask='task_work'; T.heldTasks=[];
    const st=createStore({api,boardId:'brd_test',onChange:()=>changes++,onUnauthorized:()=>unauthorized++,onStatus:s=>{if(s==='live')live++;}});
    await wait(()=>T.heldTasks.length===1);
    if (!st.gated({state:'blocked',direct_action_required:true,thread_id:'thr_ops'})) throw Error('flagged gate without label not recognized');
    st.S.threads.set('thr_label_gate',{labels:{gate:'pending'}});
    if (st.gated({state:'blocked',direct_action_required:false,thread_id:'thr_label_gate'})) throw Error('label alone counted as a gate');
    if (st.gated({state:'ready',direct_action_required:true,thread_id:'thr_ops'})) throw Error('unblocked task counted as pending gate');
    T.resync=true; const posts=T.posts; await wait(()=>T.posts>posts && T.heldTasks.length===2);
    // The old request errors after a new-generation request for the same ID
    // starts. Its catch must not log out, nor its finally clear the new read.
    T.heldTasks[0].release(401); await new Promise(r=>setTimeout(r,100));
    const offset=clock.offset; clock.offset+=11000;
    await new Promise(r=>setTimeout(r,1200)); clock.offset=offset;
    results.oldErrorIgnored=unauthorized===0;
    results.oldFinalizerIgnored=T.heldTasks.length===2;
    // Current-generation stale revisions must still lose to newer events.
    window.__taskEvent('task_work',{state:'ready',current_claim:null,revision:250});
    await wait(()=>st.S.tasks.get('task_work')?.state==='ready');
    T.heldTasks[1].release(); await new Promise(r=>setTimeout(r,100));
    results.revisionProtected=st.S.tasks.get('task_work').state==='ready';
    // Queue another read, dispose the store, then deliver a late success.
    window.__taskEvent('task_work',{state:'claimed',current_claim:{...window.__removedWork.current_claim,expires_at:new Date(Date.now()-1000).toISOString()}});
    await wait(()=>st.S.tasks.get('task_work')?.state==='claimed');
    // A new snapshot clears retry delay, so no real 10-second wait is needed.
    T.resync=true; const p2=T.posts; await wait(()=>T.posts>p2 && T.heldTasks.length===3);
    const before=JSON.stringify(st.S.tasks.get('task_work')); st.stop(); const beforeChanges=changes, beforeLive=live;
    T.heldTasks[2].release(); await new Promise(r=>setTimeout(r,400));
    results.disposedSuccessIgnored=JSON.stringify(st.S.tasks.get('task_work'))===before && changes===beforeChanges && live===beforeLive;
    // The same rule applies to errors after disposal.
    T.heldTasks=[];
    const st2=createStore({api,boardId:'brd_test',onUnauthorized:()=>unauthorized++});
    await wait(()=>T.heldTasks.length===1); st2.stop(); T.heldTasks[0].release(401);
    await new Promise(r=>setTimeout(r,100)); results.disposedErrorIgnored=unauthorized===0;
    T.heldTasks=[];
    const st3=createStore({api,boardId:'brd_test',onUnauthorized:()=>unauthorized++});
    await wait(()=>T.heldTasks.length===1); T.heldTasks[0].release(401);
    await wait(()=>unauthorized===1); st3.stop(); results.currentErrorDelivered=unauthorized===1;
    T.holdTask=null; T.tasks[0]={...T.tasks[0],state:'ready',current_claim:null,revision:300};
    return results;
  })()`);
  for (const [name, ok] of Object.entries(lifecycle)) check(`expiry lifecycle: ${name}`, ok, JSON.stringify(lifecycle));
  await go('#/b/brd_test?v=activity');

  const clockCases = await ev(`import('/agent-activity.js').then(({elapsed}) => [elapsed('bad', Date.now()), elapsed(new Date(60000).toISOString(), 0), elapsed(new Date(0).toISOString(), 3660000), elapsed(new Date(0).toISOString(), 90000000)])`);
  check('duration formatting handles unknown, future, hours and days', JSON.stringify(clockCases) === JSON.stringify(['Time unknown', '0s', '1h 1m', '1d 1h']), JSON.stringify(clockCases));
  check('claim absence never calls an agent idle or offline', (await activity()).includes('No active claimed task') && !/idle|offline/i.test(await activity()), await activity());

  // The reported path: Activity -> a channel (redraw drops #live) -> Activity.
  await go(`#/b/brd_test?ch=review`);
  s = await state();
  check('channel view replaces the feed pane', s.term && !s.liveInDoc, JSON.stringify(s));
  await ev(`window.__fake.queue.push(window.__msgEvent('msg_away', 'arrived while away', 'feed')); 1`);
  await sleep(1200);
  const feedCalls = (await state()).calls['GET /v1/boards/B/events'];
  await go(`#/b/brd_test?v=activity`);
  s = await state();
  check('returning to Activity shows the feed again', s.hash.includes('v=activity') && s.liveInDoc, JSON.stringify(s));
  check('no error banner', s.banner === '', s.banner);
  check('event that arrived while away is in the feed', s.rows.some((r) => r.includes('arrived while away')), JSON.stringify(s.rows));
  await sleep(1200);
  check('poll loops keep polling after the switches', (await state()).calls['GET /v1/boards/B/events'] > feedCalls, 'no new /events calls');

  // Keyboard path: g a from a channel.
  await go(`#/b/brd_test?ch=ops`);
  await ev(`for (const k of ['g', 'a']) document.dispatchEvent(new KeyboardEvent('keydown', { key: k, bubbles: true })); 1`);
  await sleep(800);
  s = await state();
  check('g a opens Activity with the feed', s.hash.includes('v=activity') && s.liveInDoc, JSON.stringify(s));

  // A broken event: its row is skipped, the error is shown once, later events still draw.
  await ev(`window.__fake.queue.push({ __for: 'feed', id: 'evt_bad', payload: {} }); 1`);
  await sleep(900);
  await ev(`window.__fake.queue.push(window.__msgEvent('msg_after', 'after the bad event', 'feed')); 1`);
  await sleep(1200);
  s = await state();
  check('a draw error is reported in the banner', s.banner.includes('Activity feed could not draw'), s.banner);
  check('events after a draw error still appear', s.rows.some((r) => r.includes('after the bad event')), JSON.stringify(s.rows));
  check('feed status still live after a draw error', s.status.startsWith('live'), s.status);

  // R5: old deep links keep sub= and label=.
  const titles = () => ev(`JSON.stringify([...document.querySelectorAll('.t-scroll .t-title')].map((t) => t.textContent))`).then(JSON.parse);
  await go(`#/b/brd_test?ch=ops`);
  check('ch=ops includes subchannels by default', JSON.stringify((await titles()).sort()) === JSON.stringify(['Deploy thread', 'Ops thread']), JSON.stringify(await titles()));
  await go(`#/b/brd_test?ch=ops&sub=0`);
  check('sub=0 shows the exact channel only', JSON.stringify(await titles()) === JSON.stringify(['Ops thread']), JSON.stringify(await titles()));
  await go(`#/b/brd_test?ch=review&label=status:open`);
  check('label=status:open keeps the matching thread', JSON.stringify(await titles()) === JSON.stringify(['Review thread']), JSON.stringify(await titles()));
  await go(`#/b/brd_test?ch=review&label=status:closed`);
  check('label=status:closed removes it', (await titles()).length === 0, JSON.stringify(await titles()));

  // R3: Wall filter lists pick up a channel created later.
  await go('#/wall', 1200);
  await ev(`document.querySelector('.w-filters input').focus(); window.__fake.channels.push('late'); window.__fake.queue.push({ __for: 'store', id: 'evt_chan', cursor: 'c1.x', type: 'channel.created', board_id: 'brd_test', actor_id: 'act_alice',
    credential_id: null, run_id: null, resource: { type: 'channel', id: 'chn_late', revision: 1 }, channel_id: 'chn_late', thread_id: null, recorded_at: new Date().toISOString(),
    effective_at: new Date().toISOString(), payload: { channel: { id: 'chn_late', board_id: 'brd_test', path: 'late', access_mode: 'inherited', created_by_actor_id: 'act_alice', created_at: new Date().toISOString(), caller_capabilities: [] } } }); 1`);
  await sleep(1500);
  const options = await ev(`JSON.stringify([...document.querySelectorAll('.w-filters select')[0].options].map((o) => o.value))`).then(JSON.parse);
  check('Lanes channel filter lists a channel created later', options.includes('late'), JSON.stringify(options));
  check('Lanes filter focus survives newly created channels', await ev(`document.activeElement===document.querySelector('.w-filters input')`));

  // Approved classic views: real states, clocks, links, scroll and live focus.
  await go('#/dispatch?b=brd_test');
  check('removed skin bookmark canonicalizes to Chat with board context', await ev(`location.hash === '#/b/brd_test' && !document.getElementById('term').hidden`));
  await go('#/dispatch');
  check('removed skin bookmark without board canonicalizes to default Chat', await ev(`location.hash === '#/' && !document.getElementById('term').hidden`));
  check('navigation omits removed view and names Lanes', await ev(`![...document.querySelectorAll('a')].some(a => a.textContent === 'Dispatch') && document.querySelector('.t-sidefoot a[href^="#/board?"]')?.textContent === 'Lanes' && document.querySelector('.t-sidefoot a[href="#/boards"]')?.textContent === 'Switch board'`));
  await go('#/board?b=brd_test');
  check('Lanes route mounts separately from Chat', await ev(`!!document.querySelector('.w-cols') && document.getElementById('term').hidden && document.querySelector('.w-head h1')?.textContent === 'demo · Lanes'`));
  await ev(`window.__taskEvent('task_work', { state:'claimed', current_claim:{id:'clm_board',actor_id:'act_bot',status:'active',acquired_at:new Date(Date.now()-482000).toISOString(),expires_at:new Date(Date.now()+120000).toISOString(),lease_seconds:300} });
    window.__taskEvent('task_second', {state:'failed',outcome:{by_actor_id:'act_bot',reason:'Tests failed',at:new Date().toISOString()}});
    window.__taskEvent('task_review', {state:'canceled',outcome:{by_actor_id:'act_alice',reason:'No longer needed',at:new Date().toISOString()}});
    window.__taskEvent('task_wait', {state:'waiting',state_changed_at:new Date(Date.now()-1202000).toISOString(),dependencies:[{task_id:'task_owner',state:'blocked'}]}); 1`);
  await sleep(800);
  check('failed and canceled tasks have their own truthful lists', await ev(`!!document.querySelector('.w-col-failed [data-task="task_second"]') && !!document.querySelector('.w-col-canceled [data-task="task_review"]') && !document.querySelector('.w-col-completed [data-task="task_second"]')`));
  check('Lanes reuses activity summary and real task clocks', await ev(`document.querySelector('.w-wrap .t-agent-activity')?.textContent.includes('Claim held for 8m') && document.querySelector('[data-task="task_work"]')?.textContent.includes('Claim held for 8m') && document.querySelector('[data-task="task_wait"]')?.textContent.includes('20m in waiting')`), await ev(`({summary:document.querySelector('.w-wrap .t-agent-activity')?.textContent,work:document.querySelector('[data-task="task_work"]')?.textContent,wait:document.querySelector('[data-task="task_wait"]')?.textContent})`).then(JSON.stringify));
  check('Lanes cards link visible wait prerequisites and show hidden hints', await ev(`!!document.querySelector('[data-task="task_wait"] .w-reason a[href*="thr_deploy"]') && document.querySelector('[data-task="task_hidden"]')?.textContent.includes('details not visible')`));
  check('Lanes cards carry real labels and actor badges', await ev(`!!document.querySelector('[data-task="task_owner"] .w-label') && !!document.querySelector('[data-task="task_work"] .av')`));
  await ev(`for(let i=0;i<18;i++) window.__addTask('task_list_'+i,'Check browser case '+(i+1)); 1`);
  await sleep(700);
  await ev(`const list=document.querySelector('.w-col-ready .w-list');list.scrollTop=180;list.focus();window.__taskEvent('task_owner',{updated_at:new Date().toISOString()});1`);
  await sleep(700);
  check('Lanes lists scroll independently and retain scroll and focus', await ev(`document.querySelector('.w-col-ready .w-list').scrollTop===180 && document.activeElement===document.querySelector('.w-col-ready .w-list') && document.querySelector('.w-col-claimed .w-list').scrollTop===0`));
  await ev(`document.querySelector('[data-task="task_work"] .w-title').focus(); window.__taskEvent('task_second',{updated_at:new Date().toISOString()}); 1`);
  await sleep(700);
  check('Lanes card focus survives live redraw', await ev(`document.activeElement?.closest('[data-task]')?.dataset.task === 'task_work'`));
  await ev(`document.querySelector('.w-filters input').focus(); document.querySelector('.w-filters input').value='needs'; document.querySelector('.w-filters input').dispatchEvent(new Event('input')); window.__taskEvent('task_owner',{updated_at:new Date().toISOString()}); 1`);
  await sleep(700);
  check('Lanes label filter keeps focus and filters real labels', await ev(`document.activeElement === document.querySelector('.w-filters input') && document.querySelectorAll('.w-card').length === 1 && !!document.querySelector('[data-task="task_owner"]')`));
  await ev(`document.querySelector('.w-filters input').value=''; document.querySelector('.w-filters input').dispatchEvent(new Event('input')); window.__fake.disconnected=true; 1`);
  await sleep(1300);
  check('Lanes disconnect marks last known data', await ev(`document.querySelector('.w-wrap').textContent.includes('Last known')`));
  const boardFrozen = await ev(`document.querySelector('[data-task="task_wait"] time').textContent`);
  await sleep(1200);
  check('Lanes stale card clocks freeze with the summary', boardFrozen === await ev(`document.querySelector('[data-task="task_wait"] time').textContent`));
  await ev(`window.__fake.disconnected=false; 1`);
  await sleep(1800);
  await cmd('Emulation.setDeviceMetricsOverride', { width:390,height:844,deviceScaleFactor:1,mobile:true });
  check('mobile Lanes keeps fixed-width horizontal lists without page overflow', await ev(`document.documentElement.scrollWidth <= innerWidth && document.querySelector('.w-cols').scrollWidth > document.querySelector('.w-cols').clientWidth && document.querySelector('.w-col').getBoundingClientRect().width >= 270`));
  await ev(`document.querySelector('.w-cols').scrollLeft=290; window.__taskEvent('task_owner',{updated_at:new Date().toISOString()}); 1`);
  await sleep(700);
  check('Lanes horizontal position survives live redraw', await ev(`document.querySelector('.w-cols').scrollLeft >= 280`));
  await ev(`window.__fake.holdTask='task_work';window.__fake.heldTasks=[];window.__taskEvent('task_work',{current_claim:{...window.__fake.tasks.find(t=>t.id==='task_work').current_claim,expires_at:new Date(Date.now()-1000).toISOString()}});1`);
  await until(`window.__fake.heldTasks.length > 0`);
  check('Lanes expired claims stay unconfirmed until authoritative read', await ev(`document.querySelector('[data-task="task_work"]')?.textContent.includes('Status unconfirmed') && document.querySelector('[data-task="task_work"] time')?.classList.contains('w-stale') && document.querySelector('.a-counts').textContent.includes('to confirm')`));
  await ev(`window.__taskEvent('task_work',{state:'ready',current_claim:null},'task.released');window.__fake.holdTask=null;window.__fake.heldTasks[0].release();1`);
  await sleep(700);
  check('Lanes release removes claim clock and late read cannot restore claim', await ev(`!!document.querySelector('.w-col-ready [data-task="task_work"]') && !document.querySelector('[data-task="task_work"] time')`));
  await cmd('Emulation.setDeviceMetricsOverride', { width:1280,height:900,deviceScaleFactor:1,mobile:false });
  await go('#/irc?b=brd_test');
  check('IRC frame fills entire viewport with square blue chrome', await ev(`(() => {const r=document.querySelector('.i-win').getBoundingClientRect();return r.x===0 && r.y===0 && r.width===innerWidth && r.height===innerHeight && getComputedStyle(document.querySelector('.i-title')).backgroundImage==='none';})()`));
  check('IRC navigation and logout are inside chrome', await ev(`document.querySelector('.i-win #nav [data-view="skin-board"]')?.textContent === 'Lanes' && document.querySelector('.i-win #nav [data-view="boards"]')?.textContent === 'Switch board' && !!document.querySelector('.i-win #forget')`));
  check('IRC has real channel tree left and board actors right', await ev(`!!document.querySelector('.i-tree button[data-channel="ops"]') && document.querySelector('.i-nicks')?.textContent.includes('bot-a')`));
  await ev(`document.querySelector('.i-tree button[data-channel="ops"]').click(); document.querySelector('.i-tree button[data-channel="ops"]').focus(); window.__fake.queue.push(window.__msgEvent('msg_irc_live','IRC live update','store')); 1`);
  await sleep(700);
  check('IRC live messages retain channel control focus', await ev(`document.querySelector('.i-text').textContent.includes('IRC live update') && document.activeElement?.dataset.channel==='ops'`));
  await ev(`document.querySelector('.i-tools button').focus();window.__fake.queue.push(window.__msgEvent('msg_irc_jump','Control focus on live update','store'));1`);
  await sleep(700);
  check('IRC Jump button focus survives live redraw', await ev(`document.activeElement===document.querySelector('.i-tools button')`));
  await ev(`for(let i=0;i<60;i++) window.__fake.queue.push(window.__msgEvent('msg_scroll_'+i,'Transcript scroll line '+i,'store'));1`);
  await sleep(700);
  await ev(`const text=document.querySelector('.i-text');text.scrollTop=40;text.focus();window.__fake.queue.push(window.__msgEvent('msg_scroll_later','Later live line','store'));1`);
  await sleep(700);
  check('IRC preserves transcript scroll and focus away from bottom', await ev(`document.querySelector('.i-text').scrollTop===40 && document.activeElement===document.querySelector('.i-text')`));
  await ev(`document.dispatchEvent(new KeyboardEvent('keydown',{key:'k',ctrlKey:true,bubbles:true}));1`);
  check('IRC Ctrl-K opens working channel jump', await ev(`!!document.querySelector('.jump input')`));
  await ev(`document.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true}));1`);
  await ev(`document.dispatchEvent(new KeyboardEvent('keydown',{key:'3',altKey:true,bubbles:true})); 1`);
  check('IRC Alt-number selects a real channel', await ev(`document.querySelector('.i-tab.on')?.textContent.includes('#ops/deploy')`));
  await cmd('Emulation.setDeviceMetricsOverride', {width:390,height:844,deviceScaleFactor:1,mobile:true});
  check('mobile IRC keeps transcript and side panes within viewport', await ev(`document.documentElement.scrollWidth<=innerWidth && document.querySelector('.i-win').getBoundingClientRect().height===innerHeight && document.querySelector('.i-text').clientWidth>=140 && document.querySelector('.i-tree').clientWidth>0 && document.querySelector('.i-nicks').clientWidth>0`));
  check('mobile IRC topic thread link stays visible', await ev(`(() => {const a=document.querySelector('.i-topic a').getBoundingClientRect(), p=document.querySelector('.i-topic').getBoundingClientRect();return a.left>=p.left && a.right<=p.right && a.bottom<=p.bottom;})()`));
  check('IRC input is explicitly read-only', await ev(`document.querySelector('.i-input').disabled && document.querySelector('.i-input').getAttribute('aria-label').includes('read-only')`));
  await go('#/wall?b=brd_test');
  check('old Wall bookmark remains a Lanes alias and restores global header', await ev(`!!document.querySelector('.w-cols') && !!document.querySelector('body > .top') && !document.querySelector('.i-win')`));
  await cmd('Emulation.setDeviceMetricsOverride', {width:1280,height:900,deviceScaleFactor:1,mobile:false});

  // R1: a snapshot session is deleted even when the store stops during the snapshot.
  const cleanupPosts = await ev(`window.__fake.posts`);
  await ev(`window.__fake.slowSnapshot = 600; 1`);
  await go('#/irc?b=brd_test', 200);
  await go('#/board?b=brd_test', 200);
  await ev(`window.__fake.slowSnapshot = 0; 1`);
  await sleep(1500);
  const f = await ev(`JSON.stringify({ posts: window.__fake.posts, deletes: window.__fake.deletes })`).then(JSON.parse);
  check('every snapshot session is deleted (R1)', f.deletes === f.posts && f.posts >= cleanupPosts + 2, JSON.stringify(f));

  await go('#/b/brd_test?v=activity');
  await cmd('Emulation.setDeviceMetricsOverride', { width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
  await sleep(300);
  const mobile = await ev(`({ overflow: document.documentElement.scrollWidth > innerWidth, activity: document.querySelector('#term .t-agent-activity').getBoundingClientRect().height, chat: document.querySelector('.t-scroll').getBoundingClientRect().height })`);
  check('390px layout keeps activity and chat usable without horizontal overflow', !mobile.overflow && mobile.activity >= 100 && mobile.chat >= 150, JSON.stringify(mobile));

  await go('#/boards');
  check('picker lists both synthetic boards', await ev(`document.querySelectorAll('#board-list a').length === 2 && document.getElementById('board-list').textContent.includes('Release planning')`));
  await ev(`document.querySelector('#board-list a[href*="brd_planning"]').click();1`);
  await sleep(800);
  check('second board opens its own empty Chat without leaking demo tasks', await ev(`location.hash.includes('brd_planning') && document.querySelector('.t-foot').textContent.includes('Release planning') && !document.querySelector('#term').textContent.includes('Build the viewer bundle') && document.getElementById('banner').hidden`));
  await go('#/irc?b=brd_test');
  await ev(`document.querySelector('.i-win #forget').click();1`);
  check('IRC Forget token restores login and stops the skin', await ev(`!document.querySelector('#login').hidden && !sessionStorage.getItem('yak-viewer-token') && !!document.querySelector('body > .top') && !document.body.dataset.skin`));

  const unexpected = errors.filter((e) => !e.includes('Activity feed: draw failed')); // that one is provoked above
  check('no uncaught exceptions or console errors', unexpected.length === 0, unexpected.join(' | '));
} catch (e) {
  check('test run', false, [e.message, ...errors].join(' | '));
} finally {
  browser.close();
  chrome.kill();
  server.close();
  await sleep(300);
  await rm(profile, { recursive: true, force: true }).catch(() => {});
}
console.log(failed ? `${failed} check(s) failed` : 'all checks passed');
process.exit(failed ? 1 : 0);
