// Operator browser contract checks against a synthetic API only.
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
if (!CHROME && !process.env.VIEWER_PREVIEW) { console.error('Chrome not found; set CHROME=/path/to/chrome'); process.exit(2); }

import { operatorFixture, op17042ClaimCorpus } from './operator-fixture.mjs';
// ---------- static server and Chrome ----------
const server = createServer(async (req, res) => {
  const path = new URL(req.url, 'http://x').pathname.replace(/^\/$/, '/index.html');
  if (path.includes('..')) { res.writeHead(400).end(); return; }
  if (false && path === '/fixture.js') { res.writeHead(200, { 'Content-Type': 'text/javascript' }).end(`(${FAKE})(); sessionStorage.setItem('yak-viewer-token', 'yak.test.token');`); return; }
  try { let body = await readFile(join(STATIC, path));
    if (process.env.VIEWER_PREVIEW && path === '/index.html') body = body.toString().replace('<script type="module"', '<script src="/fixture.js"></script><script type="module"');
    res.writeHead(200, { 'Content-Type': TYPES[extname(path)] || 'application/octet-stream', 'Content-Security-Policy': "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'; require-trusted-types-for 'script'" }).end(body); }
  catch { res.writeHead(404).end(); }
});
await new Promise((r) => server.listen(0, '127.0.0.1', r));
const ORIGIN = `http://127.0.0.1:${server.address().port}`;
if (process.env.VIEWER_PREVIEW) { console.log(`Synthetic viewer: ${ORIGIN}`); await new Promise(() => {}); }
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
await cmd('Page.addScriptToEvaluateOnNewDocument', {source: `(${operatorFixture})();`});
let failed=0;
const check=(name,ok,detail='')=>{console.log(`${ok?'PASS':'FAIL'} ${name}${ok?'':` ${detail}`}`);if(!ok)failed++};
const until=async(expr)=>{for(let n=0;n<70;n++){if(await ev(expr))return;await sleep(50)}throw new Error(`condition failed: ${expr}`)};
const click=async(name,selector='.op-workspace')=>{await ev(`(()=>{const b=[...document.querySelectorAll(${JSON.stringify(selector+' button')})].find(b=>b.textContent===${JSON.stringify(name)});if(!b)throw new Error('missing button '+${JSON.stringify(name)});b.click();})()`);await sleep(100)};
const count=()=>ev('window.__op.writes.length');
const open=async()=>{await ev("document.querySelector('#operator-tools').click()");await until("!!document.querySelector('.op-workspace select[aria-label=Thread]')");await ev("{const sel=document.querySelector('.op-workspace select[aria-label=Thread]');sel.value='th';sel.dispatchEvent(new Event('change'));}");await until("!!document.querySelector('.op-workspace [aria-label=\"Task actions\"]')")};
const confirm=async(name)=>{await until("!!document.querySelector('.op-confirm[open]')");await ev(`(()=>{const b=[...document.querySelectorAll('.op-confirm button')].find(b=>b.textContent===${JSON.stringify(name)});if(!b)throw new Error('missing named confirm');b.click();b.click();})()`);await sleep(150)};
const cancel=async()=>click('Cancel','.op-confirm');
async function matrix(action,named=action,setup=''){
 if(setup)await ev(setup);
 const n=await count();
 await ev("for(const input of document.querySelectorAll('.op-workspace input,.op-workspace textarea')){input.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true}));}");check(`${action}: Enter in text fields sends nothing`,await count()===n);
 await click(action);await until("!!document.querySelector('.op-confirm[open]')");
 check(`${action}: no write on open`,await count()===n);
 const text=await ev("document.querySelector('.op-confirm').textContent");check(`${action}: read state and acting identity`,['alice','ops',...(action==='Create task'?['Needs my approval']:['Revision:','Claim holder:','Assignee:','State:'])].every(x=>text.includes(x)),text);
 check(`${action}: Cancel focused`,await ev("document.activeElement.textContent==='Cancel'"));await cancel();check(`${action}: Cancel sends nothing`,await count()===n);
 await click(action);await until("!!document.querySelector('.op-confirm[open]')");
 await cmd('Input.dispatchKeyEvent',{type:'keyDown',key:'Enter',code:'Enter',windowsVirtualKeyCode:13});await cmd('Input.dispatchKeyEvent',{type:'keyUp',key:'Enter',code:'Enter',windowsVirtualKeyCode:13});
 check(`${action}: Enter on Cancel sends nothing`,await count()===n);
 if(await ev("!!document.querySelector('.op-confirm[open]')"))await cancel();
 await click(action);await confirm(named);check(`${action}: explicit confirmation writes`,await count()>n);
 await until("!document.querySelector('.op-pending')?.textContent.includes('Sending')");
}
// Each named regression owns a fresh in-page fixture and its write capture.
async function freshIdentityFixture(setup = '') {
 const { result: { identifier } } = await cmd('Page.addScriptToEvaluateOnNewDocument', {source: `if(location.origin===${JSON.stringify(ORIGIN)}){sessionStorage.clear();${setup}}`});
 try {
  await cmd('Page.navigate', {url:'about:blank'});
  await cmd('Page.navigate', {url:ORIGIN+'/#/boards'});
  await until("!!window.__op && !document.querySelector('#operator-option').hidden && ['login','boards'].includes(document.body.dataset.view)");
 } finally { await cmd('Page.removeScriptToEvaluateOnNewDocument', {identifier}); }
}
async function identityNavigationCase() {
 let assertions = 0;
 const assert = (name, ok, detail = '') => { assertions++; check(`OP17-003: ${name}`, ok, detail); };
 const header = async (name, label) => {
  assert(`${name} shows @alice`, await ev("document.querySelector('#me').textContent==='@alice'"));
  assert(`${name} shows ${label}`, await ev(`!document.querySelector('#forget').hidden && document.querySelector('#forget').textContent===${JSON.stringify(label)} && !document.querySelector('#nav').hidden`));
 };
 const cleared = async (name) => {
  await until("!document.querySelector('#login').hidden");
  assert(`${name} clears identity and logout control`, await ev("document.querySelector('#me').textContent==='' && document.querySelector('#forget').hidden && document.querySelector('#operator-session').hidden && !document.body.dataset.operator && !sessionStorage.length"));
 };
 const login = async (operator) => {
  await ev(`document.querySelector('#operator-login').checked=${operator};document.querySelector('#token').value='synthetic-identity-token';document.querySelector('#login-form').requestSubmit()`);
  await until("document.body.dataset.view==='boards'");
 };
 await freshIdentityFixture("window.__op.scope=['read'];");
 await login(false);
 await header('fresh read-only login', 'Forget token');
 assert('read-only login persists only its tab token', await ev("sessionStorage.getItem('yak-viewer-token')==='synthetic-identity-token' && window.__op.writes.length===0"));
 await ev("document.querySelector('#forget').click()");
 await cleared('read-only forget');
 assert('read-only forget sends no writes', await count()===0);
 await freshIdentityFixture("window.__op.scope=['read'];sessionStorage.setItem('yak-viewer-token','synthetic-restored-token');");
 await until("document.body.dataset.view==='boards'");
 await header('restored read-only token', 'Forget token');
 assert('restoration retains read-only tab token without writes', await ev("sessionStorage.getItem('yak-viewer-token')==='synthetic-restored-token' && window.__op.writes.length===0"));
 for (const selector of ['#forget', '#operator-logout']) {
  await freshIdentityFixture();
  await login(true);
  await header(`operator login via ${selector}`, 'Logout (revokes token everywhere)');
  assert(`operator login via ${selector} shows session identity`, await ev("!document.querySelector('#operator-session').hidden && document.querySelector('#operator-identity').textContent.includes('@alice (human)') && !sessionStorage.length"));
  await ev(`document.querySelector(${JSON.stringify(selector)}).click()`);
  await cleared(`logout via ${selector}`);
  assert(`logout via ${selector} revokes only its credential`, await ev("window.__op.revoked && window.__op.writes.length===1 && window.__op.writes[0].method==='DELETE' && window.__op.writes[0].path==='/v1/credentials/cred_test'"));
 }
 for (const [name, setup, operator] of [['rejected read-only credential', 'window.__op.expired=true', false], ['refused operator credential', "window.__op.kind='agent'", true]]) {
  await freshIdentityFixture();
  await ev(`${setup};document.querySelector('#operator-login').checked=${operator};document.querySelector('#token').value='synthetic-invalid-token';document.querySelector('#login-form').requestSubmit()`);
  await until("!document.querySelector('#login-error').hidden && !document.querySelector('#connect').disabled");
  await cleared(name);
  assert(`${name} sends no writes`, await count()===0);
 }
 assert('browser has no exceptions', errors.length===0, errors.join(' | '));
 console.log(`OP17-003 executed ${assertions} assertions across fresh login, restoration, two logout entries and two rejected credentials`);
}
async function responseBodyFailureCase() {
 let assertions = 0;
 const assert = (name, ok) => { assertions++; check(`OP17-029: ${name}`, ok); };
 for (const fault of ['stream200', 'broken200', 'truncated201', 'empty204', 'broken401', 'broken412']) {
  await freshIdentityFixture("window.__op.task.state='ready';");
  try {
   await ev("document.querySelector('#operator-login').checked=true;document.querySelector('#token').value='synthetic-body-token';document.querySelector('#login-form').requestSubmit()");
   await until("document.body.dataset.operator==='true'"); await open();
   await ev(`window.__op.responseFailure=${JSON.stringify(fault)}`);
   await click('Work on this now (while this tab is open)'); await confirm('Claim');
   if (fault === 'broken401') {
    await until("!document.body.dataset.operator");
    assert('malformed 401 ends without release/revoke/retry', await count() === 1);
    assert('malformed 401 clears session UI', await ev("!document.querySelector('.op-workspace') && !sessionStorage.length"));
   } else if (fault === 'broken412') {
    await until("document.querySelector('.op-receipts').textContent.includes('No retry was sent')");
    assert('malformed 412 is a known rejection', await ev("!document.querySelector('.op-receipts').textContent.includes('Retry same intent') && !document.querySelector('.op-receipts').textContent.includes('Saved.') && window.__op.writes.length===1"));
   } else if (fault === 'empty204') {
    await until("document.querySelector('.op-receipts').textContent.includes('Saved.')");
    assert('204 succeeds with explicit proof unavailable notice', await ev("document.querySelector('#operator-identity').textContent.includes('proof unavailable') && window.__op.writes.length===1"));
   } else {
    assert(`${fault} keeps receipt uncertain without completion or proof`, await ev("document.querySelector('.op-receipts').textContent.includes('Retry same intent') && !document.querySelector('.op-receipts').textContent.includes('Saved.') && !document.querySelector('#operator-identity').textContent.includes('proof unavailable') && !document.querySelector('.op-lease') && window.__op.writes.length===1"));
    assert(`${fault} prevents a new intent`, await ev("[...document.querySelectorAll('[data-op-write]')].every(b=>b.disabled)"));
    // Continue other variants even against the unfixed transport during RED.
    if (await ev("document.querySelector('.op-receipts').textContent.includes('Retry same intent')")) {
     await click('Retry same intent');
     await until("document.querySelector('.op-receipts').textContent.includes('Saved.')");
     assert(`${fault} replay uses the same request and key`, await ev("window.__op.writes.length===2 && JSON.stringify(window.__op.writes[0])===JSON.stringify(window.__op.writes[1])"));
     assert(`${fault} replay obtains a usable proof`, await ev("!!document.querySelector('.op-lease') && !document.querySelector('#operator-identity').textContent.includes('proof unavailable')"));
     await click('Release'); await confirm('Release');
     assert(`${fault} release carries recovered proof`, await ev("window.__op.writes.at(-1).headers['Yak-Claim-Proof']==='fake-secret-proof'"));
    }
   }
  } finally {
   if (await ev("document.body.dataset.operator==='true'")) {
    await ev("document.querySelector('#operator-logout').click()"); await until("!document.body.dataset.operator");
   }
  }
 }
 assert('browser has no exceptions', errors.length === 0);
 console.log(`OP17-029 executed ${assertions} assertions across six fresh body-failure fixtures`);
}
async function renewalClaimViewCase() {
 let assertions = 0;
 const assert = (name, ok) => { assertions++; check(`OP17-042: ${name}`, ok); };
 const date = 'Fri, 20 Nov 2026 00:00:00 GMT';
 const variants = [
  ...op17042ClaimCorpus.map(view => [view.status, view, 7000, view.status === 'active' ? 27 : null]),
  ['wrong id', { ...op17042ClaimCorpus[0], id: 'wrong' }, 7000, null],
  ['malformed', {}, 7000, null],
  ['old replay expiry', { ...op17042ClaimCorpus[0], expires_at: '2026-11-20T00:00:10Z' }, 7000, 7],
  ['delayed skewed clock', op17042ClaimCorpus[0], 20000, 14],
  ['elapsed started lease', op17042ClaimCorpus[0], 35001, null],
  ['elapsed expiry', { ...op17042ClaimCorpus[0], expires_at: '2026-11-20T00:00:01Z' }, 7000, null],
  ['missing Date', op17042ClaimCorpus[0], 7000, 23],
  ['replacement', { ...op17042ClaimCorpus[0], status: 'revoked' }, 7000, 'replacement'],
 ];
 for (const [name, view, receive, remaining] of variants) {
  await freshIdentityFixture('window.__op.task.state="ready";window.__op.enableRenewTest();');
  try {
   await ev("document.querySelector('#operator-login').checked=true;document.querySelector('#token').value='synthetic-renew-token';document.querySelector('#login-form').requestSubmit()");
   await until("document.body.dataset.operator==='true'"); await open();
   await click('Work on this now (while this tab is open)'); await confirm('Claim');
   // The fixture grants 30 seconds for these cases so the original bound is observable.
   await ev(`window.__op.renewReply=${JSON.stringify(view)};window.__op.renewDate=${JSON.stringify(name === 'missing Date' ? 'unknown' : date)};window.__op.mono=5000;window.__op.deferRenew=true;window.__op.pendingRenew=window.__op.fireRenew();void 0`);
   await until("typeof window.__op.resolveRenew==='function'");
   if (remaining === 'replacement') {
    await click('Release'); await confirm('Release');
    await ev("window.__op.claimId='replacement'");
    await click('Work on this now (while this tab is open)'); await confirm('Claim');
   }
   // Wall time is deliberately far behind the server; only monotonic bounds count.
   await ev(`window.__op.mono=${receive};Date.now=()=>-9000000000000;window.__op.resolveRenew();`);
   await ev('window.__op.pendingRenew'); await sleep(40);
   assert(`${name} sends exactly one renewal with proof and receipt`, await ev("window.__op.writes.filter(w=>w.path.endsWith('/renew')).length===1 && window.__op.writes.find(w=>w.path.endsWith('/renew')).headers['Yak-Claim-Proof']==='fake-secret-proof' && !!window.__op.writes.find(w=>w.path.endsWith('/renew')).headers['Idempotency-Key']"));
   if (remaining === null) {
    assert(`${name} drops held claim and tells person to refresh`, await ev("document.querySelector('#operator-identity').textContent.includes('Your claim ended. Refresh') && document.querySelector('.op-lease').textContent.includes('Your claim ended') && window.__op.renewTimers.length===0"));
    await ev("document.querySelector('#operator-logout').click()"); await until("!document.body.dataset.operator");
    assert(`${name} discarded proof is never released`, await ev("!window.__op.writes.some(w=>w.path.endsWith('/release'))"));
   } else {
    // changed() runs on the next tick for a retained claim.
    await sleep(1050);
    assert(`${name} retains only conservative remaining time`, await ev(`document.querySelector('.op-lease').textContent.includes(${JSON.stringify(remaining === 'replacement' ? '28 seconds remaining' : remaining + ' seconds remaining')}) && window.__op.renewTimers.length===1`));
    if (name === 'missing Date') assert('unknown Date requests refresh', await ev("document.querySelector('#operator-identity').textContent.includes('Refresh')"));
   }
  } finally { await ev("document.querySelector('#operator-logout')?.click()"); }
 }
 assert('no browser exceptions', errors.length === 0);
 console.log(`OP17-042 executed ${assertions} assertions across ${variants.length} fresh renewal fixtures`);
}
async function boardSwitchCase() {
 let assertions = 0;
 const assert = (name, ok, detail='') => { assertions++; check(`OP17-005: ${name}`, ok, detail); };
 const selectBoard = async id => ev(`{const s=document.querySelector('.op-workspace select[aria-label=Board]');s.value=${JSON.stringify(id)};s.dispatchEvent(new Event('change'));}`);
 const selectThread = async id => {
  await until("!!document.querySelector('.op-workspace select[aria-label=Thread]')");
  await ev(`{const s=document.querySelector('.op-workspace select[aria-label=Thread]');s.value=${JSON.stringify(id)};s.dispatchEvent(new Event('change'));}`);
  await until("!!document.querySelector('[aria-label=\"Task actions\"]')");
 };
 for (const fail of [false,true]) for (const reply of [false,true]) {
  const variant = `${fail?'failed':'deferred'} discovery, ${reply?'reply':'new thread'}`;
  await freshIdentityFixture('window.__op.enableBoardSwitch();');
  await ev("document.querySelector('#operator-login').checked=true;document.querySelector('#token').value='synthetic-switch';document.querySelector('#login-form').requestSubmit()");
  await until("document.body.dataset.operator==='true'");
  await ev("document.querySelector('#operator-tools').click()");
  await until("!!document.querySelector('textarea[aria-label^=Message]')");
  if (reply) await selectThread('th');
  await ev(`{
   const inputs=[...document.querySelectorAll('.op-workspace input,.op-workspace textarea')];
   for(const n of inputs) n.value=n.getAttribute('aria-label').startsWith('Label key')?'tag':n.getAttribute('aria-label').startsWith('Reaction')?'like':'unsent A draft';
   window.__oldButtons=[...document.querySelectorAll('.op-workspace [data-op-write]')];
   window.__oldChannel=document.querySelector('.op-workspace select[aria-label=Channel]');
   window.__oldThread=document.querySelector('.op-workspace select[aria-label=Thread]');
   window.__op.deferBoard=${!fail};window.__op.failBoard=${fail};
  }`);
  await click('Create task');
  await until("!!document.querySelector('.op-confirm[open]')");
  await ev("window.__oldConfirm=[...document.querySelectorAll('.op-confirm button')].find(b=>b.textContent==='Create task')");
  await selectBoard('brd_other'); await until('window.__op.discoveryStarted'); await sleep(100);
  assert(`${variant}: transition clears content, channel and confirmation`,await ev("!document.querySelector('.op-confirm') && !document.querySelector('.op-workspace select[aria-label=Channel]') && !document.querySelector('.op-workspace [data-op-write]')"));
  for (const [index, name] of (await ev('window.__oldButtons.map(b=>b.textContent)')).entries()) {
   const before=await count();
   await ev(`window.__oldButtons[${index}].dispatchEvent(new Event('click'))`);await sleep(50);
   assert(`${variant}: retained ${name} refuses writes`,await count()===before);
  }
  const before=await count(), reads=await ev('window.__op.boardReads.length');
  await ev("window.__oldConfirm.dispatchEvent(new Event('click'));window.__oldChannel.dispatchEvent(new Event('change'));window.__oldThread.dispatchEvent(new Event('change'))");await sleep(150);
  assert(`${variant}: retained confirmation and selectors refuse`,await count()===before && await ev('window.__op.boardReads.length')===reads);
  assert(`${variant}: no stale dialog reopens`,await ev("!document.querySelector('.op-confirm')"));
  if (fail) { await ev('window.__op.failBoard=false'); await selectBoard('brd_other'); }
  else await ev('window.__op.deferBoard=false;window.__op.resolveBoard()');
  await ev("document.querySelectorAll('.op-confirm').forEach(d=>d.remove())");
  await selectBoard('brd_other');
  await until("!!document.querySelector('textarea[aria-label^=Message][aria-label*=brd_other]')");
  assert(`${variant}: board B draft is separate`,await ev("document.querySelector('textarea[aria-label^=Message]').value===''") );
  await selectThread('th_other');
  await ev("document.querySelector('textarea[aria-label^=Message]').value='B reply'");
  await click('Send reply');await until(`window.__op.writes.length>${before}`);
  assert(`${variant}: new reply targets only B thread`,await ev("window.__op.writes.at(-1).path==='/v1/boards/brd_other/threads/th_other/messages'"));
  if (!fail && reply) {
   for (const [name, suffix, named] of [
    ['Save edit','/messages/reply_other'], ['Set label','/messages/root_other/labels'],
    ['React','/messages/root_other/reactions/like'], ['Remove approval requirement','/tasks/t_other','Remove approval requirement'],
    ['Create task','/tasks','Create task']
   ]) {
    await until("!!document.querySelector('.op-workspace [aria-label=\"Task actions\"]') && !document.querySelector('.op-pending')?.textContent.includes('Sending')");
    await ev("for(const n of document.querySelectorAll('.op-workspace input,.op-workspace textarea'))n.value=n.getAttribute('aria-label').startsWith('Label key')?'tag':n.getAttribute('aria-label').startsWith('Reaction')?'like':'B values'");
    const n = await count(); await click(name); if(named) await confirm(named);
    await until(`window.__op.writes.length>${n}`);
    assert(`new B ${name} uses B destination`,await ev(`window.__op.writes.at(-1).path===${JSON.stringify('/v1/boards/brd_other'+suffix)}`));
    if(name==='Create task') assert('new B task uses B channel',await ev("window.__op.writes.at(-1).body.thread.channel==='other'"),JSON.stringify(await ev('window.__op.writes.at(-1)')));
   }
   await ev("{const s=document.querySelector('.op-workspace select[aria-label=Thread]');s.value='';s.dispatchEvent(new Event('change'));}");
   await until("!!document.querySelector('.op-workspace input[aria-label^=Title]')");
   await ev("document.querySelector('textarea[aria-label^=Message]').value='B new';document.querySelector('.op-workspace input[aria-label^=Title]').value='B title'");
   const n=await count();await click('Start thread');await until(`window.__op.writes.length>${n}`);
   assert('new B thread uses B board and channel',await ev("window.__op.writes.at(-1).path==='/v1/boards/brd_other/threads' && window.__op.writes.at(-1).body.channel==='other'"));
  }
  await selectBoard('brd_test');await until("!!document.querySelector('textarea[aria-label^=Message][aria-label*=brd_test]')");
  if(reply) await selectThread('th');
  assert(`${variant}: returning to A retains unsent draft`,await ev("document.querySelector('textarea[aria-label^=Message]').value==='unsent A draft'"));
 }
 // OP17-005: receipts retain their original intent across invalidated views.
 for (const cosmetic of [false,true]) for (const transition of ['board','refresh','close']) {
  const variant = `${cosmetic?'cosmetic':'ordinary'} pending recovery after ${transition}`;
  await freshIdentityFixture('window.__op.enableBoardSwitch();');
  await ev("document.querySelector('#operator-login').checked=true;document.querySelector('#token').value='synthetic-recovery';document.querySelector('#login-form').requestSubmit()");
  await until("document.body.dataset.operator==='true'"); await open();
  await ev(`window.__op.pendingFailurePath=${JSON.stringify(cosmetic?'/v1/boards/brd_test/messages/root/labels':'/v1/boards/brd_test/threads/th/messages')};document.querySelector('textarea[aria-label^=Message]').value='original A body';window.__staleWrites=[...document.querySelectorAll('.op-workspace [data-op-write]')];`);
  if (cosmetic) { await click('Approve and unblock'); await confirm('Approve and unblock'); }
  else await click('Send reply');
  const retry = cosmetic?'Retry label only':'Retry same intent';
  await until(`document.querySelector('.op-receipts').textContent.includes(${JSON.stringify(retry)})`);
  await ev(`window.__retry=[...document.querySelectorAll('.op-pending button')].find(b=>b.textContent===${JSON.stringify(retry)});window.__originalWrite=JSON.stringify(window.__op.writes.at(-1));`);
  assert(`${variant}: receipt identifies original destination`,await ev("document.querySelector('.op-receipts').textContent.includes(JSON.parse(window.__originalWrite).path)"));
  const before = await count();
  if (transition==='board') {
   await selectBoard('brd_other'); await until("!!document.querySelector('textarea[aria-label*=brd_other]')");
  } else if (transition==='refresh') {
   await click('Refresh permissions and state'); await until("!!document.querySelector('[aria-label=\"Task actions\"]')");
  } else {
   await click('Close'); await ev("document.querySelector('#operator-tools').click()"); await until("!!document.querySelector('textarea[aria-label^=Message]')");
  }
  await ev("document.querySelector('textarea[aria-label^=Message]').value='new view draft';for(const b of window.__staleWrites)b.dispatchEvent(new Event('click'));");
  assert(`${variant}: navigation and stale actions send nothing`,await count()===before && await ev("!document.querySelector('.op-confirm')"));
  assert(`${variant}: fresh writes stay locked until explicit recovery`,await ev("[...document.querySelectorAll('.op-workspace [data-op-write]')].every(b=>b.disabled)"));
  await ev("window.__retry.click()"); await sleep(250);
  const recovered = await count()===before+1;
  assert(`${variant}: explicit retry sends original destination body key and run`,recovered && await ev("JSON.stringify(window.__op.writes.at(-1))===window.__originalWrite"));
  assert(`${variant}: receipt resolves and fresh writes unlock`,await ev("document.querySelector('.op-receipts').textContent.includes('Saved.') && [...document.querySelectorAll('.op-workspace [data-op-write]')].some(b=>!b.disabled)"));
  assert(`${variant}: recovery does not clear replacement draft`,await ev("document.querySelector('textarea[aria-label^=Message]').value==='new view draft'"));
  assert(`${variant}: recovery never repeats completed task step`,await ev(`window.__op.writes.filter(w=>w.path.endsWith('/unblock')).length===${cosmetic?1:0}`));
  await ev("window.__retry.dispatchEvent(new Event('click'));for(const b of window.__staleWrites)b.dispatchEvent(new Event('click'))"); await sleep(100);
  assert(`${variant}: settled receipt and stale actions remain inert`,await count()===(recovered?before+1:before));
  if (recovered) {
   const n=await count(); const reply=await ev("[...document.querySelectorAll('.op-workspace button')].some(b=>b.textContent==='Send reply')");
   if (!reply) await ev("document.querySelector('input[aria-label^=Title]').value='new view title'");
   await ev(`window.__op.pendingFailurePath=${JSON.stringify(transition==='board'?'/v1/boards/brd_other/threads':reply?'/v1/boards/brd_test/threads/th/messages':'/v1/boards/brd_test/threads')}`);
   await click(reply?'Send reply':'Start thread');
   assert(`${variant}: new write uses current destination`,await count()===n+1 && await ev(`window.__op.writes.at(-1).path.startsWith(${JSON.stringify(transition==='board'?'/v1/boards/brd_other/':'/v1/boards/brd_test/')})`));
   await until("!![...document.querySelectorAll('.op-pending button')].find(b=>b.textContent==='Retry same intent')");
   await ev("window.__retry=[...document.querySelectorAll('.op-pending button')].find(b=>b.textContent==='Retry same intent')");
  }
  // Retained receipt callbacks must also refuse after the session is replaced.
  await ev("document.querySelector('#operator-logout').click()"); await until("!document.querySelector('#login').hidden");
  const ended=await count(); await ev("window.__retry.dispatchEvent(new Event('click'))"); await sleep(100);
  assert(`${variant}: ended session cannot replay receipt`,await count()===ended);
  await ev("document.querySelector('#operator-login').checked=true;document.querySelector('#token').value='synthetic-replacement';window.__op.revoked=false;document.querySelector('#login-form').requestSubmit()");
  await until("document.body.dataset.operator==='true'");
  const replaced=await count(); await ev("window.__retry.dispatchEvent(new Event('click'))"); await sleep(100);
  assert(`${variant}: replacement session cannot replay receipt`,await count()===replaced);
 }
 assert('all retained handlers avoid browser exceptions',errors.length===0,errors.join(' | '));
 assert('required variants executed nonzero assertions',assertions>=40);
 console.log(`OP17-005 executed ${assertions} assertions across four fresh board transitions and six fresh pending-recovery fixtures`);
}
async function refreshDiscoveryCase() {
 let assertions = 0;
 const assert = (name, ok) => { assertions++; check(`OP17-007: ${name}`, ok); };
 const selectBoard = async id => ev(`{const s=document.querySelector('.op-workspace select[aria-label=Board]');s.value=${JSON.stringify(id)};s.dispatchEvent(new Event('change'));}`);
 for (const variant of ['initial', 'switch', 'failed', 'empty']) {
  await freshIdentityFixture('window.__op.enableBoardSwitch();');
  try {
   await ev("document.querySelector('#operator-login').checked=true;document.querySelector('#token').value='synthetic-discovery-refresh';document.querySelector('#login-form').requestSubmit()");
   await until("document.body.dataset.operator==='true'");
   await ev(`window.__op.deferBoard=true;window.__op.failBoard=${variant === 'failed'};window.__op.emptyBoard=${variant === 'empty'};`);
   if (variant === 'initial') {
    await ev("window.dispatchEvent(new CustomEvent('yak-operator-open',{detail:{board:'brd_other'}}))");
   } else {
    await ev("document.querySelector('#operator-tools').click()");
    await until("!!document.querySelector('.op-workspace select[aria-label=Channel]')");
    assert(`${variant}: A starts with write controls`, await ev("!!document.querySelector('.op-workspace [data-op-write]')"));
    await selectBoard('brd_other');
   }
   await until('window.__op.discoveryStarted && typeof window.__op.resolveBoard==="function"');
   await ev("window.__refresh=[...document.querySelectorAll('.op-workspace button')].find(b=>b.textContent==='Refresh permissions and state')");
   assert(`${variant}: Refresh disabled during discovery`, await ev('window.__refresh.disabled'));
   const reads = await ev('window.__op.boardReads.length');
   // Native click and direct listener dispatch both must leave discovery intact.
   await ev("window.__refresh.click();window.__refresh.dispatchEvent(new Event('click'))"); await sleep(120);
   assert(`${variant}: Refresh sends no read while channel is unresolved`, await ev('window.__op.boardReads.length') === reads);
   assert(`${variant}: no old channel or writes during discovery`, await ev("!document.querySelector('.op-workspace select[aria-label=Channel]') && !document.querySelector('.op-workspace [data-op-write]')"));
   await ev('window.__op.deferBoard=false;window.__op.resolveBoard()');
   if (variant === 'failed' || variant === 'empty') {
    await until(variant === 'failed' ? "document.querySelector('.op-workspace [role=status]').textContent.includes('synthetic_discovery_failure')" : "document.querySelector('.op-workspace [role=status]').textContent.includes('No readable channel')");
    assert(`${variant}: Refresh stays disabled and old writes stay absent`, await ev("window.__refresh.disabled && !document.querySelector('.op-workspace [data-op-write]')"));
    const settled = await ev('window.__op.boardReads.length');
    await ev("window.__refresh.dispatchEvent(new Event('click'))"); await sleep(80);
    assert(`${variant}: handler remains inert after discovery`, await ev('window.__op.boardReads.length') === settled);
    assert(`${variant}: board selector remains usable`, await ev("!document.querySelector('.op-workspace select[aria-label=Board]').disabled"));
    await selectBoard('brd_test');
    await until("document.querySelector('.op-workspace select[aria-label=Channel]')?.value==='ops'");
    assert(`${variant}: selecting another board restores Refresh`, await ev('!window.__refresh.disabled'));
   } else {
    await until("document.querySelector('.op-workspace select[aria-label=Channel]')?.value==='other'");
    assert(`${variant}: discovery survives Refresh and restores button`, await ev("!window.__refresh.disabled && document.querySelector('.op-workspace select[aria-label=Board]').value==='brd_other'"));
    const before = await ev("window.__op.boardReads.filter(p=>p.startsWith('/v1/boards/brd_other/permissions?')).length");
    await click('Refresh permissions and state');
    await until("document.querySelector('.op-workspace select[aria-label=Channel]')?.value==='other'");
    assert(`${variant}: ready Refresh rereads B permissions`, await ev("window.__op.boardReads.filter(p=>p.startsWith('/v1/boards/brd_other/permissions?')).length") === before + 1);
    assert(`${variant}: refreshed B channel and composer are retained`, await ev("document.querySelector('.op-workspace select[aria-label=Channel]').value==='other' && !!document.querySelector('textarea[aria-label*=brd_other]')"));
   }
   assert(`${variant}: permissions and thread lists always include a resolved channel`, await ev("window.__op.boardReads.filter(p=>/\\/(permissions|threads)\\?/.test(p)).every(p=>['ops','other'].includes(new URL(p,location.origin).searchParams.get('channel')))"));
   assert(`${variant}: regression sent no writes`, await count() === 0);
  } finally {
   await ev("if(document.body.dataset.operator==='true')document.querySelector('#operator-logout').click()");
   await until("!document.body.dataset.operator");
  }
 }
 assert('no browser exceptions', errors.length === 0);
 assert('all required variants executed assertions', assertions >= 35);
 console.log(`OP17-007 executed ${assertions} assertions across four fresh discovery fixtures`);
}
async function openRaceCase() {
 let assertions = 0;
 const assert = (name, ok) => { assertions++; check(`OP17-006: ${name}`, ok); };
 for (const replacement of [true, false]) for (const reject of [false, true]) {
  const variant = `${replacement ? 'replacement B' : 'closed without replacement'}, old A ${reject ? 'rejects' : 'resolves'}`;
  await freshIdentityFixture();
  try {
   await ev("document.querySelector('#operator-login').checked=true;document.querySelector('#token').value='synthetic-open-race';document.querySelector('#login-form').requestSubmit()");
   await until("document.body.dataset.operator==='true'");
   await ev(`window.__op.enableOpenRace();window.dispatchEvent(new CustomEvent('yak-operator-open',{detail:{board:'brd_test',ch:'ops',t:'th'}}));`);
   await until('window.__op.openReads.length===1');
   assert(`${variant}: A really waits for boards`, await ev("!document.querySelector('.op-workspace select')"));
   await ev("window.__op.oldPanel=document.querySelector('.op-workspace')");
   await click('Close');
   if (replacement) {
    await ev(`window.dispatchEvent(new CustomEvent('yak-operator-open',{detail:{board:'brd_other',ch:'other',t:'th_other'}}));`);
    await until('window.__op.openReads.length===2');
    await ev('window.__op.openReads[1].resolve()');
    await until("document.querySelector('.op-workspace select[aria-label=Thread]')?.value==='th_other' && !!document.querySelector('[aria-label=\"Task actions\"]')");
   }
   await ev(`window.__op.beforeReads=window.__op.boardReads.length;
    window.__op.observedPanel=document.querySelector('.op-workspace') || window.__op.oldPanel;
    window.__op.beforeDOM=window.__op.observedPanel.outerHTML;
    window.__op.domChanges=0;
    window.__op.observer=new MutationObserver(records=>window.__op.domChanges+=records.length);
    window.__op.observer.observe(window.__op.observedPanel,{subtree:true,childList:true,characterData:true});
    window.__op.openReads[0].${reject ? "reject(new Error('OP17-006 obsolete read failed'))" : 'resolve()'};`);
   await sleep(150);
   assert(`${variant}: no A loadBoard discovery`, await ev('window.__op.boardReads.length===window.__op.beforeReads'));
   assert(`${variant}: no stale DOM or status updates`, await ev("window.__op.observedPanel.outerHTML===window.__op.beforeDOM && window.__op.domChanges===0"));
   if (replacement) {
    assert(`${variant}: exactly one board selector`, await ev("document.querySelectorAll('.op-workspace select[aria-label=Board]').length===1"));
    assert(`${variant}: B board/channel/thread stays selected`, await ev("document.querySelector('.op-workspace select[aria-label=Board]').value==='brd_other' && document.querySelector('.op-workspace select[aria-label=Channel]').value==='other' && document.querySelector('.op-workspace select[aria-label=Thread]').value==='th_other'"));
   } else assert(`${variant}: workspace remains closed`, await ev("!document.querySelector('.op-workspace')"));
   assert(`${variant}: no operator writes`, await count()===0);
  } finally {
   await ev("window.__op.observer?.disconnect();for(const read of window.__op.openReads || [])read.resolve();document.querySelector('.op-workspace button')?.click()");
  }
 }
 assert('no browser exceptions', errors.length===0);
 if (!assertions) throw new Error('OP17-006 executed no assertions');
 console.log(`OP17-006 executed ${assertions} assertions across four fresh initial-open fixtures`);
}
async function delayedDraftCase() {
 let assertions = 0, fixtures = 0;
 const assert = (name, ok) => { assertions++; check(`OP17-018: ${name}`, ok); };
 const setField = async (selector, value) => ev(`{const n=document.querySelector(${JSON.stringify(selector)});n.value=${JSON.stringify(value)};n.dispatchEvent(new Event('input',{bubbles:true}));}`);
 const text = 'textarea[aria-label^=Message]';
 for (const kind of ['reply', 'thread', 'task']) {
  const title = kind === 'task' ? 'input[aria-label^="Task title"]' : 'input[aria-label^=Title]';
  const variants = ['unchanged','text','restore','network','conflict','reopen',...(kind === 'reply' ? [] : ['title','both','title-restore', ...(kind === 'task' ? ['cancel'] : [])])];
  for (const variant of variants) {
   fixtures++;
   await freshIdentityFixture('window.__op.enableBoardSwitch();window.__op.enableDraftDelay();');
   try {
    await ev("document.querySelector('#operator-login').checked=true;document.querySelector('#token').value='synthetic-draft-token';document.querySelector('#login-form').requestSubmit()");
    await until("document.body.dataset.operator==='true'");
    if (kind === 'reply') await open();
    else {
     await ev("document.querySelector('#operator-tools').click()");
     await until("!!document.querySelector('.op-workspace select[aria-label=Thread]')");
    }
    await setField(text, 'submitted text');
    if (kind !== 'reply') await setField(title, 'submitted title');
    await ev(`window.__op.delayDraft=true;window.__op.draftOutcome=${JSON.stringify(variant)}`);
    const action = {reply:'Send reply',thread:'Start thread',task:'Create task'}[kind];
    const before = await count();
    await click(action);
    if (kind === 'task') {
     if (variant === 'cancel') {
      await cancel();
      assert(`${kind}/${variant}: no write and both drafts retained`, await count()===before && await ev(`document.querySelector(${JSON.stringify(text)}).value==='submitted text' && document.querySelector(${JSON.stringify(title)}).value==='submitted title'`));
      continue;
     }
     await confirm(action);
    }
    await until('typeof window.__op.resolveDraft==="function"');
    assert(`${kind}/${variant}: one captured original request`, await ev(`window.__op.writes.length===${before+1} && window.__op.writes.at(-1).body${kind === 'task' ? '.thread' : ''}.text==='submitted text' ${kind === 'reply' ? '' : `&& window.__op.writes.at(-1).body${kind === 'task' ? '.thread' : ''}.title==='submitted title'`}`));
    let expectedText = 'submitted text', expectedTitle = 'submitted title';
    if (['text','both','reopen'].includes(variant)) { await setField(text, 'later text'); expectedText='later text'; }
    if (['title','both'].includes(variant)) { await setField(title, 'later title'); expectedTitle='later title'; }
    if (variant === 'restore') { await setField(text, 'temporary'); await setField(text, 'submitted text'); }
    if (variant === 'title-restore') { await setField(title, 'temporary'); await setField(title, 'submitted title'); }
    if (variant === 'reopen') {
     await click('Close');
     await ev("window.dispatchEvent(new CustomEvent('yak-operator-open',{detail:{board:'brd_other'}}))");
     await until("!!document.querySelector('textarea[aria-label*=brd_other]')");
     await setField('textarea[aria-label*=brd_other]', 'other board draft');
     await ev('window.__op.resolveDraft()');
     await until("document.querySelector('.op-receipts').textContent.includes('Saved.')");
     assert(`${kind}/${variant}: completion preserves other board`, await ev("document.querySelector('textarea[aria-label*=brd_other]').value==='other board draft'"));
     await click('Close');
     await ev(`window.dispatchEvent(new CustomEvent('yak-operator-open',{detail:{board:'brd_test',ch:'ops',${kind === 'reply' ? "t:'th'" : ''}}}))`);
     await until(`!!document.querySelector(${JSON.stringify(text)})`);
    } else {
     await ev('window.__op.resolveDraft()');
     await until(variant === 'network' ? "document.querySelector('.op-receipts').textContent.includes('Retry same intent')" : variant === 'conflict' ? "document.querySelector('.op-receipts').textContent.includes('No retry was sent')" : "document.querySelector('.op-receipts').textContent.includes('Saved.') && !document.querySelector('.op-pending')?.textContent.includes('Sending')");
     if (!['network','conflict'].includes(variant)) {
      if (!['text','both','restore'].includes(variant)) expectedText='';
      if (!['title','both','title-restore'].includes(variant)) expectedTitle='';
     }
    }
    await until(`!!document.querySelector(${JSON.stringify(text)})`);
    assert(`${kind}/${variant}: text retained or cleared independently`, await ev(`document.querySelector(${JSON.stringify(text)}).value===${JSON.stringify(expectedText)}`));
    if (kind !== 'reply') assert(`${kind}/${variant}: title retained or cleared independently`, await ev(`document.querySelector(${JSON.stringify(title)}).value===${JSON.stringify(expectedTitle)}`));
    assert(`${kind}/${variant}: no implicit resend`, await count()===before+1);
   } finally {
    await ev("window.__op.delayDraft=false;document.querySelector('#operator-logout')?.click()");
    await until("!document.body.dataset.operator");
   }
  }
 }
 assert('no browser exceptions', errors.length===0);
 assert('all variants executed nonzero assertions', fixtures===25 && assertions>=80);
 console.log(`OP17-018 executed ${assertions} assertions across ${fixtures} fresh delayed-draft fixtures`);
}
async function cosmeticReceiptCase() {
 let assertions=0, fixtures=0;
 const assert=(name,ok)=>{assertions++;check(`OP17-019: ${name}`,ok);};
 const replyEnabled="[...document.querySelectorAll('.op-workspace button')].find(b=>b.textContent==='Send reply')?.disabled===false";
 for(const kind of ['approve','changes']) for(const status of [403,404,412,0,502,503,'approved']) {
  if(kind==='changes' && status==='approved') continue;
  fixtures++;
  const name=`${kind}/${status}`, ambiguous=[0,502,503].includes(status);
  await freshIdentityFixture();
  try {
   await ev("document.querySelector('#operator-login').checked=true;document.querySelector('#token').value='synthetic-cosmetic-token';document.querySelector('#login-form').requestSubmit()");
   await until("document.body.dataset.operator==='true'"); await open();
   await ev(`window.__op.labelFault={status:${status==='approved'?412:status},current:${JSON.stringify(status==='approved'?'approved':'changes_requested')}};document.querySelector('textarea[aria-label="Gate note t"]').value='original changes';`);
   const action=kind==='approve'?'Approve and unblock':'Request changes';
   await click(action); await confirm(action);
   if(status==='approved') {
    await until("document.querySelector('.op-receipts').textContent.includes('Saved.')");
    assert(`${name}: approved conflict finishes without retry`,await count()===2 && await ev("!document.querySelector('.op-receipts button')"));
    continue;
   }
   await until("document.querySelector('.op-receipts').textContent.includes('Retry label only')");
   assert(`${name}: explicit partial receipt`,await ev(`document.querySelector('.op-receipts').textContent.includes(${JSON.stringify(kind==='approve'?'Approved; label not updated':'Reply posted; label not updated')})`));
   await ev('window.__receipt=document.querySelector(".op-pending")');
   const original=await ev('window.__op.writes[1]');
   assert(`${name}: original conditional label`,original.body.operations[0].expect.value==='pending' && original.body.operations[0].value===(kind==='approve'?'approved':'changes_requested'));
   assert(`${name}: global lock matches uncertainty`,await ev(replyEnabled)===!ambiguous);
   if(!ambiguous && await ev(replyEnabled)) {
    await ev("document.querySelector('textarea[aria-label^=Message]').value='unrelated message'");
    await click('Send reply');
    await until("window.__op.writes.some(w=>w.body?.text==='unrelated message')");
    assert(`${name}: unrelated message succeeds`,await ev("window.__op.writes.some(w=>w.body?.text==='unrelated message')"));
   }
   for(let n=0;n<3;n++) {
    const before=await count(); await click('Retry label only');
    assert(`${name}: retry ${n} sends once with original body/key`,await count()===before+1 && JSON.stringify(await ev('window.__op.writes.at(-1)'))===JSON.stringify(original));
    assert(`${name}: retry ${n} preserves lock semantics`,await ev(replyEnabled)===!ambiguous);
   }
   await ev('window.__op.deferLabel=true');
   const before=await count();
   await ev("window.__retry=[...document.querySelectorAll('.op-receipts button')].find(b=>b.textContent==='Retry label only');window.__retry.click();window.__retry.click();");
   await until("typeof window.__op.resolveLabel==='function'");
   assert(`${name}: rapid retry captures one write and blocks new writes`,await count()===before+1 && !await ev(replyEnabled));
   // Resolve the same receipt with an uncertain result, then a successful retry.
   await ev('window.__op.labelFault={status:503};window.__op.resolveLabel();window.__op.deferLabel=false');
   await until("document.querySelector('.op-receipts').textContent.includes('Retry label only')");
   assert(`${name}: uncertain retry retains receipt and global lock`,await ev("window.__receipt===document.querySelector('.op-pending') && window.__receipt.textContent.includes('Retry label only')") && !await ev(replyEnabled));
   await ev('window.__op.labelFault=null'); await click('Retry label only');
   await until("!document.querySelector('.op-receipts button')");
   assert(`${name}: resolution permits unrelated writes`,await ev(replyEnabled));
   const writes=await ev('window.__op.writes');
   assert(`${name}: first action never repeated`,writes.filter(w=>!w.path.endsWith('/labels')&&w.body?.text!=='unrelated message').length===1);
   assert(`${name}: every label retry is original request`,writes.filter(w=>w.path.endsWith('/labels')).every(w=>JSON.stringify(w)===JSON.stringify(original)));
  } finally {
   await ev("window.__op.deferLabel=false;window.__op.resolveLabel?.();document.querySelector('#operator-logout')?.click()");
   await until('!document.body.dataset.operator');
  }
 }
 assert('all fresh variants executed',fixtures===13 && assertions>=100);
 assert('no browser exceptions',errors.length===0);
 console.log(`OP17-019 executed ${assertions} assertions across ${fixtures} fresh cosmetic receipt fixtures`);
}
async function labelOpcodeCase() {
 let assertions = 0, fixtures = 0;
 const assert = (name, ok) => { assertions++; check(`OP17-035: ${name}`, ok); };
 const cases = [
  {name:'existing', action:'Remove label', key:'priority', old:'high'},
  {name:'absent', action:'Remove label', key:'priority'},
  {name:'set', action:'Set label', key:'priority', old:'high'},
  ...['gate','yak.test','abby.test'].flatMap(key => ['Remove label','Set label'].map(action => ({name:`guard ${key}/${action}`, action, key, refused:true}))),
 ];
 for (const c of cases) {
  fixtures++;
  await freshIdentityFixture();
  try {
   await ev(`${c.old===undefined?'delete window.__op.root.labels.priority':`window.__op.root.labels.priority=${JSON.stringify(c.old)}`};document.querySelector('#operator-login').checked=true;document.querySelector('#token').value='synthetic-label-token';document.querySelector('#login-form').requestSubmit()`);
   await until("document.body.dataset.operator==='true'"); await open();
   await ev(`document.querySelector('input[aria-label="Label key root"]').value=${JSON.stringify(`  ${c.key.toUpperCase()}  `)};document.querySelector('input[aria-label="Label value root"]').value='low'`);
   await click(c.action);
   if (c.refused) {
    assert(`${c.name}: no write`, await count()===0);
    assert(`${c.name}: key guard explains refusal`, await ev("document.querySelector('.op-workspace').textContent.includes('Choose an ordinary label key.')"));
    continue;
   }
   await until("document.querySelector('.op-receipts').textContent.includes('Saved.')");
   const writes = await ev('window.__op.writes');
   assert(`${c.name}: exactly one PATCH to selected message labels`, writes.length===1 && writes[0].method==='PATCH' && writes[0].path==='/v1/boards/brd_test/messages/root/labels');
   const ops = writes[0].body.operations, op = ops[0];
   assert(`${c.name}: one operation with normalized key`, ops.length===1 && op.key==='priority');
   assert(`${c.name}: wire opcode`, op.op===(c.action==='Set label'?'set':'unset'));
   assert(`${c.name}: original expectation`, JSON.stringify(op.expect)===JSON.stringify(c.old===undefined?{absent:true}:{value:c.old}));
   assert(`${c.name}: value only for set`, c.action==='Set label'?op.value==='low':!Object.hasOwn(op,'value'));
   assert(`${c.name}: fixture applies supported operation`, await ev(c.action==='Set label'?"window.__op.root.labels.priority==='low'":"!Object.hasOwn(window.__op.root.labels,'priority')"));
   console.log(`OP17-035 WIRE ${JSON.stringify(writes[0].body)}`);
  } finally {
   await ev("document.querySelector('#operator-logout')?.click()");
   await until('!document.body.dataset.operator');
  }
 }
 assert('all nine fresh fixtures executed nonzero assertions', fixtures===9 && assertions===30);
 assert('no browser exceptions', errors.length===0);
 console.log(`OP17-035 executed ${assertions} assertions across ${fixtures} fresh label fixtures`);
}
const operatorCases = new Map([['OP17-035', labelOpcodeCase], ['OP17-019', cosmeticReceiptCase], ['OP17-018', delayedDraftCase], ['OP17-007', refreshDiscoveryCase], ['OP17-006', openRaceCase], ['OP17-005', boardSwitchCase], ['OP17-003', identityNavigationCase], ['OP17-029', responseBodyFailureCase], ['OP17-042', renewalClaimViewCase]]);
const selectedCase = process.env.OPERATOR_CASE;
try {
 await cmd('Page.navigate',{url:ORIGIN+'/#/b/brd_test?ch=ops&t=th'});await until("!document.querySelector('#operator-option').hidden");
 if (selectedCase && !operatorCases.has(selectedCase)) throw new Error(`Unknown OPERATOR_CASE: ${selectedCase}`);
 if (!selectedCase) {
 // Unsafe credentials fail closed without writing or persistence.
 for(const [name,setup]of [['agent',"window.__op.kind='agent'"],['broad scope',"window.__op.scope=['*']"],['credential flag',"window.__op.flags=false"]]){
  await ev(`${setup};document.querySelector('#operator-login').checked=true;document.querySelector('#token').value='fake-operator';document.querySelector('#login-form').requestSubmit()`);await sleep(150);
  check(`${name} refused without storage or writes`,await ev("!document.querySelector('#login').hidden && !sessionStorage.getItem('yak-viewer-token') && !window.__op.writes.length"));
  await ev("window.__op.kind='human';window.__op.scope=null;window.__op.flags=true");
 }
 await ev("document.querySelector('#token').value='fake-operator';document.querySelector('#login-form').requestSubmit()");await until("document.body.dataset.operator==='true'");
 check('operator token memory only and password cleared',await ev("!sessionStorage.length && !localStorage.getItem('yak-viewer-token') && document.querySelector('#token').value==='' && !location.href.includes('fake-operator') && !document.cookie && !history.state"));
 await open();
 check('server content renders as text',await ev("!window.pwned && !document.querySelector('.op-workspace script,.op-workspace img') && document.querySelector('.op-workspace').textContent.includes('<script>')"));
 await ev("window.__op.permissions=['read','post','label','claim']");await click('Refresh permissions and state');await sleep(100);
 check('non-manager has no Approve or flag action',await ev("![...document.querySelectorAll('.op-workspace button')].some(b=>b.textContent==='Approve and unblock'||b.textContent==='Remove approval requirement')"));
 await ev("window.__op.permissions=null;delete window.__op.task.direct_action_required");await click('Refresh permissions and state');await sleep(100);
 check('missing gate field hides Approve',await ev("![...document.querySelectorAll('.op-workspace button')].some(b=>b.textContent==='Approve and unblock')"));
 await ev("window.__op.task.direct_action_required=false");await click('Refresh permissions and state');await sleep(100);
 check('label-only gate is explicit',await ev("document.querySelector('.op-workspace').textContent.includes('label only; not a gate')"));
 for(const principal of [null,'a']) {
  await ev(`window.__op.task.direct_action_required=true;window.__op.task.state='ready';window.__op.events=[{id:'nonhuman'+${JSON.stringify(principal)},type:'task.unblocked',actor_id:'bot',on_behalf_of_actor_id:${JSON.stringify(principal)},credential_id:'agent-c',recorded_at:new Date().toISOString(),payload:{task:{...window.__op.task},reason:'gate approved'}}]`);
  await click('Refresh permissions and state');await until(`document.querySelector('.op-workspace').textContent.includes("not a person's approval")`);
  check(`agent unblock is never approval (principal ${principal})`,await ev("document.querySelector('.op-workspace').textContent.includes('not approved')"));
  if(principal)check('delegated provenance shows both actors',await ev("document.querySelector('.op-workspace').textContent.includes('@bot (agent) for @alice (human)')"));
 }
 await ev("window.__op.task.state='blocked';window.__op.events=[];window.__op.thread.labels.gate='approved'");await click('Refresh permissions and state');await sleep(100);
 check('display approval cannot hide a blocked gate',await ev("document.querySelector('.op-workspace').textContent.includes('label says approved; task is still blocked')"));
 await ev("window.__op.thread.labels.gate='pending'");await click('Refresh permissions and state');await sleep(100);

 await matrix('Remove approval requirement');check('A13 clear sends only gate flag and If-Match',await ev("JSON.stringify(window.__op.writes.at(-1).body)===''+JSON.stringify({direct_action_required:false}) && !!window.__op.writes.at(-1).headers['If-Match']"));
 await matrix('Require my approval');
 await matrix('Request changes','Request changes',"document.querySelector('textarea[aria-label=\"Gate note t\"]').value='Please change this'");
 check('A12 reply before label with expect, task stays blocked',await ev("window.__op.writes.at(-2).path.endsWith('/messages') && window.__op.writes.at(-1).body.operations[0].expect.value==='pending' && window.__op.task.state==='blocked' && window.__op.task.direct_action_required"));
 await ev("window.__op.failLabel=1");await matrix('Approve and unblock');
 check('A6 cosmetic failure is explicit',await ev("document.querySelector('.op-receipts').textContent.includes('Approved; label not updated')"));
 const unblocks=await ev("window.__op.writes.filter(x=>x.path.endsWith('/unblock')).length");await click('Retry label only');await sleep(150);
 check('A6 cosmetic retry never repeats unblock',await ev(`window.__op.writes.filter(x=>x.path.endsWith('/unblock')).length===${unblocks} && window.__op.task.direct_action_required`));
 await matrix('Assign to myself','Assign to @alice');
 await ev("{const sel=document.querySelector('select[aria-label=\"New assignee\"]');sel.value='';}");await matrix('Assign / unassign','Unassign');
 await matrix('Work on this now (while this tab is open)','Claim');check('claim proof never shown or stored',await ev("!document.body.textContent.includes('fake-secret-proof') && !JSON.stringify(sessionStorage).includes('fake-secret-proof')"));
 await matrix('Release');check('release carries proof header',await ev("window.__op.writes.at(-1).headers['Yak-Claim-Proof']==='fake-secret-proof'"));
 for(const gate of [false,true])await matrix('Create task','Create task',`document.querySelector('input[aria-label^="Task title"]').value='new task';document.querySelector('input[type=checkbox][aria-label^="Needs my approval"]').checked=${gate}`);
 check('A11 no dependencies',await ev("!('depends_on' in window.__op.writes.at(-1).body) && window.__op.writes.at(-1).body.direct_action_required===true"));
 // Reopen the synthetic gate to exercise conflict and ambiguous transport paths.
 await ev("window.__op.task.state='blocked';window.__op.task.current_claim=null;window.__op.task.assignee_actor_id=null;window.__op.conflict=true;window.__op.thread.labels.gate='pending'");await click('Refresh permissions and state');await sleep(150);await click('Approve and unblock');await confirm('Approve and unblock');
 check('412 displays before/after and never retries',await ev("document.querySelector('.op-receipts').textContent.includes('Previously read:') && document.querySelector('.op-receipts').textContent.includes('No retry was sent')"));
 await click('Review current state');await sleep(150);await ev("window.__op.dropUnblock=true");await click('Approve and unblock');await confirm('Approve and unblock');check('uncertain intent disables every new write button',await ev("[...document.querySelectorAll('.op-workspace button')].filter(b=>['Send reply','Set label','Create task'].includes(b.textContent)).every(b=>b.disabled)"));await click('Retry same intent');await sleep(150);
 check('ambiguous unblock retry reuses receipt',await ev("window.__op.seenKeys.at(-1)===window.__op.seenKeys.at(-2)"));
 // A store resync must leave the pending draft and panel intact.
 const snapshots=await ev('window.__op.snapshots');
 await ev("document.querySelector('textarea[aria-label^=Message]').value='unsent draft';window.__op.resync=true");
 await until(`window.__op.snapshots>${snapshots}`);
 check('resync keeps operator draft without resending',await ev("document.querySelector('textarea[aria-label^=Message]').value==='unsent draft'"));
 await ev("window.__op.task.state='ready';window.__op.task.assignee_actor_id=null");await click('Refresh permissions and state');await sleep(150);
 await click('Work on this now (while this tab is open)');await confirm('Claim');await sleep(150);
 await click('Close');
 await ev("document.querySelector('#operator-logout').click()");await until("!document.querySelector('#login').hidden");
 check('self logout revokes only own credential and clears session',await ev("window.__op.revoked && window.__op.writes.at(-1).path==='/v1/credentials/cred_test' && !document.body.dataset.operator && !document.querySelector('.op-workspace') && !sessionStorage.length"));
 check('logout releases held claim before revoke',await ev("window.__op.writes.at(-2).path.endsWith('/release')"));
 await ev("window.__op.revoked=false;document.querySelector('#token').value='fake-operator';document.querySelector('#login-form').requestSubmit()");await until("document.body.dataset.operator==='true'");await open();
 await ev("window.__op.expired=true");await click('Refresh permissions and state');await until("!document.querySelector('#login').hidden");
 const failures=await ev('window.__op.authFailures');await sleep(400);
 check('stale authorization ends session without retry',await ev(`!document.body.dataset.operator && window.__op.authFailures===${failures}`));
 await cmd('Page.reload');await sleep(500);
 check('reload starts at read-only login without token',await ev("!document.querySelector('#login').hidden && !document.body.dataset.operator && !sessionStorage.length && !window.__op.writes.length"));

 await ev("document.querySelector('#operator-login').checked=true;document.querySelector('#token').value='fake-operator';document.querySelector('#login-form').requestSubmit()");await until("document.body.dataset.operator==='true'");await open();
 await ev("window.__op.task.state='ready'");await click('Refresh permissions and state');await sleep(100);await click('Work on this now (while this tab is open)');await confirm('Claim');
 await ev("window.__realNow=Date.now;Date.now=()=>window.__realNow()+840000");await until("document.querySelector('#operator-identity').textContent.includes('idle logout')");
 check('idle warning appears at fourteen minutes',await ev("!document.querySelector('#operator-session').hidden"));
 await ev("Date.now=()=>window.__realNow()+901000");await until("!document.querySelector('#login').hidden");
 check('idle timeout releases then self-revokes without polling extending it',await ev("window.__op.writes.at(-2).path.endsWith('/release') && window.__op.revoked && !document.body.dataset.operator"));
 check('no browser exceptions',errors.length===0,errors.join(' | '));
 }
 for (const [name, run] of operatorCases) if (!selectedCase || name===selectedCase) await run();
} catch(e) {check('operator browser run',false,[e.message,...errors,await ev("document.querySelector('.op-workspace')?.textContent")].join(' | '));}
finally {browser.close();chrome.kill();server.close();await sleep(300);await rm(profile,{recursive:true,force:true}).catch(()=>{});}
process.exit(failed?1:0);
