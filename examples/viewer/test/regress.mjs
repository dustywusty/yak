// Viewer regression check. Needs Node 22+ and Chrome; no Abby server, no Go
// proxy, no npm packages. It serves examples/viewer/static itself, answers
// every /v1 request inside the page with a fake API, drives headless Chrome
// over the DevTools protocol, and exits 1 if any check fails.
//
//   node examples/viewer/test/regress.mjs
//   CHROME=/path/to/chrome node examples/viewer/test/regress.mjs
//
// The token is a fake ("abby.test.token"); nothing leaves 127.0.0.1.

import { createServer } from 'node:http';
import { readFile, mkdtemp, rm } from 'node:fs/promises';
import { existsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, extname, dirname } from 'node:path';
import { spawn } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const STATIC = process.env.VIEWER_STATIC || join(dirname(fileURLToPath(import.meta.url)), '..', 'static');
const CHROME = process.env.CHROME || ['/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  '/usr/bin/google-chrome', '/usr/bin/chromium', '/usr/bin/chromium-browser'].find(existsSync);
const TYPES = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css' };
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
if (!CHROME) { console.error('Chrome not found; set CHROME=/path/to/chrome'); process.exit(2); }

// ---------- fake Abby API, installed in the page before any script runs ----------
const FAKE = () => {
  const B = 'brd_test', now = Date.now(), iso = (ms) => new Date(now + ms).toISOString();
  const actor = (id, handle, kind, owner = null) => ({ id, kind, handle, display_name: handle, owner_actor_id: owner, server_admin: false, created_at: iso(-9e6) });
  const actors = [actor('act_alice', 'alice', 'human'), actor('act_bot', 'bot-a', 'agent', 'act_alice'), actor('act_view', 'viewer', 'service')];
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
  const T = window.__fake = { channels: ['ops', 'ops/deploy', 'review'], queue: [], calls: {}, posts: 0, deletes: 0, slowSnapshot: 0 };
  const json = (body, status = 200) => new Response(body == null ? null : JSON.stringify(body), { status, headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json' } });
  const count = (k) => { T.calls[k] = (T.calls[k] || 0) + 1; };
  window.fetch = async (url, opts = {}) => {
    const u = new URL(url, location.href), p = u.pathname, m = opts.method || 'GET';
    if (opts.signal?.aborted) throw new DOMException('aborted', 'AbortError'); // as the real fetch does
    if (p === '/v1/server') return new Response(JSON.stringify({ limits: { max_page_size: 200, max_wait_ms: 30000 } }), { headers: { Date: new Date().toUTCString() } });
    if (!/^Bearer abby\.test\.token$/.test(opts.headers?.Authorization || '')) return json({ code: 'unauthenticated', status: 401, detail: 'no token' }, 401);
    count(`${m} ${p.replace(B, 'B').replace(/snp_\w+/, 'S')}`);
    if (p === '/v1/me') return json({ actor: actors[2], credential: { id: 'cred_v', actor_id: 'act_view', scopes: ['read'], board_ids: [B] } });
    if (p === '/v1/boards') return json({ boards: [{ id: B, name: 'demo', owner_actor_id: 'act_alice', revision: 1, policy_revision: 1, created_at: iso(-9e6), updated_at: iso(-9e6), caller_is_board_admin: false }] });
    if (p === `/v1/boards/${B}`) return json({ id: B, name: 'demo', owner_actor_id: 'act_alice', revision: 1, policy_revision: 1, created_at: iso(-9e6), updated_at: iso(-9e6), caller_is_board_admin: false });
    if (p.endsWith('/actors')) return json({ actors });
    if (p.endsWith('/channels')) return json({ channels: T.channels.map(chan) });
    if (p.endsWith('/snapshots') && m === 'POST') { T.posts++; return json({ id: `snp_${T.posts}`, board_id: B, item_count: 7, created_at: iso(0), expires_at: iso(6e5) }, 201); }
    if (/\/snapshots\/snp_\w+$/.test(p) && m === 'DELETE') { T.deletes++; return new Response(null, { status: 204 }); }
    if (/\/snapshots\/snp_\w+$/.test(p)) {
      if (T.slowSnapshot) await new Promise((r) => setTimeout(r, T.slowSnapshot));
      const items = threads.flatMap((t) => [{ type: 'thread', id: t.id, data: t }, ...msgs.filter((x) => x.thread_id === t.id).map((x) => ({ type: 'message', id: x.id, data: x }))]);
      return json({ snapshot: { id: 'snp' }, items, next_page_token: null, continuation_cursor: 'c1.store' });
    }
    if (p.endsWith('/events')) {
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
  // Helper for checks: a message.created event for both loops.
  window.__msgEvent = (id, text, forWho) => ({ __for: forWho, id, cursor: 'c1.x', type: 'message.created', board_id: B, actor_id: 'act_bot', credential_id: null, run_id: null,
    resource: { type: 'message', id, revision: 1 }, channel_id: 'chn_ops', thread_id: 'thr_ops', recorded_at: new Date().toISOString(), effective_at: new Date().toISOString(),
    payload: { message: { ...msgs[1], id, seq: 9, text, created_at: new Date().toISOString() }, thread_title: 'Ops thread', task_id: null } });
};

// ---------- static server and Chrome ----------
const server = createServer(async (req, res) => {
  const path = new URL(req.url, 'http://x').pathname.replace(/^\/$/, '/index.html');
  if (path.includes('..')) { res.writeHead(400).end(); return; }
  try { const body = await readFile(join(STATIC, path)); res.writeHead(200, { 'Content-Type': TYPES[extname(path)] || 'application/octet-stream' }).end(body); }
  catch { res.writeHead(404).end(); }
});
await new Promise((r) => server.listen(0, '127.0.0.1', r));
const ORIGIN = `http://127.0.0.1:${server.address().port}`;
const profile = await mkdtemp(join(tmpdir(), 'viewer-regress-'));
const chrome = spawn(CHROME, ['--headless=new', '--remote-debugging-port=0', `--user-data-dir=${profile}`, '--no-first-run', 'about:blank'], { stdio: ['ignore', 'ignore', 'pipe'] });
const wsUrl = await new Promise((resolve, reject) => {
  let buf = '';
  chrome.stderr.on('data', (d) => { buf += d; const m = /DevTools listening on (ws:\S+)/.exec(buf); if (m) resolve(m[1]); });
  setTimeout(() => reject(new Error('Chrome did not start')), 15000);
});
const browser = new WebSocket(wsUrl);
await new Promise((r) => browser.addEventListener('open', r));
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
await cmd('Page.addScriptToEvaluateOnNewDocument', { source: `(${FAKE})(); sessionStorage.setItem('abby-viewer-token', 'abby.test.token');
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
  await ev(`window.__fake.channels.push('late'); window.__fake.queue.push({ __for: 'store', id: 'evt_chan', cursor: 'c1.x', type: 'channel.created', board_id: 'brd_test', actor_id: 'act_alice',
    credential_id: null, run_id: null, resource: { type: 'channel', id: 'chn_late', revision: 1 }, channel_id: 'chn_late', thread_id: null, recorded_at: new Date().toISOString(),
    effective_at: new Date().toISOString(), payload: { channel: { id: 'chn_late', board_id: 'brd_test', path: 'late', access_mode: 'inherited', created_by_actor_id: 'act_alice', created_at: new Date().toISOString(), caller_capabilities: [] } } }); 1`);
  await sleep(1500);
  const options = await ev(`JSON.stringify([...document.querySelectorAll('.w-filters select')[0].options].map((o) => o.value))`).then(JSON.parse);
  check('Wall channel filter lists a channel created later', options.includes('late'), JSON.stringify(options));

  // R1: a snapshot session is deleted even when the store stops during the snapshot.
  await ev(`window.__fake.slowSnapshot = 600; 1`);
  await go('#/dispatch', 200);
  await go('#/irc', 200);
  await ev(`window.__fake.slowSnapshot = 0; 1`);
  await sleep(1500);
  const f = await ev(`JSON.stringify({ posts: window.__fake.posts, deletes: window.__fake.deletes })`).then(JSON.parse);
  check('every snapshot session is deleted (R1)', f.deletes === f.posts, JSON.stringify(f));

  const unexpected = errors.filter((e) => !e.includes('Activity feed: draw failed')); // that one is provoked above
  check('no uncaught exceptions or console errors', unexpected.length === 0, unexpected.join(' | '));
} catch (e) {
  check('test run', false, e.message);
} finally {
  browser.close();
  chrome.kill();
  server.close();
  await sleep(300);
  await rm(profile, { recursive: true, force: true }).catch(() => {});
}
console.log(failed ? `${failed} check(s) failed` : 'all checks passed');
process.exit(failed ? 1 : 0);
