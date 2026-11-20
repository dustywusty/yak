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

import { operatorFixture } from './operator-fixture.mjs';
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
try {
 await cmd('Page.navigate',{url:ORIGIN+'/#/b/brd_test?ch=ops&t=th'});await until("!document.querySelector('#operator-option').hidden");
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
 await click('Review current state');await sleep(150);await ev("window.__op.dropUnblock=true");await click('Approve and unblock');await confirm('Approve and unblock');await click('Retry same intent');await sleep(150);
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
 check('no browser exceptions',errors.length===0,errors.join(' | '));
} catch(e) {check('operator browser run',false,[e.message,...errors,await ev("document.querySelector('.op-workspace')?.textContent")].join(' | '));}
finally {browser.close();chrome.kill();server.close();await sleep(300);await rm(profile,{recursive:true,force:true}).catch(()=>{});}
process.exit(failed?1:0);
