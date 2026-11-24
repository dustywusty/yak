// Viewer regression check. Needs Node 22+ and Chrome; no Yak server, no Go
// proxy, no npm packages. It serves examples/viewer/static itself, answers
// every /v1 request inside the page with a fake API, drives headless Chrome
// over the DevTools protocol, and exits 1 if any check fails.
//
//   node examples/viewer/test/regress.mjs
//   CHROME=/path/to/chrome node examples/viewer/test/regress.mjs
//
// The token is a fake ("yak.test.token"); nothing leaves 127.0.0.1.

import { checkDesignRules } from './design-rules.mjs';
import { createServer } from 'node:http';
import { readFile, mkdtemp, rm } from 'node:fs/promises';
import { existsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, extname, dirname } from 'node:path';
import { spawn } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const STATIC = process.env.VIEWER_STATIC || join(dirname(fileURLToPath(import.meta.url)), '..', 'static');
await checkDesignRules(STATIC);
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
    if (p === '/v1/boards') return json({ boards: T.pickerBoards ?? boards });
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
      const items = threads.flatMap((t) => [{ type: 'thread', id: t.id, data: t }, ...msgs.filter((x) => x.thread_id === t.id && !(T.partial && x.id === 'thr_ops_m2')).map((x) => ({ type: 'message', id: x.id, data: x }))]);
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
    if (p.endsWith('/messages')) {
      if (T.postsError) return json({ code: 'unavailable' }, T.postsError);
      const id = p.split('/').at(-2), rows = msgs.filter(m => m.thread_id === id);
      const token = u.searchParams.get('page_token');
      if (token === 'opaque +/next') {
        count('messagePageTwo');
        if (T.holdMessages) return new Promise(resolve => {
          T.finishMessages = (status = 401) => resolve(status === 200 ? json({ messages: rows.slice(1), next_page_token: null }) : json({}, status));
        });
        return json({ messages: rows.slice(1), next_page_token: null });
      }
      return json({ messages: rows.slice(0, 1), next_page_token: rows.length > 1 ? 'opaque +/next' : null });
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
// Keep startup failure output bounded and free of paths, URLs and credentials.
const sanitize = (value) => String(value)
  .replace(/\b(?:https?|wss?):\/\/[^\s"'<>]+/gi, '<url>')
  .replace(/\b(?:authorization\s*[:=]\s*(?:bearer\s+)?|bearer\s+)[^\r\n,;]+/gi, 'authorization=<redacted>')
  .replace(/\b(?:[\w-]*(?:token|password|credential|secret|api[_-]?key)[\w-]*)["']?\s*[:=]\s*(?:"[^"]*"|'[^']*'|[^\s,;]+)/gi, 'credential=<redacted>')
  .replace(/[A-Za-z]:\\[^\s"'<>]+|\/[^\s"'<>]+/g, '<path>');
const safeCode = (value) => value == null ? null : String(value).replace(/[^A-Z0-9_]/gi, '').slice(0, 80);
const startupFailure = (fields) => console.error('VIEWER_CHROME_STARTUP ' + JSON.stringify(fields)
  .replace(/::/g, '\\u003a\\u003a').replace(/##\[/g, '\\u0023\\u0023['));
await new Promise((r) => server.listen(0, '127.0.0.1', r));
const ORIGIN = `http://127.0.0.1:${server.address().port}`;
if (process.env.VIEWER_PREVIEW) { console.log(`Synthetic viewer: ${ORIGIN}`); await new Promise(() => {}); }
let profile, chrome, browser;
let exitObserved = false, exitCode = null, exitSignal = null, spawnError = null, childClosed = false;
let totalBytes = 0, tail = Buffer.alloc(0);
const collect = (d) => {
  totalBytes += d.length;
  tail = Buffer.concat([tail, d.subarray(-4096)]).subarray(-4096);
};
try {
  profile = await mkdtemp(join(tmpdir(), 'viewer-regress-'));
  chrome = spawn(CHROME, ['--headless=new', '--remote-debugging-port=0', `--user-data-dir=${profile}`, '--no-first-run', 'about:blank'], { stdio: ['ignore', 'ignore', 'pipe'] });
  chrome.on('error', (e) => { spawnError = safeCode(e.code); });
  chrome.on('exit', (code, signal) => { exitObserved = true; exitCode = code; exitSignal = safeCode(signal); });
  chrome.on('close', () => { childClosed = true; });
  chrome.stderr.on('data', collect);
  const wsUrl = await new Promise((resolve, reject) => {
    let buf = '';
    const announce = (d) => {
      buf += d;
      const m = /DevTools listening on (ws:\S+)/.exec(buf);
      if (m) { clearTimeout(timer); chrome.stderr.removeListener('data', announce); resolve(m[1]); }
    };
    const timer = setTimeout(() => { chrome.stderr.removeListener('data', announce); reject(new Error('Chrome did not start')); }, 15000);
    chrome.stderr.on('data', announce);
  });
  chrome.stderr.removeListener('data', collect);
  tail = Buffer.alloc(0);
  browser = new WebSocket(wsUrl);
  await new Promise((r) => browser.addEventListener('open', r));
} catch (error) {
  chrome?.stderr.removeListener('data', collect);
  // Drop a possibly truncated first line before sanitizing the retained tail.
  const stderr = totalBytes > tail.length ? tail.subarray(tail.indexOf(10) < 0 ? tail.length : tail.indexOf(10) + 1) : tail;
  startupFailure({ marker: 'startup_failed', error_code: safeCode(error.code), spawn_error_code: spawnError,
    exit_observed: exitObserved, exit_code: exitCode, signal: exitSignal,
    stderr_truncated: totalBytes > tail.length, sanitized_stderr_tail: sanitize(stderr.toString()) });
  let cleanupError = null;
  const waitForClose = (ms) => new Promise((resolve) => {
    const done = () => { clearTimeout(timer); chrome.removeListener('close', done); resolve(); };
    const timer = setTimeout(done, ms);
    chrome.once('close', done);
  });
  try {
    if (browser) { try { browser.close(); } catch { /* Connecting sockets may already be closed. */ } }
    await new Promise((resolve) => server.close(resolve));
    if (chrome && !childClosed) {
      if (!exitObserved && chrome.pid) chrome.kill('SIGTERM');
      await waitForClose(300);
      if (!childClosed) {
        if (!exitObserved && chrome.pid) chrome.kill('SIGKILL');
        await waitForClose(1000);
      }
    }
    if (chrome && !childClosed) cleanupError = 'direct_child_close_not_observed';
    else if (profile) await rm(profile, { recursive: true, force: true });
  } catch (e) { cleanupError = safeCode(e.code) || 'cleanup_failed'; }
  startupFailure({ marker: 'cleanup_complete', complete: !cleanupError, error_code: cleanupError });
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

async function chatKey(key, code, modifiers = 0) {
  const event = { key, windowsVirtualKeyCode: code, modifiers };
  await cmd('Input.dispatchKeyEvent', { ...event, type: 'keyDown', text: key === 'Enter' ? '\r' : '' });
  await cmd('Input.dispatchKeyEvent', { ...event, type: 'keyUp' });
}

async function checkChatJump(until) {
  await cmd('Page.bringToFront');
  await cmd('Emulation.setDeviceMetricsOverride', { width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
  const thread = '#/b/brd_test?ch=ops&t=thr_ops';
  await cmd('Page.navigate', { url: ORIGIN + '/index.html?chat-jump' + thread });
  await until("document.querySelector('.t-close')");
  await ev("window.openerButton = document.querySelector('.t-jump'); window.openerButton.focus()");
  await chatKey('Enter', 13);
  const selected = (label) => ev(`(() => {
    const input = document.activeElement;
    const list = document.getElementById(input.getAttribute('aria-controls'));
    const options = list?.querySelectorAll('[role=option][aria-selected=true]');
    return list?.role === 'listbox' && options.length === 1 && options[0].textContent === ${JSON.stringify(label)}
      && options[0].id === input.getAttribute('aria-activedescendant') && options[0].getClientRects().length > 0;
  })()`);
  const named = await ev(`document.activeElement.role === 'combobox' && document.activeElement.matches(':focus-visible') &&
    document.activeElement.getAttribute('aria-expanded') === 'true' && !!document.querySelector('.jump:modal[aria-label]')`);
  check('Keyboard Jump opens a named modal with visible combobox focus', named);
  if (!named) return;
  const tree = (await cmd('Accessibility.getFullAXTree')).result.nodes;
  check('Accessibility tree names the Jump dialog, combobox and result list',
    ['dialog', 'combobox', 'listbox'].every(role => tree.some(n => n.role.value === role && n.name?.value)));
  check('Jump limits its unfiltered list to twelve destinations', await ev("document.querySelectorAll('.jump [role=option]').length === 12"));
  await chatKey('ArrowDown', 40);
  check('Down announces Mentions as the sole selected option', await selected('@ Mentions'));
  await chatKey('ArrowUp', 38);
  check('Up announces Activity as the sole selected option', await selected('~ Activity'));
  for (const modifiers of [0, 8]) {
    await chatKey('Tab', 9, modifiers);
    check('Tab stays in Jump, modifiers ' + modifiers, await ev("document.activeElement.role === 'combobox'"));
  }
  await cmd('Input.insertText', { text: 'no such destination' });
  await chatKey('Enter', 13);
  check('Empty Enter retains Jump with no active descendant or options', await ev(`location.hash === ${JSON.stringify(thread)} &&
    document.activeElement.role === 'combobox' && !document.activeElement.hasAttribute('aria-activedescendant') &&
    !document.querySelector('.jump [role=option]')`));
  await ev("window.__addTask('jump_redraw', 'Jump redraw', 'ready')");
  await until("!window.openerButton.isConnected");
  await chatKey('Escape', 27);
  check('Escape restores the redrawn Jump button without closing its thread', await ev(`location.hash === ${JSON.stringify(thread)} &&
    document.activeElement.matches('.t-jump:focus-visible') && !document.querySelector('.jump')`));
  await ev("document.querySelector('.t-close').focus()");
  await chatKey('k', 75, 2);
  await ev("window.oldClose = document.querySelector('.t-close'); window.__addTask('jump_again', 'Another redraw', 'ready')");
  await until("!window.oldClose.isConnected");
  for (const type of ['mousePressed', 'mouseReleased']) {
    await cmd('Input.dispatchMouseEvent', { type, button: 'left', clickCount: 1, x: 2, y: 2 });
  }
  check('Backdrop restores the shortcut opener on the same thread', await ev(`location.hash === ${JSON.stringify(thread)} &&
    document.activeElement.matches('.t-close') && !document.querySelector('.jump')`));
  await chatKey('k', 75, 4);
  await cmd('Input.insertText', { text: 'mention' });
  check('Typing filters to the selected Mentions destination', await selected('@ Mentions'));
  await ev(`window.jumpRoutes = []; addEventListener('hashchange', e => window.jumpRoutes.push(new URL(e.newURL).hash));`);
  await chatKey('Enter', 13);
  await until("location.hash.endsWith('?v=mentions') && !document.querySelector('.jump')");
  await until('window.jumpRoutes.length > 0');
  check('Enter navigates to Mentions once', await ev("window.jumpRoutes.length === 1 && window.jumpRoutes[0] === '#/b/brd_test?v=mentions'"));
  await chatKey('k', 75, 2);
  await ev("document.querySelector('.jump [role=option]').click()");
  await until("location.hash.endsWith('?v=activity') && !document.querySelector('.jump')");
}

async function checkThreadOwnership(until) {
  const thread = '#/b/brd_test?ch=ops&t=thr_ops&m=thr_ops_m2';
  await cmd('Emulation.setDeviceMetricsOverride', { width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
  await cmd('Page.navigate', { url: ORIGIN + '/index.html' + thread });
  await until("document.querySelector('#tm-thr_ops_m2.t-target')");
  await ev(`window.toggle = () => document.querySelector('.a-toggle');
    window.details = () => document.getElementById(toggle().getAttribute('aria-controls'));
    window.inChat = el => {
      const pane = document.querySelector('.t-scroll').getBoundingClientRect(), r = el.getBoundingClientRect();
      return r.height > 0 && r.top >= pane.top && r.bottom <= Math.min(pane.bottom, innerHeight);
    }`);
  const compact = await ev(`toggle()?.checkVisibility() && toggle().textContent === 'Show details' &&
    toggle().getAttribute('aria-expanded') === 'false' && !details().checkVisibility() &&
    document.querySelector('.a-counts').innerText.includes('2 claimed tasks')`);
  check('Desktop thread opens with ownership counts and a collapsed Show details button', compact);
  check('The opening pane shows the thread title, root and highlighted reply', await ev(`
    document.querySelector('.t-h1').getBoundingClientRect().top < 420 &&
    ['.t-h1', '#tm-thr_ops_m1', '#tm-thr_ops_m2.t-target'].every(s => inChat(document.querySelector(s)))`));
  if (!compact) return;
  await ev('toggle().focus()');
  for (const expanded of [true, false, true]) {
    await chatKey('Enter', 13);
    await ev(`window.oldToggle = toggle(); window.__taskEvent('task_second', { updated_at: new Date().toISOString() })`);
    await until('!oldToggle.isConnected');
    check(`Ownership stays ${expanded ? 'expanded' : 'collapsed'} and focused after redraw`, await ev(`
      document.activeElement === toggle() && toggle().getAttribute('aria-expanded') === '${expanded}' &&
      details().checkVisibility() === ${expanded} && location.hash === ${JSON.stringify(thread)} &&
      inChat(document.querySelector('#tm-thr_ops_m2.t-target'))`));
  }
  check('Expanded ownership includes reasons, clocks, activity qualification and freshness', await ev(`
    ['waiting on @alice', 'Claim held for', 'Worker activity unavailable', 'Current task claims and waits'].every(s => details().innerText.includes(s))`));
  await ev(`window.__fake.holdTask = 'task_work'; window.__taskEvent('task_work', { current_claim: {
    ...window.__fake.tasks[0].current_claim, expires_at: new Date(Date.now() - 1000).toISOString() } })`);
  await until("details().innerText.includes('Status unconfirmed')");
  await ev('window.__fake.disconnected = true');
  await until("details().innerText.includes('Last known data')");
  check('Expanded ownership identifies stale data and expired claims', await ev(`
    details().innerText.includes('last reported claim') && document.querySelector('.a-counts').innerText.includes('1 to confirm')`));
  for (const route of ['?v=activity', '?ch=ops']) {
    await go('#/b/brd_test' + route);
    check(`Desktop ${route} shows full ownership by default`, await ev(`
      !toggle()?.checkVisibility() && document.querySelector('.a-freshness').checkVisibility()`));
  }
}

async function checkChatHelp(until) {
  const thread = '#/b/brd_test?ch=ops&t=thr_ops&m=thr_ops_m2';
  for (const width of [390, 1280]) {
    await cmd('Emulation.setDeviceMetricsOverride', { width, height: width === 390 ? 844 : 900, deviceScaleFactor: 1, mobile: width === 390 });
    await cmd('Page.navigate', { url: ORIGIN + '/index.html?chat-help=' + width + thread });
    const opener = width === 390 ? '.t-close' : '.t-side .t-nav.on';
    await until("document.querySelector('#tm-thr_ops_m2.t-target')");
    await ev(`window.helpOpener = document.querySelector('${opener}'); helpOpener.focus()`);
    await chatKey('ArrowDown', 40);
    const selection = await ev("document.querySelector('.t-sel').id");
    await chatKey('?', 191, 8);
    const tree = (await cmd('Accessibility.getFullAXTree')).result.nodes;
    const named = tree.some(n => n.role?.value === 'dialog' && n.name?.value === 'Keys');
    check(`Keys opens a named dialog at ${width}px`, named);
    if (!named) continue;
    const closeFocused = () => ev("document.activeElement.matches('.t-help button:focus-visible') && document.activeElement.textContent === 'Close'");
    check('Keyboard focus enters help on Close', await closeFocused());
    await chatKey('Tab', 9);
    await chatKey('ArrowDown', 40);
    await chatKey('k', 75, 2);
    await ev("window.__addTask('help_redraw', 'Help redraw')");
    await until('!helpOpener.isConnected');
    check('Live redraw and shortcuts keep focus on Close and preserve selection', await closeFocused() &&
      await ev(`!document.querySelector('.jump') && document.querySelector('.t-sel').id === ${JSON.stringify(selection)}`));
    await chatKey(width === 390 ? 'Enter' : 'Escape', width === 390 ? 13 : 27);
    await until("!document.querySelector('.t-help')");
    check('Dismissing help restores the replacement opener, thread and reply', await ev(`
      document.activeElement.matches('${opener}:focus-visible') && location.hash === ${JSON.stringify(thread)} &&
      document.querySelector('#tm-thr_ops_m2.t-target') && document.querySelector('.t-sel').id === ${JSON.stringify(selection)}`));
  }
  await go('#/b/brd_test?ch=ops');
  await ev("document.querySelector('.t-title[data-control=\"thread:thr_ops\"]').focus()");
  await chatKey('?', 191, 8);
  await chatKey('?', 191, 8);
  check('Toggling Keys returns to the focused thread title', await ev(`
    !document.querySelector('.t-help') && document.activeElement.dataset.control === 'thread:thr_ops'`));
}

async function checkChatChannels(until) {
  const thread = '#/b/brd_test?ch=ops&t=thr_ops';
  const viewport = width => cmd('Emulation.setDeviceMetricsOverride', {
    width, height: width === 390 ? 844 : 900, deviceScaleFactor: 1, mobile: width === 390,
  });
  await viewport(390);
  await cmd('Page.navigate', { url: ORIGIN + '/index.html?chat-channels' + thread });
  await until("document.querySelector('.t-close')");
  await ev(`window.menu = () => document.querySelector('.t-menu');
    window.nav = () => document.getElementById(menu().getAttribute('aria-controls'));
    window.channel = () => nav().querySelector('a[href$="?ch=review"]');
    window.routes = []; addEventListener('hashchange', e => routes.push(new URL(e.newURL).hash));
    menu().focus()`);
  await chatKey('Enter', 13);
  const opened = await ev(`menu().getAttribute('aria-expanded') === 'true' && nav()?.matches('nav[aria-label="Channels"]') &&
    !!nav().getClientRects().length && nav().contains(document.activeElement)`);
  check('Phone Channels opens a named navigation region and moves keyboard focus inside', opened);
  if (!opened) return;
  await ev(`channel().focus(); window.oldChannel = channel(); window.__addTask('drawer_redraw', 'Channel redraw')`);
  await until('!oldChannel.isConnected');
  check('Live redraw keeps focus on the same channel', await ev('channel() === document.activeElement'));
  const dismissed = () => ev(`menu().getAttribute('aria-expanded') === 'false' && !nav().getClientRects().length &&
    (channel().focus(), document.activeElement === menu()) && routes.length === 0 && location.hash === ${JSON.stringify(thread)}`);
  await chatKey('Escape', 27);
  check('Escape dismisses Channels, restores its opener and leaves the thread open', await dismissed());
  await chatKey('Enter', 13);
  const closeFocused = await ev("document.activeElement.matches('button[data-control=close-channels]')");
  await chatKey('Enter', 13);
  check('The drawer close button works from the keyboard', closeFocused && await dismissed());
  // Click over a real header action in the uncovered strip.
  const point = await ev(`(() => { const r = document.querySelector('.t-close').getBoundingClientRect();
    return { x: r.right - 2, y: r.top + r.height / 2 }; })()`);
  await chatKey('Enter', 13);
  for (const type of ['mousePressed', 'mouseReleased']) {
    await cmd('Input.dispatchMouseEvent', { type, ...point, button: 'left', clickCount: 1 });
  }
  check('Outside click dismisses Channels without activating the underlying close-thread link', await dismissed());
  await chatKey('Enter', 13);
  await ev('channel().focus()');
  await chatKey('Enter', 13);
  await until('routes.length > 0');
  check('Selecting a channel closes navigation and changes route once', await ev(`routes.length === 1 &&
    location.hash === '#/b/brd_test?ch=review' && !nav().getClientRects().length && menu().getAttribute('aria-expanded') === 'false'`));
  await ev('menu().focus()');
  await chatKey('Enter', 13);
  await viewport(1280);
  await until("nav().getClientRects().length && !document.querySelector('.t-main').inert");
  check('Desktop keeps channel links keyboard accessible and hides the drawer controls', await ev(`
    (channel().focus(), document.activeElement === channel()) && !menu().getClientRects().length &&
    !nav().querySelector('[data-control="close-channels"]').getClientRects().length`));
}

async function checkClearLaneFilters(until) {
  const hash = '#/board?b=brd_test&view=compact';
  await cmd('Page.navigate', { url: ORIGIN + '/index.html?clear-filters' + hash });
  await until("document.querySelectorAll('.w-card').length === 6");
  const clear = "document.querySelector('.w-count + button')";
  check('Unfiltered Lanes has no visible Clear filters action', await ev(`!${clear}?.getClientRects().length`));
  await go(hash + '&ch=ops&agent=act_alice&label=unmatched');
  check('Combined filters show zero of six tasks', await ev(`
    document.querySelector('.w-count').textContent === 'Showing 0 of 6 tasks' &&
    !document.querySelector('.w-card')`));
  const offered = await ev(`${clear}?.textContent === 'Clear filters' && !!${clear}.getClientRects().length`);
  check('Filtered Lanes offers Clear filters beside the count', offered);
  if (!offered) return;
  await ev(`${clear}.focus()`);
  await cmd('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Enter', code: 'Enter', text: '\r', windowsVirtualKeyCode: 13 });
  await cmd('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Enter', windowsVirtualKeyCode: 13 });
  check('Enter clears all three filters and retains the board, route and unrelated parameter', await ev(`
    location.hash === ${JSON.stringify(hash)} && document.querySelectorAll('.w-card').length === 6 &&
    document.querySelector('.w-count').textContent === 'Showing 6 of 6 tasks' &&
    [...document.querySelectorAll('.w-filters select, .w-filters input')].every(el => el.value === '')`));
  check('Clearing filters returns focus to the channel control', await ev(`
    !${clear}?.getClientRects().length && document.activeElement === document.querySelector('.w-filters select')`));
}

// Exercise the lane menu with native type-ahead and Tab, then revisit a card.
async function checkLaneJump(until) {
  await cmd('Emulation.setDeviceMetricsOverride', { width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
  // Start a fresh history entry after the touch test has restored a scrolled board.
  await cmd('Page.navigate', { url: ORIGIN + '/index.html?lane-jump#/board?b=brd_test' });
  await until("!!document.querySelector('.w-card')");
  const menu = "document.querySelector('select[aria-label=\"Jump to lane\"]')";
  const board = "document.querySelector('.w-cols')";
  const visible = await ev(`!!${menu}?.getClientRects().length`);
  check('Phone offers a named lane menu at Ready', visible && await ev(`${board}.scrollLeft === 0`));
  if (!visible) return;
  const countsMatch = () => ev(`(() => {
    const lanes = [...document.querySelectorAll('.w-col h2')];
    const options = [...${menu}.options].filter(option => option.value);
    return options.length === 7 && lanes.length === 7 && lanes.every((h, i) =>
      options[i].value === h.firstChild.textContent.toLowerCase() &&
      options[i].textContent === h.firstChild.textContent + ' (' + h.lastChild.textContent + ')');
  })()`);
  const key = async (key, code, char) => {
    await cmd('Input.dispatchKeyEvent', { type:'keyDown', key, windowsVirtualKeyCode:code, text:char });
    await cmd('Input.dispatchKeyEvent', { type:'keyUp', key, windowsVirtualKeyCode:code });
  };
  const jump = async (name) => {
    await ev(`${menu}.focus()`);
    const letter = name[0];
    await key(letter, letter.toUpperCase().charCodeAt(0), letter);
    check('Select ' + name + ' by name and focus its visible heading', await ev(`(() => {
      const h = document.querySelector('.w-col-${name} h2');
      return document.activeElement === h && Math.abs(h.getBoundingClientRect().left - ${board}.getBoundingClientRect().left) <= 1;
    })()`));
  };
  check('All seven destinations match headings and counts', await countsMatch());
  await jump('blocked');
  await ev(`const channel = document.querySelector('.w-filters select'); channel.value='activity'; channel.dispatchEvent(new Event('change'))`);
  check('Filtered destinations include the now-empty Blocked lane', await countsMatch() &&
    await ev(`${menu}.querySelector('[value="blocked"]').textContent === 'Blocked (0)'`));
  await jump('waiting');
  await ev("for(let i=0;i<18;i++) window.__addTask('jump_' + i, 'Waiting case ' + i, 'waiting')");
  await until("document.querySelector('.w-col-waiting h2 span').textContent === '20'");
  check('Live updates preserve the Waiting heading focus and update menu counts',
    await countsMatch() && await ev("document.activeElement === document.querySelector('.w-col-waiting h2')"));
  await key('Tab', 9);
  const list = "document.querySelector('.w-col-waiting .w-list')";
  check('Tab from the heading reaches its list without document overflow', await ev(`document.activeElement === ${list} && document.documentElement.scrollWidth === innerWidth`));
  const link = await ev(`(() => {
    const a = ${list}.querySelector('[data-task="jump_8"] .w-title');
    a.scrollIntoView({block:'center', inline:'nearest'});
    a.focus();
    return { hash:a.hash, left:${board}.scrollLeft, top:${list}.scrollTop };
  })()`);
  await key('Enter', 13, '\r');
  await until(`location.hash === ${JSON.stringify(link.hash)} && !!document.querySelector('.t-close')`);
  await ev('history.back()');
  await until("!!document.querySelector('.w-card')");
  check('Back from a jumped-to card restores its filter, both offsets and exact link',
    link.left > 0 && link.top > 0 && await ev(`document.activeElement.hash === ${JSON.stringify(link.hash)} &&
      document.querySelector('.w-filters select').value === 'activity' && ${board}.scrollLeft === ${link.left} && ${list}.scrollTop === ${link.top}`));
  await cmd('Emulation.setDeviceMetricsOverride', { width:1280, height:900, deviceScaleFactor:1, mobile:false });
  check('Desktop keeps horizontal columns without the narrow-screen menu', await ev(`!${menu}.getClientRects().length &&
    document.querySelector('.w-col-claimed').getBoundingClientRect().left > document.querySelector('.w-col-ready').getBoundingClientRect().left`));
}

// Exercise the browser's nested scroll handling with native touch input.
async function checkBoardPicker(until) {
  const name = 'UnbrokenBoardName'.repeat(5);
  await ev(`window.__fake.pickerBoards = [
    { id: 'brd_test', name: ${JSON.stringify(name)}, caller_is_board_admin: true, created_at: '2026-01-01T00:00:00Z' },
    { id: 'brd_planning', name: 'Release planning', caller_is_board_admin: false, created_at: '2026-01-02T00:00:00Z' }
  ]; 1`);
  await cmd('Page.bringToFront');
  await cmd('Emulation.setDeviceMetricsOverride', { width: 320, height: 844, deviceScaleFactor: 1, mobile: true });
  await go('#/boards');
  const rows = await ev(`[...document.querySelectorAll('#board-list li')].map(row => {
    const link = row.querySelector('a');
    const box = link.getBoundingClientRect(), item = row.getBoundingClientRect();
    const date = row.querySelector('.small').getBoundingClientRect();
    const point = { x: date.left + date.width / 2, y: date.top + date.height / 2 };
    const hit = (x, y) => document.elementFromPoint(x, y)?.closest('a') === link;
    return { width: box.width, height: box.height, itemWidth: item.width, itemHeight: item.height,
      fullTarget: box.width === item.width && item.height - box.height <= 1 &&
        hit(item.left + 1, item.top + 1) && hit(item.right - 2, item.bottom - 2) && hit(point.x, point.y),
      datePoint: point, name: row.querySelector('strong').textContent,
      wraps: row.querySelector('strong').getClientRects().length > 1 && row.scrollWidth <= row.clientWidth,
      admin: link.textContent.includes('board admin') };
  })`);
  check('picker rows include their padding and date in one full-size target at 320px',
    rows.length === 2 && rows.every(r => r.fullTarget && r.width >= 24 && r.height >= 24), JSON.stringify(rows));
  check('picker preserves a long wrapping name and shows admin only for the reported board',
    rows[0].name === name && rows[0].wraps && rows[0].admin && !rows[1].admin);
  await ev(`document.querySelector('#forget').focus()`);
  await chatKey('Tab', 9);
  check('Tab focuses the first board link with a visible outline', await ev(`
    document.activeElement === document.querySelector('#board-list li:first-child a:focus-visible') &&
    parseFloat(getComputedStyle(document.activeElement).outlineWidth) > 0`));
  await chatKey('Tab', 9);
  check('the next tab stop is the second board link', await ev(`
    document.activeElement === document.querySelector('#board-list li:nth-child(2) a')`));
  await chatKey('Enter', 13);
  await until(`location.hash === '#/b/brd_planning' && document.querySelector('.t-foot')?.textContent.includes('Release planning')`);
  await go('#/boards');
  await cmd('Emulation.setTouchEmulationEnabled', { enabled: true });
  await cmd('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [rows[0].datePoint] });
  await cmd('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
  await until(`location.hash === '#/b/brd_test' && !!document.querySelector('.t-foot')`);
  await cmd('Emulation.setTouchEmulationEnabled', { enabled: false });
  await ev('window.__fake.pickerBoards = []; 1');
  await go('#/boards');
  check('empty board picker stays inert', await ev(`
    document.querySelector('#board-list').textContent === 'No visible boards.' &&
    !document.querySelector('#board-list :is(a,button,input,[tabindex])')`));
  await ev('delete window.__fake.pickerBoards');
}

async function checkLaneTouch(until) {
  await cmd('Emulation.setDeviceMetricsOverride', { width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
  await cmd('Emulation.setTouchEmulationEnabled', { enabled: true });
  await cmd('Page.navigate', { url: ORIGIN + '/index.html#/board?b=brd_test' });
  const ready = '.w-col-ready';
  await until(`!!document.querySelector('${ready} .w-card')`);
  const offsets = () => ev(`[document.querySelector('.w-cols').scrollLeft, document.querySelector('${ready} .w-list').scrollTop]`);
  const touch = (type, touchPoints) => cmd('Input.dispatchTouchEvent', { type, touchPoints });
  async function drag(x, y, dx, dy) {
    await touch('touchStart', [{ x, y }]);
    for (let step = 1; step <= 6; step++) {
      await touch('touchMove', [{ x: x + dx * step / 6, y: y + dy * step / 6 }]);
      await sleep(24);
    }
    await sleep(150); // Lift after stopping so momentum cannot carry into the next trial.
    await touch('touchEnd', []);
    await sleep(100);
  }
  async function horizontal(selector, dx) {
    const { x, y, hit } = await ev(`(() => {
      const el = document.querySelector('${ready} ${selector}');
      const r = el.getBoundingClientRect();
      const x = Math.round((Math.max(0, r.left) + Math.min(innerWidth, r.right)) / 2);
      const y = Math.round(r.top + r.height / 2);
      return { x, y, hit: el.contains(document.elementFromPoint(x, y)) };
    })()`);
    if (!hit) throw new Error('Touch missed ' + selector);
    const [before] = await offsets();
    await drag(x, y, dx, 0);
    const [after] = await offsets();
    return { moved: (after - before) * Math.sign(-dx) > 0, before, after };
  }

  for (const count of [1, 30]) {
    if (count === 30) {
      await ev(`for (let i = 1; i < 30; i++) window.__addTask('touch_' + i, 'Touch task ' + i); 1`);
      await until(`document.querySelectorAll('${ready} .w-card').length === 30`);
    }
    check('Ready list overflow matches fixture of ' + count, await ev(`(() => {
      const list = document.querySelector('${ready} .w-list');
      return (list.scrollHeight > list.clientHeight) === ${count === 30};
    })()`));
    for (let trial = 1; trial <= 3; trial++) {
      for (const target of ['h2', '.w-card']) {
        for (const dx of [-90, 110]) {
          const move = await horizontal(target, dx);
          check(`Ready (${count}), trial ${trial}, ${target} swipe ${dx < 0 ? 'left' : 'right'}`, move.moved, JSON.stringify(move));
        }
      }
    }
  }

  // Keep both scroll offsets nonzero for the redraw and return-navigation checks.
  await horizontal('h2', -60);
  const { x, y } = await ev(`(() => {
    const r = document.querySelector('${ready} .w-list').getBoundingClientRect();
    return { x: 80, y: Math.round(r.top + r.height / 2) };
  })()`);
  const [left, top] = await offsets();
  await drag(x, y, 0, -150);
  const [upLeft, upTop] = await offsets();
  await drag(x, y, 0, 70);
  const [downLeft, downTop] = await offsets();
  check('Vertical drags scroll the Ready list in both directions within its lane',
    upTop > top && downTop < upTop && downTop > 0 && left > 0 && upLeft === left && downLeft === left);

  const link = await ev(`(() => {
    const list = document.querySelector('${ready} .w-list');
    for (const el of list.querySelectorAll('.w-title')) {
      const r = el.getBoundingClientRect();
      const x = (Math.max(0, r.left) + Math.min(innerWidth, r.right)) / 2, y = r.top + r.height / 2;
      if (document.elementFromPoint(x, y) !== el) continue;
      el.focus({ preventScroll: true });
      return { x, y, hash: el.hash, task: el.closest('[data-task]').dataset.task };
    }
  })()`);
  await ev(`for (let i = 1; i <= 80; i++) window.__taskEvent('task_owner', { title: 'Update ' + i }); 1`);
  await until(`document.querySelector('[data-task="task_owner"] .w-title')?.textContent === 'Update 80'`);
  const retained = async () => {
    const [newLeft, newTop] = await offsets();
    return newLeft === left && newTop === downTop && await ev(`document.activeElement === document.querySelector('[data-task="${link.task}"] .w-title')`);
  };
  check('80 live updates preserve the touched lane offsets and focused link', await retained());
  await touch('touchStart', [{ x: link.x, y: link.y }]);
  await touch('touchEnd', []);
  await until(`location.hash === ${JSON.stringify(link.hash)} && !!document.querySelector('.t-close')`);
  await ev('history.back()');
  await until(`!!document.querySelector('${ready} .w-card')`);
  check('Tapping the exact thread and going Back restores offsets and card focus', await retained());
}

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

  await go('#/forum?b=brd_test&ch=ops%2Fdeploy&t=missing');
  const recoveryLink = '.f-content h2 + p a';
  check('Unavailable thread offers its channel index beside the error', await ev(`
    document.querySelector('.f-content h2').textContent === 'Thread unavailable.' &&
    document.querySelector('${recoveryLink}')?.hash === '#/forum?b=brd_test&ch=ops%2Fdeploy'`));
  await ev(`document.querySelector('${recoveryLink}').click()`);
  await until("!!document.querySelector('[data-thread=thr_deploy]')");
  check('Returning to the channel index focuses the filtered list heading', await ev(`
    document.activeElement === document.querySelector('.f-content h2') &&
    document.activeElement.textContent.includes('#ops/deploy') && !!document.querySelector('[data-thread=thr_deploy]')`));
  await go('#/forum?b=brd_planning&ch=missing&t=missing');
  check('Unavailable channel falls back to the same board index', await ev(`
    document.querySelector('${recoveryLink}')?.hash === '#/forum?b=brd_planning'`));
  await ev('window.__fake.holdMessages=true');
  await go('#/forum?b=brd_test&ch=ops&t=thr_ops');
  await until("!!window.__fake.finishMessages");
  await go('#/forum?b=brd_test');
  await ev('window.__fake.holdMessages=false; window.__fake.postsError=403');
  await go('#/forum?b=brd_test&ch=ops&t=thr_ops');
  await until("document.querySelector('.f-content h2')?.textContent === 'Thread unavailable.'");
  check('Denied post read offers the channel index beside Retry', await ev(`
    document.querySelector('.f-content [role=status] a')?.hash === '#/forum?b=brd_test&ch=ops' &&
    document.querySelector('.f-content [role=status] button')?.textContent === 'Retry'`));
  check('Unavailable Forum thread renders only recovery controls', await ev(`
    document.querySelector('.f-content').children.length === 2 &&
    !document.querySelector('.f-content article, .f-content .f-meta, #forum-task-heading')`));
  await ev("document.querySelector('.f-content [role=status] a').click()");
  await until("document.querySelector('.f-content h2')?.textContent.startsWith('#ops')");
  await ev("window.__fake.finishMessages(200); delete window.__fake.finishMessages; 1");
  await sleep(100);
  check('Late success from a canceled read leaves the unavailable thread out of the index', await ev("!document.querySelector('.f-summary')"));
  await go('#/boards');
  await go('#/forum?b=brd_test');
  await until("!!document.querySelector('.f-content [data-thread=thr_deploy]')");
  check('Remounted index retains the visibility decision', await ev(`
    !document.querySelector('.f-content [data-thread=thr_ops]')`));
  await ev("window.__fake.postsError=404; window.__fake.disconnected=true; 1");
  await go('#/forum?b=brd_test&ch=review&t=thr_review');
  await until("!!document.querySelector('.f-content [role=status] button')");
  check('Unavailable task during a poll outage keeps only the recovery view', await ev(`
    document.querySelector('.f-content h2').textContent === 'Thread unavailable.' &&
    !document.querySelector('.f-content article, .f-content .f-meta, #forum-task-heading')`));
  await ev("document.querySelector('.f-content [role=status] a').click()");
  await until("document.querySelector('.f-content h2')?.textContent.startsWith('#review')");
  check('Disconnected channel index omits the unavailable task', await ev("!document.querySelector('.f-content [data-thread]')"));
  await go('#/forum?b=brd_test&ch=review&t=thr_review');
  await until("!!document.querySelector('.f-content [role=status] button')");
  await ev("window.__fake.postsError=false; document.querySelector('.f-content [role=status] button').click()");
  await until("!!document.querySelector('.f-opening')");
  check('Successful retry restores a readable thread', await ev("document.querySelector('.f-content h2').textContent === 'Review thread'"));
  await ev('window.__fake.disconnected=false');
  await go('#/forum?b=brd_test&ch=ops&t=thr_ops');
  await until("!!document.querySelector('.f-opening')");
  await go('#/forum?b=brd_test');
  check('Revalidated threads return to the index', await ev(`
    !!document.querySelector('.f-content [data-thread=thr_review]') && !!document.querySelector('.f-content [data-thread=thr_ops]')`));
  await ev("document.querySelector('[data-thread=thr_deploy] .f-task').click()");
  await until("!!document.querySelector('.f-opening')");
  check('Forum task link targets the approval ask inside the Deploy thread', await ev(`(() => {
    const details = document.querySelector('[aria-labelledby="forum-task-heading"]');
    return document.querySelector('.f-content h2').textContent === 'Deploy thread' &&
      document.activeElement.id === 'forum-task-heading' && details?.textContent.includes('Approve production rollout') &&
      ['blocked', 'Assigned to @alice', 'waiting on @alice'].every(s => details.textContent.includes(s)) &&
      !details.querySelector('a') && !document.querySelector('.f-content .f-task');
  })()`));
  await go('#/forum?b=brd_test&ch=activity&t=thr_wait');
  const dependency = '[aria-labelledby="forum-task-heading"] a[href*="thr_deploy"]';
  check('Forum wait reason links to the readable prerequisite in its channel',
    await ev(`document.querySelector('${dependency}')?.textContent.includes('Approve production rollout') && document.querySelector('${dependency}').hash.includes('ch=ops%2Fdeploy')`));
  await go('#/forum?b=brd_test&ch=activity&t=thr_hidden');
  check('Forum hides private prerequisite details', await ev(`(() => {
    const d = document.querySelector('[aria-labelledby="forum-task-heading"]');
    return d?.textContent.includes('Prerequisite pending (details not visible)') && !d.querySelector('a');
  })()`));
  await go('#/forum?b=brd_test&ch=activity&t=thr_work');
  check('Forum claim identifies the holder and dates the claim age', await ev(`(() => {
    const d = document.querySelector('[aria-labelledby="forum-task-heading"]');
    return d?.textContent.includes('Claim held by @bot-a') && d.textContent.includes('8m') && !!d.querySelector('time');
  })()`));
  await go('#/forum?b=brd_planning');
  await ev("window.__missingTask = window.__fake.tasks.pop(); 1");
  await go('#/forum?b=brd_test&ch=activity&t=thr_hidden&section=task');
  check('Missing task leaves the readable conversation available', await ev(`
    document.activeElement.id === 'forum-task-heading' && document.activeElement.textContent === 'Task details unavailable.' &&
    !!document.querySelector('.f-opening') && !document.querySelector('.f-content .f-task')`));
  await ev("window.__fake.tasks.push(window.__missingTask); delete window.__missingTask");
  await go('#/');
  check('prominent ownership and waiting summary', (await activity()).includes('Claimed tasks') && (await activity()).includes('Waiting'), await activity());
  const windowOwnership = await activity();
  check('ownership does not imply worker activity', ['Board-wide task ownership', 'Worker activity unavailable. A claim does not confirm worker activity.'].every((t) => (windowOwnership || '').includes(t)) && !/Working now|Working for|working actors?/i.test(windowOwnership), windowOwnership);
  check('counts actors separately from multiple claims', (await activity()).includes('1 claiming actor') && (await activity()).includes('2 claimed tasks'), await activity());
  check('unclaimed agents are named without a process-status guess', (await activity()).includes('No active claimed task: bot-b'), await activity());
  check('longest waits stand out', await ev(`getComputedStyle(document.querySelector('[data-activity-task="task_owner"] time')).color !== getComputedStyle(document.querySelector('[data-activity-task="task_wait"] time')).color`));
  check('claim clock uses acquired_at', (await activity()).includes('Claim held for 8m'), await activity());
  const windowActivity = await activity();
  check('waits show assignee, reason, dependency and time in state', ['bot-a', 'review', 'waiting on @alice', '20m in waiting'].every((t) => (windowActivity || '').includes(t)), '');
  await ev(`document.querySelector('.t-jump').focus(); window.__taskEvent('task_work', { current_claim: { ...window.__fake.tasks[0].current_claim, expires_at: new Date(Date.now() + 300000).toISOString() }, updated_at: new Date().toISOString() }, 'task.claim_renewed'); 1`);
  await sleep(700);
  check('Chat keeps keyboard focus on Jump after a claim renewal', await ev(`document.activeElement === document.querySelector('.t-jump')`));
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
  await ev(`document.querySelector('[data-task="task_list_3"] .w-title').click(); 1`);
  await until(`location.hash.includes('thr_task_list_3') && !!document.querySelector('.t-close')`);
  await ev(`history.back(); 1`);
  await until(`!!document.querySelector('[data-task="task_list_3"]')`);
  check('Back restores the vertical position inside a long lane', await ev(`
    document.querySelector('.w-col-ready .w-list').scrollTop === 180 &&
    document.activeElement === document.querySelector('[data-task="task_list_3"] .w-title')`));
  await ev(`document.querySelector('[data-task="task_work"] .w-title').focus(); window.__taskEvent('task_second',{updated_at:new Date().toISOString()}); 1`);
  await sleep(700);
  check('Lanes card focus survives live redraw', await ev(`document.activeElement?.closest('[data-task]')?.dataset.task === 'task_work'`));
  await ev(`document.querySelector('.w-filters input').focus(); document.querySelector('.w-filters input').value='needs'; document.querySelector('.w-filters input').dispatchEvent(new Event('input')); window.__taskEvent('task_owner',{updated_at:new Date().toISOString()}); 1`);
  await sleep(700);
  check('Lanes label filter keeps focus and filters real labels', await ev(`document.activeElement === document.querySelector('.w-filters input') && document.querySelectorAll('.w-card').length === 1 && !!document.querySelector('[data-task="task_owner"]')`));
  await ev(`for (const [i, value] of ['ops', 'act_alice'].entries()) {
    const select = document.querySelectorAll('.w-filters select')[i];
    select.value = value;
    select.dispatchEvent(new Event('change'));
  }
  window.returnFilters = Object.fromEntries(new URLSearchParams(location.hash.split('?')[1]));
  document.querySelector('.w-cols').scrollLeft = 380;
  window.returnLeft = document.querySelector('.w-cols').scrollLeft;
  document.querySelector('[data-task="task_owner"] .w-title').click(); 1`);
  await until(`location.hash.includes('thr_deploy') && !!document.querySelector('.t-close')`);
  await ev(`history.back(); 1`);
  await until(`!!document.querySelector('.w-card')`);
  check('Back restores channel, actor and label filters with the original card and scroll',
    await ev(`window.returnFilters.ch === 'ops' && window.returnFilters.agent === 'act_alice' && window.returnFilters.label === 'needs' &&
      document.querySelectorAll('.w-filters select')[0].value === 'ops' &&
      document.querySelectorAll('.w-filters select')[1].value === 'act_alice' &&
      document.querySelector('.w-filters input').value === 'needs' && document.querySelectorAll('.w-card').length === 1 &&
      document.activeElement === document.querySelector('[data-task="task_owner"] .w-title') &&
      window.returnLeft > 0 && document.querySelector('.w-cols').scrollLeft === window.returnLeft`));
  await go('#/board?b=brd_test');
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
  await ev('window.__fake.partial=true');
  await go('#/irc?b=brd_test');
  check('Old bookmark opens the Forum for the same board',await ev("location.hash==='#/forum?b=brd_test' && !!document.querySelector('.f-summary')"));
  await ev("document.querySelector('.f-directory a[href$=\"ch=ops\"]').click()");
  await until("document.activeElement?.textContent.startsWith('#ops') && !!document.querySelector('.f-summary')");
  check('Channel index uses exact channel',await ev("document.querySelectorAll('.f-summary').length===1"));
  await ev("document.querySelector('.f-summary h3 a').focus()");
  await cmd('Input.dispatchKeyEvent',{type:'keyDown',key:'Enter',windowsVirtualKeyCode:13});
  await cmd('Input.dispatchKeyEvent',{type:'keyUp',key:'Enter',windowsVirtualKeyCode:13});
  await until("!!document.querySelector('.f-reply')");
  check('Forum thread reads page two outside the snapshot',await ev("location.hash==='#/forum?b=brd_test&ch=ops&t=thr_ops' && window.__fake.calls.messagePageTwo>0 && document.querySelector('.f-reply p').textContent==='ops reply'"));
  await ev("document.querySelector('.f-directory a').focus();window.__fake.queue.push(window.__msgEvent('msg_forum_live','<img src=x onerror=alert(1)>','store'));1");
  await until("document.querySelectorAll('.f-reply').length===2");
  check('Live replies preserve focus and render literal text',await ev("document.activeElement===document.querySelector('.f-directory a') && document.querySelector('.f-reply:last-child p').textContent.startsWith('<img') && !document.querySelector('.f-reply img')"));
  await cmd('Emulation.setDeviceMetricsOverride', {width:390,height:844,deviceScaleFactor:1,mobile:true});
  check('Mobile Forum fits channel links and replies',await ev("document.documentElement.scrollWidth<=390 && document.querySelector('.f-reply').getBoundingClientRect().right<=390"));
  await go('#/forum?b=brd_test&ch=review');
  await ev("window.__fake.postsError=503;document.querySelector('.f-task').click()");
  await until("document.querySelector('.f-content')?.textContent.includes('Could not load posts.')");
  await ev("window.__fake.postsError=false;document.querySelector('.f-content button').click()");
  await until("!!document.querySelector('.f-opening')");
  check('Task link and retry open the Forum conversation',await ev("location.hash==='#/forum?b=brd_test&ch=review&t=thr_review&section=task'"));
  await ev("window.__fake.holdMessages=true;location.hash='#/forum?b=brd_test&ch=ops&t=thr_ops'");
  await until("!!window.__fake.finishMessages");
  await go('#/forum?b=brd_planning');
  check('Empty Forum switches boards without stale content',await ev("document.querySelector('.f-content').textContent.includes('No threads.') && document.querySelector('.f-head').textContent.includes('Release planning')"));
  await ev("window.__fake.finishMessages();window.__fake.holdMessages=false");
  await sleep(100);
  check('A late message page cannot log out the replacement board',await ev("!document.querySelector('#skin').hidden && document.querySelector('.f-head').textContent.includes('Release planning') && !!sessionStorage.getItem('yak-viewer-token')"));
  await go('#/forum?b=brd_test');
  const workCard = `document.querySelector('.f-summary h3 a[href$="t=thr_work"]').closest('article')`;
  await ev(`${workCard}.querySelector('.f-meta a').focus();
    window.__fake.queue.push(window.__msgEvent('msg_focus_redraw','Unrelated ops reply','store')); 1`);
  await until(`document.querySelector('.f-summary h3 a[href$="t=thr_ops"]').closest('article').textContent.includes('8 replies')`);
  check('Live redraw keeps the channel link in its original thread card',
    await ev(`document.activeElement === ${workCard}.querySelector('.f-meta a')`));
  await cmd('Input.dispatchKeyEvent', {type:'keyDown',key:'Tab',windowsVirtualKeyCode:9});
  await cmd('Input.dispatchKeyEvent', {type:'keyUp',key:'Tab',windowsVirtualKeyCode:9});
  check('Next Tab reaches the task in the same thread card',
    await ev(`document.activeElement === ${workCard}.querySelector('.f-task')`));
  await go('#/wall?b=brd_test');
  check('old Wall bookmark remains a Lanes alias and restores global header', await ev(`!!document.querySelector('.w-cols') && !!document.querySelector('body > .top')`));
  await cmd('Emulation.setDeviceMetricsOverride', {width:1280,height:900,deviceScaleFactor:1,mobile:false});

  // R1: a snapshot session is deleted even when the store stops during the snapshot.
  const cleanupPosts = await ev(`window.__fake.posts`);
  await ev(`window.__fake.slowSnapshot = 600; 1`);
  await go('#/forum?b=brd_test', 200);
  await go('#/board?b=brd_test', 200);
  await ev(`window.__fake.slowSnapshot = 0; 1`);
  await sleep(1500);
  const f = await ev(`JSON.stringify({ posts: window.__fake.posts, deletes: window.__fake.deletes })`).then(JSON.parse);
  check('every snapshot session is deleted (R1)', f.deletes === f.posts && f.posts >= cleanupPosts + 2, JSON.stringify(f));

  await go('#/b/brd_test?v=activity');
  await cmd('Emulation.setDeviceMetricsOverride', { width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
  await sleep(300);
  const mobile = await ev(`({ overflow: document.documentElement.scrollWidth > innerWidth, activity: document.querySelector('#term .t-agent-activity').getBoundingClientRect().height, chat: document.querySelector('.t-scroll').getBoundingClientRect().height })`);
  check('390px layout keeps activity and chat usable without horizontal overflow', !mobile.overflow && mobile.activity > 0 && mobile.activity <= 130 && mobile.chat > mobile.activity * 3, JSON.stringify(mobile));

  await ev(`document.querySelector('.a-toggle').click(); 1`);
  check('phone ownership disclosure reveals claim explanations and waits',
    await ev(`document.querySelector('.a-toggle').getAttribute('aria-expanded') === 'true' &&
      document.querySelector('.a-freshness').getBoundingClientRect().height > 0 &&
      document.querySelector('#chat-ownership-details').textContent.includes('Worker activity unavailable')`));

  await go('#/boards');
  check('picker lists both synthetic boards', await ev(`document.querySelectorAll('#board-list a').length === 2 && document.getElementById('board-list').textContent.includes('Release planning')`));
  await ev(`document.querySelector('#board-list a[href*="brd_planning"]').click();1`);
  await sleep(800);
  check('second board opens its own empty Chat without leaking demo tasks', await ev(`location.hash.includes('brd_planning') && document.querySelector('.t-foot').textContent.includes('Release planning') && !document.querySelector('#term').textContent.includes('Build the viewer bundle') && document.getElementById('banner').hidden`));
  await checkBoardPicker(until);
  await go('#/forum?b=brd_test');
  await ev(`document.querySelector('#forget').click();1`);
  check('Forum Forget token restores login and stops the skin', await ev(`!document.querySelector('#login').hidden && !sessionStorage.getItem('yak-viewer-token') && !!document.querySelector('body > .top') && !document.body.dataset.skin`));

  await checkLaneTouch(until);
  await checkLaneJump(until);
  await checkClearLaneFilters(until);
  await checkChatJump(until);
  await checkChatChannels(until);
  await checkChatHelp(until);
  await checkThreadOwnership(until);

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
