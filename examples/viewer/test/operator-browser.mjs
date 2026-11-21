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

import { operatorFixture, op17032Clock, op17036Cases, op17020Cases, op17042ClaimCorpus } from './operator-fixture.mjs';
// ---------- static server and Chrome ----------
const server = createServer(async (req, res) => {
  const path = new URL(req.url, 'http://x').pathname.replace(/^\/$/, '/index.html');
  if (path.includes('..')) { res.writeHead(400).end(); return; }
  if (process.env.VIEWER_PREVIEW && path === '/fixture.js') {
    res.writeHead(200, { 'Content-Type': 'text/javascript' }).end(`(${operatorFixture.toString()})(); window.__op.scope=['read']; sessionStorage.setItem('yak-viewer-token', 'synthetic-preview-read-token');`); return;
  }
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
let session, op17028FixtureInstallations=0;
const cmd = (method, params = {}) => new Promise((r) => {
  if(method==='Page.addScriptToEvaluateOnNewDocument' && params.source===`(${operatorFixture})();`)op17028FixtureInstallations++;
  const i = ++id; pending.set(i, r); browser.send(JSON.stringify({ id: i, method, params, ...(session ? { sessionId: session } : {}) }));
});
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
async function matrix(action,named=action,setup='',report=check,reject=false){
 const check=report;
 if(setup)await ev(setup);
 const n=await count();
 await op17027TextFields('.op-workspace',action,check);check(`${action}: Enter in text fields sends nothing`,await count()===n);
 const details=await op17030ExpectedDetails(action);
 const inspect=async()=>{const actual=await ev("document.querySelector('.op-confirm pre').textContent");check(`${action}: exact confirmed read values`,actual===details,JSON.stringify({expected:details,actual}));};
 await click(action);await until("!!document.querySelector('.op-confirm[open]')");
 check(`${action}: no write on open`,await count()===n);
 await inspect();
 check(`${action}: Cancel focused`,await ev("document.activeElement.textContent==='Cancel'"));await cancel();check(`${action}: Cancel sends nothing`,await count()===n);
 await click(action);await until("!!document.querySelector('.op-confirm[open]')");
 await inspect();
 await op17027Enter();
 check(`${action}: Enter on Cancel sends nothing`,await count()===n);
 if(await ev("!!document.querySelector('.op-confirm[open]')"))await op17027TextFields('.op-confirm',action,check);
 if(await ev("!!document.querySelector('.op-confirm[open]')"))await cancel();
 const receipts=await ev("document.querySelectorAll('.op-pending').length");
 await ev(`window.__op.enableConfirmationDelay();window.__op.delayConfirmation=true;window.__op.rejectConfirmation=${reject}`);
 await click(action);await until("!!document.querySelector('.op-confirm[open]')");
 await inspect();
 const expected=await op17026ExpectedWrites(action);
 await confirm(named);
 await until("typeof window.__op.resolveConfirmation==='function'");
 check(`${action}: double click sends only first step before success`,await op17026Matches(n,expected.slice(0,1)));
 check(`${action}: one pending receipt while response delayed`,await ev(`document.querySelectorAll('.op-pending').length===${receipts+1} && [...document.querySelectorAll('.op-pending')].at(-1).textContent==='Sending…'`));
 await ev("window.__op.resolveConfirmation()");
 await until(`[...document.querySelectorAll('.op-pending')].length===${receipts+1} && ![...document.querySelectorAll('.op-pending')].at(-1).textContent.includes('Sending')`);
 check(`${action}: explicit confirmation writes exact ordered intent`,await op17026Matches(n,reject?expected.slice(0,1):expected));
 check(`${action}: one receipt after settlement`,await ev(`document.querySelectorAll('.op-pending').length===${receipts+1}`));
}
async function op17033ConflictCase() {
 let assertions=0;
 const assert=(name,ok,detail='')=>{assertions++;check(`OP17-033: ${name}`,ok,detail)};
 await freshIdentityFixture("Object.assign(window.__op.task,{revision:833,brief_revision:33,assignee_actor_id:'a'});window.__op.conflict=true;");
 try {
  await ev("document.querySelector('#operator-login').checked=true;document.querySelector('#token').value='synthetic-conflict-token';document.querySelector('#login-form').requestSubmit()");
  await until("!!document.body && document.body.dataset.operator==='true'");await open();
  await ev("window.__op.enableConflictClock()");
  const before=await count(),details=await op17030ExpectedDetails('Approve and unblock');
  await click('Approve and unblock');await until("!!document.querySelector('.op-confirm[open]')");
  assert('initial confirmation displays exact original read',await ev("document.querySelector('.op-confirm pre').textContent")===details);
  assert('initial review sends no writes',await count()===before);
  await confirm('Approve and unblock');
  await until("!!document.querySelector('.op-receipts pre')");
  const capture=await ev('window.__op.conflictCapture');
  assert('412 captures exactly one rejected unblock',capture.writeCount===before+1 && capture.intent.method==='POST' && capture.intent.path==='/v1/boards/brd_test/tasks/t/unblock' && JSON.stringify(capture.intent.body)==='{"reason":"gate approved"}');
  assert('rejected intent uses exact original revision and an intent key',capture.intent.headers['If-Match']==='"833"' && /^[a-f0-9]{32}$/.test(capture.intent.headers['Idempotency-Key']));
  assert('legacy no-retry receipt wording is present',await ev("document.querySelector('.op-receipts').textContent.includes('Previously read:') && document.querySelector('.op-receipts').textContent.includes('No retry was sent')"));
  const shown=await ev("document.querySelector('.op-receipts pre').textContent");
  const values=t=>({state:t.state,assignee:t.assignee_actor_id,claim:t.current_claim?.actor_id||null,brief_revision:String(t.brief_revision),revision:String(t.revision),direct_action_required:t.direct_action_required});
  assert('receipt shows actual old and new assignee and revision',shown===`Previously read: ${JSON.stringify(values(capture.before))}\nNow: ${JSON.stringify(values(capture.after))}\nLabels previously: {"gate":"pending"}\nLabels now: {"gate":"pending"}` && capture.before.assignee_actor_id==='a' && capture.after.assignee_actor_id==='bot' && capture.before.revision===833 && capture.after.revision===834,shown);
  const actionWrites=()=>ev(`window.__op.writes.slice(${before}).filter(w=>!(w.method==='POST' && w.path==='/v1/boards/brd_test/tasks/t/claims/q/renew'))`);
  const noRepeat=async phase=>{
   await ev("window.__op.clockProbe=false;setTimeout(()=>window.__op.clockProbe=true,30000)");
   const drained=await ev('window.__op.drainConflictClock()');
   assert(`${phase}: controlled timers and refresh completions drained`,drained.fired>0 && drained.reads===0 && await ev('window.__op.clockProbe'));
   const writes=await actionWrites();
   assert(`${phase}: zero further action writes`,writes.length===1,JSON.stringify(writes));
   assert(`${phase}: rejected request headers and body remain unchanged`,JSON.stringify(writes[0])===JSON.stringify(capture.intent));
  };
  await noRepeat('after 412');
  await click('Review current state');
  await until("!!document.querySelector('.op-workspace [aria-label=\"Task actions\"]')");
  await noRepeat('after explicit read refresh');
  const latest=await op17030ExpectedDetails('Approve and unblock');
  assert('freshly displayed read uses changed assignee and exact revision',latest.includes('Assignee: @bot (agent)') && latest.includes('Revision: 834; brief: 33'));
  await click('Approve and unblock');await until("!!document.querySelector('.op-confirm[open]')");
  assert('new dialog displays exact refreshed read',await ev("document.querySelector('.op-confirm pre').textContent")===latest);
  await cancel();await noRepeat('after Cancel');
  await click('Approve and unblock');await until("!!document.querySelector('.op-confirm[open]')");
  assert('native Enter targets focused Cancel',await ev("document.activeElement.textContent==='Cancel'"));
  await op17027Enter();await noRepeat('after native Enter');
  if(await ev("!!document.querySelector('.op-confirm[open]')"))await cancel();
  await click('Approve and unblock');await until("!!document.querySelector('.op-confirm[open]')");
  assert('fresh explicit confirmation still shows latest values',await ev("document.querySelector('.op-confirm pre').textContent")===latest);
  await noRepeat('before fresh confirmation');
  await confirm('Approve and unblock');
  await until("!!document.querySelector('.op-receipts') && document.querySelector('.op-receipts').textContent.includes('Saved.')");
  await ev('window.__op.drainConflictClock()');
  const writes=await actionWrites(),unblocks=writes.filter(w=>w.path.endsWith('/unblock'));
  assert('fresh confirmation adds exactly one new state-changing intent',writes.length===3 && unblocks.length===2 && writes[2].method==='PATCH' && writes[2].path==='/v1/boards/brd_test/messages/root/labels' && writes[2].body.operations[0].value==='approved',JSON.stringify(writes));
  assert('new explicit unblock uses latest exact revision and a new key',unblocks[1]?.headers['If-Match']==='"834"' && /^[a-f0-9]{32}$/.test(unblocks[1]?.headers['Idempotency-Key']) && unblocks[1]?.headers['Idempotency-Key']!==capture.intent.headers['Idempotency-Key']);
  assert('original rejected If-Match never changes',unblocks[0]?.headers['If-Match']==='"833"' && JSON.stringify(unblocks[0])===JSON.stringify(capture.intent));
  assert('new intent succeeds against changed task',await ev("window.__op.task.state==='ready' && window.__op.task.assignee_actor_id==='bot' && window.__op.task.revision===835"));
 } finally {
  await ev("window.__op.disposeConflictClock?.();document.querySelector('#operator-logout')?.click()");
 }
 assert('no browser exceptions',errors.length===0,errors.join(' | '));
 if(assertions!==30)throw new Error(`OP17-033 expected 30 assertions, executed ${assertions}`);
 console.log(`OP17-033 executed ${assertions} assertions across one fresh fixture`);
}
// OP17-030: the oracle uses API fixture reads and form inputs, never dialog labels.
async function op17030ExpectedDetails(action) {
 const {task:t,values:v}=await ev(`({task:structuredClone(window.__op.task),values:{title:document.querySelector('input[aria-label^="Task title"]').value,channel:document.querySelector('select[aria-label="Channel"]').value,text:document.querySelector('textarea[aria-label^="Message"]').value,assignee:document.querySelector('select[aria-label="Task assignee"]').value || null,gate:document.querySelector('input[aria-label^="Needs my approval"]').checked,note:document.querySelector('textarea[aria-label="Gate note t"]')?.value || '',newAssignee:document.querySelector('select[aria-label="New assignee"]')?.value || null}})`);
 const actor=id=>id==='a'?'@alice (human)':id==='bot'?'@bot (agent)':id?`${id.slice(0,16)} (unknown)`:'none';
 const lines=['Acting as @alice (human)'];
 if(action==='Create task')return [...lines,`Channel: ${v.channel}`,`Title: ${v.title}`,`Assignee: ${actor(v.assignee)}`,`Needs my approval: ${v.gate}`,`Text: ${v.text}`].join('\n');
 const next=action==='Approve and unblock'?'ready or waiting (prerequisites decide)':action.startsWith('Work on')?'claimed':action==='Release'?'ready':t.state;
 lines.push(`Task: ${t.title}`,`Channel: ${t.channel_path}`,`State: ${t.state} → ${next}`,`Assignee: ${actor(t.assignee_actor_id)}`,`Claim holder: ${actor(t.current_claim?.actor_id)}`,`Revision: ${t.revision}; brief: ${t.brief_revision}`);
 if(action==='Require my approval'||action==='Remove approval requirement')lines.push(`Needs my approval: ${t.direct_action_required} → ${action==='Require my approval'}`);
 if(action==='Assign to myself'||action==='Assign / unassign')lines.push(`New assignee: ${actor(action==='Assign to myself'?'a':v.newAssignee)}`);
 if(action==='Approve and unblock'||action==='Request changes')lines.push(`Gate reason: ${t.block.reason}`,`Note: ${v.note}`);
 return lines.join('\n');
}
async function op17030ConfirmationValuesCase() {
 let assertions=0,fixtures=0;
 const assert=(name,ok,detail='')=>{assertions++;check(`OP17-030: ${name}`,ok,detail)};
 const variants=[
  ['A6','Approve and unblock','Approve and unblock'],
  ['A7','Work on this now (while this tab is open)','Claim'],['A9','Release','Release'],
  ['A10 assign','Assign to myself','Assign to @alice'],['A10 unassign','Assign / unassign','Unassign'],
  ['A11 off','Create task','Create task',false],['A11 on','Create task','Create task',true],
  ['A12','Request changes','Request changes'],['A13 on','Require my approval','Require my approval'],['A13 off','Remove approval requirement','Remove approval requirement'],
 ];
 for(const [variant,action,named,gate=false] of variants) {
  // Each variant starts with distinct fixed read values and its own write capture.
  await freshIdentityFixture(`Object.assign(window.__op.task,{title:'OP17-030 read title',revision:730,brief_revision:43,state:${JSON.stringify(['A7','A9'].includes(variant)?'ready':'blocked')},assignee_actor_id:${JSON.stringify(['A7','A9'].includes(variant)?'a':'bot')},direct_action_required:${action!=='Require my approval'},block:{reason:'OP17-030 waiting for security review'}});window.__op.enableConfirmationDelay();`);fixtures++;
  try {
   await ev("document.querySelector('#operator-login').checked=true;document.querySelector('#token').value='synthetic-values-token';document.querySelector('#login-form').requestSubmit()");
   await until("!!document.body && document.body.dataset.operator==='true'");await open();
   if(action==='Release'){await click('Work on this now (while this tab is open)');await confirm('Claim');await until("!!document.querySelector('.op-receipts') && document.querySelector('.op-receipts').textContent.includes('Saved.')");}
   await ev(`document.querySelector('input[aria-label^="Task title"]').value='OP17-030 new title';document.querySelector('textarea[aria-label^="Message"]').value='OP17-030 new task text';document.querySelector('select[aria-label="Task assignee"]').value=${JSON.stringify(gate?'a':'bot')};document.querySelector('input[aria-label^="Needs my approval"]').checked=${gate};const note=document.querySelector('textarea[aria-label="Gate note t"]');if(note)note.value='OP17-030 requested security changes';const assignee=document.querySelector('select[aria-label="New assignee"]');if(assignee)assignee.value='';`);
   await matrix(action,named,'',(name,ok,detail)=>assert(`${variant}: ${name}`,ok,detail));
   if(action==='Request changes')assert(`${variant}: gate flag remains true and state remains blocked`,await ev("window.__op.task.direct_action_required===true && window.__op.task.state==='blocked'"));
  } finally {await ev("window.__op.resolveConfirmation?.();document.querySelector('#operator-logout')?.click()");}
 }
 assert('all ten variants executed',fixtures===10 && assertions>=140);
 assert('no browser exceptions',errors.length===0,errors.join(' | '));
 console.log(`OP17-030 executed ${assertions} assertions across ${fixtures} fresh fixtures`);
}
// CDP input invokes browser default actions, unlike dispatchEvent(KeyboardEvent).
async function op17027Enter() {
 for(const type of ['keyDown','keyUp']) {
  const result=await cmd('Input.dispatchKeyEvent',{type,key:'Enter',code:'Enter',windowsVirtualKeyCode:13,nativeVirtualKeyCode:13,text:type==='keyDown'?'\r':undefined});
  if(result.error)throw new Error(`OP17-027 native Enter failed: ${JSON.stringify(result.error)}`);
 }
 await sleep(150); // Let implicit submission and asynchronous write capture settle.
}
async function op17027TextFields(scope,variant,assert) {
 const selector=scope+' input:not([type=checkbox]):not([type=radio]):not([type=hidden]),'+scope+' textarea';
 const editable=`Array.from(document.querySelectorAll(${JSON.stringify(selector)})).filter(f=>!f.disabled && !f.readOnly)`;
 const fields=await ev(`${editable}.map(f=>f.getAttribute('aria-label') || f.name || f.tagName)`);
 for(const [index,label] of fields.entries()) {
  const before=await count();
  const focused=await ev(`(()=>{const f=(${editable})[${index}];if(!f)return false;if(!f.value)f.value='OP17-027 nonempty value';f.focus();return document.activeElement===f && !!f.value;})()`);
  assert(`${variant}: ${scope} ${label} focused with nonempty value`,focused);
  await op17027Enter();
  assert(`${variant}: ${scope} ${label} native Enter sends no writes`,await count()===before);
 }
 return fields.length;
}
async function op17027NativeEnterCase() {
 let assertions=0,fixtures=0,fields=0;
 const assert=(name,ok,detail='')=>{assertions++;check(`OP17-027: ${name}`,ok,detail)};
 const variants=[
  ['A6','Approve and unblock'],['A7','Work on this now (while this tab is open)'],['A9','Release'],
  ['A10 assign','Assign to myself'],['A10 unassign','Assign / unassign'],
  ['A11 approval off','Create task',false],['A11 approval on','Create task',true],
  ['A12','Request changes'],['A13 false→true','Require my approval'],['A13 true→false','Remove approval requirement'],
 ];
 for(const [variant,action,approval=false] of variants) {
  await freshIdentityFixture(`window.__op.task.state=${JSON.stringify(['A7','A9'].includes(variant)?'ready':'blocked')};window.__op.task.direct_action_required=${action!=='Require my approval'};window.__op.task.assignee_actor_id=${variant==='A10 unassign'?"'a'":'null'};`);fixtures++;
  try {
   await ev("document.querySelector('#operator-login').checked=true;document.querySelector('#token').value='synthetic-enter-token';document.querySelector('#login-form').requestSubmit()");
   await until("!!document.body && document.body.dataset.operator==='true'");await open();
   if(action==='Release'){await click('Work on this now (while this tab is open)');await confirm('Claim');await until("!!document.querySelector('.op-receipts') && document.querySelector('.op-receipts').textContent.includes('Saved.')");}
   await ev(`document.querySelector('input[aria-label^="Task title"]').value='Native Enter task';document.querySelector('textarea[aria-label^="Message"]').value='Native Enter brief';document.querySelector('input[aria-label^="Needs my approval"]').checked=${approval};const note=document.querySelector('textarea[aria-label="Gate note t"]');if(note)note.value='Please change this';`);
   if(variant==='A10 unassign')await ev("document.querySelector('select[aria-label=\"New assignee\"]').value=''");
   const before=await count();
   const tested=await op17027TextFields('.op-workspace',variant,assert);fields+=tested;
   assert(`${variant}: workspace text fields executed`,tested>=2);
   assert(`${variant}: native Enter before confirmation sends no writes`,await count()===before);
   await click(action);await until("!!document.querySelector('.op-confirm[open]')");
   assert(`${variant}: Cancel initially focused`,await ev("document.activeElement.matches('.op-confirm button') && document.activeElement.textContent==='Cancel'"));
   await op17027Enter();assert(`${variant}: native Enter on Cancel sends no writes`,await count()===before);
   if(await ev("!!document.querySelector('.op-confirm[open]')"))await cancel();
   await click(action);await until("!!document.querySelector('.op-confirm[open]')");
   const dialogFields=await op17027TextFields('.op-confirm',variant,assert);fields+=dialogFields;
   assert(`${variant}: dialog fields inspected (${dialogFields} editable) and no writes`,await count()===before);
   await cancel();assert(`${variant}: Cancel after field input sends no writes`,await count()===before);
  } finally {await ev("document.querySelector('#operator-logout')?.click()");}
 }
 assert('all ten action variants and workspace fields executed',fixtures===10 && fields>=20 && assertions>=100);
 assert('no browser exceptions',errors.length===0,errors.join(' | '));
 console.log(`OP17-027 executed ${assertions} assertions, ${fields} native text-field Enter cases across ${fixtures} fresh fixtures`);
}
// Independent wire table: fixed fixture values and values read before confirmation.
async function op17026ExpectedWrites(action) {
 const read=await ev(`({revision:String(window.__op.confirmationTaskRead?.revision),brief:window.__op.confirmationTaskRead?.brief_revision,gate:window.__op.root.labels.gate,note:document.querySelector('textarea[aria-label="Gate note t"]')?.value || '',title:document.querySelector('input[aria-label^="Task title"]')?.value,text:document.querySelector('textarea[aria-label^="Message"]')?.value,approval:document.querySelector('input[aria-label^="Needs my approval"]')?.checked,assignee:document.querySelector('select[aria-label="Task assignee"]')?.value || null})`);
 const B='/v1/boards/brd_test', task=B+'/tasks/t';
 const request=(method,path,body,headers={})=>({method,path,body,headers});
 const label=value=>request('PATCH',B+'/messages/root/labels',{operations:[{op:'set',key:'gate',value,expect:{value:read.gate}}]});
 switch(action) {
  case 'Approve and unblock':return [request('POST',task+'/unblock',{reason:'gate approved'+(read.note?': '+read.note:'')},{'If-Match':'"'+read.revision+'"'}),label('approved')];
  case 'Request changes':return [request('POST',B+'/threads/th/messages',{text:read.note}),label('changes_requested')];
  case 'Work on this now (while this tab is open)':return [request('POST',task+'/claims',{lease_seconds:300,expect_brief_revision:read.brief})];
  case 'Release':return [request('POST',task+'/release',{reason:'released by operator'},{'Yak-Claim-Proof':'fake-secret-proof'})];
  case 'Assign to myself':return [request('PUT',task+'/assignee',{assignee_actor_id:'a'},{'If-Match':'"'+read.revision+'"'})];
  case 'Assign / unassign':return [request('PUT',task+'/assignee',{assignee_actor_id:null},{'If-Match':'"'+read.revision+'"'})];
  case 'Remove approval requirement':case 'Require my approval':return [request('PATCH',task,{direct_action_required:action==='Require my approval'},{'If-Match':'"'+read.revision+'"'})];
  case 'Create task':return [request('POST',B+'/tasks',{thread:{channel:'ops',title:read.title,text:read.text},assignee_actor_id:read.assignee,direct_action_required:read.approval})];
  default:throw new Error('OP17-026 missing expected request for '+action);
 }
}
async function op17026Matches(before,expected) {
 const writes=await ev(`window.__op.writes.slice(${before}).filter(w=>!(w.method==='POST' && w.path==='/v1/boards/brd_test/tasks/t/claims/q/renew'))`);
 const keys=writes.map(w=>w.headers?.['Idempotency-Key']);
 const actual=writes.map(w=>({...w,headers:Object.fromEntries(Object.entries(w.headers || {}).filter(([k])=>/^if-/i.test(k) || k.toLowerCase()==='yak-claim-proof'))}));
 const canonical=value=>JSON.stringify(value,(_key,v)=>v && !Array.isArray(v) && typeof v==='object'?Object.fromEntries(Object.entries(v).sort(([a],[b])=>a.localeCompare(b))):v);
 const ok=canonical(actual)===canonical(expected) && keys.every(k=>typeof k==='string' && k.length>=22) && new Set(keys).size===expected.length;
 if(!ok)console.log('OP17-026 wire mismatch',JSON.stringify({actual,expected,keyCount:keys.length,uniqueKeys:new Set(keys).size}));
 return ok;
}
async function op17026ConfirmationCase() {
 let assertions=0,fixtures=0;
 const assert=(name,ok,detail='')=>{assertions++;check(`OP17-026: ${name}`,ok,detail)};
 const variants=[
  ['Approve and unblock','Approve and unblock'],
  ['Request changes','Request changes'],
  ['Work on this now (while this tab is open)','Claim'],['Release','Release'],
  ['Assign to myself','Assign to @alice'],['Assign / unassign','Unassign'],
  ['Create task','Create task',false],['Create task','Create task',true],
  ['Require my approval','Require my approval'],['Remove approval requirement','Remove approval requirement'],
  ['Approve and unblock','Approve and unblock',null,true],['Request changes','Request changes',null,true],
 ];
 for(const [action,named,gate,reject=false] of variants) {
  await freshIdentityFixture(`window.__op.task.state=${JSON.stringify(action.includes('Work on') || action==='Release'?'ready':'blocked')};window.__op.task.direct_action_required=${action!=='Require my approval'};window.__op.task.assignee_actor_id=${action==='Assign / unassign'?"'a'":'null'};`);fixtures++;
  try {
   await ev("document.querySelector('#operator-login').checked=true;document.querySelector('#token').value='synthetic-confirm-token';document.querySelector('#login-form').requestSubmit()");
   await until("!!document.body && document.body.dataset.operator==='true'");await open();
   if(action==='Release') {await click('Work on this now (while this tab is open)');await confirm('Claim');await until("!!document.querySelector('.op-receipts') && document.querySelector('.op-receipts').textContent.includes('Saved.')");}
   if(action==='Assign / unassign')await ev("document.querySelector('select[aria-label=\"New assignee\"]').value=''");
   await matrix(action,named,`{const note=document.querySelector('textarea[aria-label="Gate note t"]');if(note)note.value='Please change this';}document.querySelector('input[aria-label^="Task title"]').value='Confirmation task';document.querySelector('textarea[aria-label^="Message"]').value='Confirmation brief';document.querySelector('input[aria-label^="Needs my approval"]').checked=${!!gate}`,assert,reject);
   if(reject)assert(`${action}: rejected first step never updates label`,await ev("window.__op.root.labels.gate==='pending' && !document.querySelector('.op-receipts').textContent.includes('Saved.')"));
  } finally {await ev("window.__op.resolveConfirmation?.();document.querySelector('#operator-logout')?.click()");}
 }
 assert('all required variants executed',fixtures===12 && assertions>=120);
 assert('no browser exceptions',errors.length===0,errors.join(' | '));
 console.log(`OP17-026 executed ${assertions} assertions across ${fixtures} fresh fixtures`);
}
// Each named regression owns a fresh in-page fixture and its write capture.
async function freshIdentityFixture(setup = '') {
 const { result: { identifier } } = await cmd('Page.addScriptToEvaluateOnNewDocument', {source: `if(location.origin===${JSON.stringify(ORIGIN)}){sessionStorage.clear();${setup}}`});
 try {
  await cmd('Page.navigate', {url:'about:blank'});
  await cmd('Page.navigate', {url:ORIGIN+'/#/boards'});
  await until("!!window.__op && !!document.querySelector('#operator-option') && !document.querySelector('#operator-option').hidden && !!document.body && ['login','boards'].includes(document.body.dataset.view)");
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
  await until("!!document.querySelector('#login') && !document.querySelector('#login').hidden");
  assert(`${name} clears identity and logout control`, await ev("document.querySelector('#me').textContent==='' && document.querySelector('#forget').hidden && document.querySelector('#operator-session').hidden && !document.body.dataset.operator && !sessionStorage.length"));
 };
 const login = async (operator) => {
  await ev(`document.querySelector('#operator-login').checked=${operator};document.querySelector('#token').value='synthetic-identity-token';document.querySelector('#login-form').requestSubmit()`);
  await until("!!document.body && document.body.dataset.view==='boards'");
 };
 await freshIdentityFixture("window.__op.scope=['read'];");
 await login(false);
 await header('fresh read-only login', 'Forget token');
 assert('read-only login persists only its tab token', await ev("sessionStorage.getItem('yak-viewer-token')==='synthetic-identity-token' && window.__op.writes.length===0"));
 await ev("document.querySelector('#forget').click()");
 await cleared('read-only forget');
 assert('read-only forget sends no writes', await count()===0);
 await freshIdentityFixture("window.__op.scope=['read'];sessionStorage.setItem('yak-viewer-token','synthetic-restored-token');");
 await until("!!document.body && document.body.dataset.view==='boards'");
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
  await until("!!document.querySelector('#login-error') && !document.querySelector('#login-error').hidden && !!document.querySelector('#connect') && !document.querySelector('#connect').disabled");
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
   await until("!!document.body && document.body.dataset.operator==='true'"); await open();
   await ev(`window.__op.responseFailure=${JSON.stringify(fault)}`);
   await click('Work on this now (while this tab is open)'); await confirm('Claim');
   if (fault === 'broken401') {
    await until("!!document.body && !document.body.dataset.operator");
    assert('malformed 401 ends without release/revoke/retry', await count() === 1);
    assert('malformed 401 clears session UI', await ev("!document.querySelector('.op-workspace') && !sessionStorage.length"));
   } else if (fault === 'broken412') {
    await until("!!document.querySelector('.op-receipts') && document.querySelector('.op-receipts').textContent.includes('No retry was sent')");
    assert('malformed 412 is a known rejection', await ev("!document.querySelector('.op-receipts').textContent.includes('Retry same intent') && !document.querySelector('.op-receipts').textContent.includes('Saved.') && window.__op.writes.length===1"));
   } else if (fault === 'empty204') {
    await until("!!document.querySelector('.op-receipts') && document.querySelector('.op-receipts').textContent.includes('Saved.')");
    assert('204 succeeds with explicit proof unavailable notice', await ev("document.querySelector('#operator-identity').textContent.includes('proof unavailable') && window.__op.writes.length===1"));
   } else {
    assert(`${fault} keeps receipt uncertain without completion or proof`, await ev("document.querySelector('.op-receipts').textContent.includes('Retry same intent') && !document.querySelector('.op-receipts').textContent.includes('Saved.') && !document.querySelector('#operator-identity').textContent.includes('proof unavailable') && !document.querySelector('.op-lease') && window.__op.writes.length===1"));
    assert(`${fault} prevents a new intent`, await ev("[...document.querySelectorAll('[data-op-write]')].every(b=>b.disabled)"));
    // Continue other variants even against the unfixed transport during RED.
    if (await ev("document.querySelector('.op-receipts').textContent.includes('Retry same intent')")) {
     await click('Retry same intent');
     await until("!!document.querySelector('.op-receipts') && document.querySelector('.op-receipts').textContent.includes('Saved.')");
     assert(`${fault} replay uses the same request and key`, await ev("window.__op.writes.length===2 && JSON.stringify(window.__op.writes[0])===JSON.stringify(window.__op.writes[1])"));
     assert(`${fault} replay obtains a usable proof`, await ev("!!document.querySelector('.op-lease') && !document.querySelector('#operator-identity').textContent.includes('proof unavailable')"));
     await click('Release'); await confirm('Release');
     assert(`${fault} release carries recovered proof`, await ev("window.__op.writes.at(-1).headers['Yak-Claim-Proof']==='fake-secret-proof'"));
    }
   }
  } finally {
   if (await ev("document.body.dataset.operator==='true'")) {
    await ev("document.querySelector('#operator-logout').click()"); await until("!!document.body && !document.body.dataset.operator");
   }
  }
 }
 assert('browser has no exceptions', errors.length === 0);
 console.log(`OP17-029 executed ${assertions} assertions across six fresh body-failure fixtures`);
}
async function op17032AutomaticRenewalCase() {
 let assertions=0, fixtures=0;
 const assert=(name,ok)=>{assertions++;check(`OP17-032: ${name}`,ok)};
 const variants=['success','retry',403,404,409,...op17042ClaimCorpus.slice(1).map(v=>v.status),'deadline','logout','unmount'];
 for(const variant of variants) {
  await freshIdentityFixture(`window.__op.task.state='ready';window.__op.testLease=30;window.__op.clock=(${op17032Clock.toString()})();Object.defineProperty(performance,'now',{configurable:true,value:window.__op.clock.now});window.setTimeout=window.__op.clock.schedule;window.clearTimeout=window.__op.clock.unschedule;`);fixtures++;
  try {
   await ev("document.querySelector('#operator-login').checked=true;document.querySelector('#token').value='synthetic-auto-renew';document.querySelector('#login-form').requestSubmit()");
   await until("!!document.body && document.body.dataset.operator==='true'");await open();
   await click('Work on this now (while this tab is open)');await confirm('Claim');
   const delay=await ev('[...window.__op.clock.timers.values()].map(t=>t.delay).find(ms=>ms>=9000 && ms<=11000)');
   assert(`${variant}: actual timer uses lease/3 jitter`,delay>=9000 && delay<=11000);
   await until("!!document.querySelector('.op-lease') && document.querySelector('.op-lease').textContent.includes('30 seconds remaining')");
   assert(`${variant}: visible held lease`,await ev("document.querySelector('.op-lease').textContent.includes('30 seconds remaining')"));
   if(variant==='success') {
    const invalid=await ev(`(async()=>{const send=(body,proof,path='q')=>fetch('/v1/boards/brd_test/tasks/t/claims/'+path+'/renew',{method:'POST',body:JSON.stringify(body),headers:{'Yak-Claim-Proof':proof}}).then(async r=>({status:r.status,body:await r.json()}));return Promise.all([send({lease_seconds:30},''),send({lease_seconds:30},'wrong-proof'),send({lease_seconds:1},'fake-secret-proof'),send({lease_seconds:30,extra:true},'fake-secret-proof'),send({lease_seconds:30},'fake-secret-proof','missing')]);})()`);
    assert('malformed fixture renewals reject without empty success',JSON.stringify(invalid.map(x=>x.status))==='[428,403,400,400,404]' && invalid.every(x=>Object.keys(x.body).length>0));
    await ev('window.__op.writes=[]');
   }
   if(variant==='logout'||variant==='unmount') {
    if(variant==='logout')await ev("document.querySelector('#operator-logout').click()");
    else await ev("window.dispatchEvent(new Event('pagehide'))");
    await until('!!document.body && !document.body.dataset.operator');
    await ev('window.__op.clock.advance(60000)');
    assert(`${variant}: no later renew and release precedes revoke`,await ev("!window.__op.writes.some(w=>w.path.endsWith('/renew')) && window.__op.writes.at(-2).path.endsWith('/release') && window.__op.writes.at(-1).method==='DELETE'"));
    continue;
   }
   await ev(`{const base=window.fetch;window.__op.renewAttempts=0;window.fetch=async(url,opts={})=>{if(new URL(url,location.href).pathname.endsWith('/renew')){window.__op.renewAttempts++;${variant==='retry'?"if(window.__op.renewAttempts===1){await base(url,opts);throw new TypeError('synthetic lost response')}" : variant==='deadline'?"await base(url,opts);throw new TypeError('synthetic disconnected')" : typeof variant==='number'?`await base(url,opts);return Response.json({code:'claim_lost'},{status:${variant}})` : ''}}return base(url,opts);};}`);
   if(op17042ClaimCorpus.some(v=>v.status===variant))await ev(`window.__op.renewReply=${JSON.stringify(op17042ClaimCorpus.find(v=>v.status===variant))}`);
   await ev(`window.__op.clock.advance(${delay}-1)`);
   assert(`${variant}: no renewal before scheduled deadline`,await ev('window.__op.renewAttempts===0'));
   await ev('window.__op.clock.advance(1)');
   assert(`${variant}: crossing timer invokes route with lease proof run and key`,await ev("window.__op.writes.filter(w=>w.path.endsWith('/renew')).length===1 && (()=>{const w=window.__op.writes.at(-1);return w.method==='POST' && w.path==='/v1/boards/brd_test/tasks/t/claims/q/renew' && JSON.stringify(w.body)==='{\"lease_seconds\":30}' && w.headers['Yak-Claim-Proof']==='fake-secret-proof' && w.headers['Yak-Run-Id'].startsWith('operator/') && /^[a-f0-9]{32}$/.test(w.headers['Idempotency-Key']);})()"));
   if(variant==='success'||variant==='retry') {
    if(variant==='retry') {
     await ev('window.__op.clock.advance(1000)');
     assert('transport loss retry preserves key and run',await ev("(()=>{const w=window.__op.writes.filter(w=>w.path.endsWith('/renew'));return w.length===2 && JSON.stringify(w[0].headers)===JSON.stringify(w[1].headers)})()"));
    }
    await sleep(1050);
    assert(`${variant}: renewed visible time is conservative`,await ev("/(?:29|30) seconds remaining/.test(document.querySelector('.op-lease').textContent)"));
    const next=await ev('[...window.__op.clock.timers.values()].find(t=>t.delay>=9000 && t.delay<=11000)?.at-window.__op.clock.now()');
    await ev(`window.__op.clock.advance(${next})`);
    assert(`${variant}: subsequent automatic renewal uses new key`,await ev("(()=>{const w=window.__op.writes.filter(w=>w.path.endsWith('/renew'));return w.length>=2 && w.at(-1).headers['Idempotency-Key']!==w.at(-2).headers['Idempotency-Key']})()"));
   } else {
    if(variant==='deadline')await ev('window.__op.clock.advance(30001-window.__op.clock.now())');
    await sleep(1050);
    assert(`${variant}: claim ended visibly`,await ev("document.querySelector('.op-lease').textContent.includes('Your claim ended') && document.querySelector('#operator-identity').textContent.includes('Your claim ended')"));
    const before=await ev('window.__op.renewAttempts');await ev('window.__op.clock.advance(60000)');
    assert(`${variant}: no timer continues after loss`,await ev('window.__op.renewAttempts')===before);
    await ev("document.querySelector('#operator-logout').click()");await until('!!document.body && !document.body.dataset.operator');
    assert(`${variant}: dropped proof cannot be released`,await ev("!window.__op.writes.some(w=>w.path.endsWith('/release'))"));
   }
  } finally {await ev("document.querySelector('#operator-logout')?.click()");}
 }
 assert('no browser exceptions',errors.length===0);
 if(fixtures!==13 || assertions<70)throw new Error(`OP17-032 incomplete coverage: ${fixtures} fixtures, ${assertions} assertions`);
 console.log(`OP17-032 executed ${assertions} assertions across ${fixtures} fresh automatic-renewal fixtures`);
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
   await until("!!document.body && document.body.dataset.operator==='true'"); await open();
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
    await ev("document.querySelector('#operator-logout').click()"); await until("!!document.body && !document.body.dataset.operator");
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
  await until("!!document.body && document.body.dataset.operator==='true'");
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
  await until("!!document.body && document.body.dataset.operator==='true'"); await open();
  await ev(`window.__op.pendingFailurePath=${JSON.stringify(cosmetic?'/v1/boards/brd_test/messages/root/labels':'/v1/boards/brd_test/threads/th/messages')};document.querySelector('textarea[aria-label^=Message]').value='original A body';window.__staleWrites=[...document.querySelectorAll('.op-workspace [data-op-write]')];`);
  if (cosmetic) { await click('Approve and unblock'); await confirm('Approve and unblock'); }
  else await click('Send reply');
  const retry = cosmetic?'Retry label only':'Retry same intent';
  await until(`!!document.querySelector('.op-receipts') && document.querySelector('.op-receipts').textContent.includes(${JSON.stringify(retry)})`);
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
  await ev("document.querySelector('#operator-logout').click()"); await until("!!document.querySelector('#login') && !document.querySelector('#login').hidden");
  const ended=await count(); await ev("window.__retry.dispatchEvent(new Event('click'))"); await sleep(100);
  assert(`${variant}: ended session cannot replay receipt`,await count()===ended);
  await ev("document.querySelector('#operator-login').checked=true;document.querySelector('#token').value='synthetic-replacement';window.__op.revoked=false;document.querySelector('#login-form').requestSubmit()");
  await until("!!document.body && document.body.dataset.operator==='true'");
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
   await until("!!document.body && document.body.dataset.operator==='true'");
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
    await until(variant === 'failed' ? "!!document.querySelector('.op-workspace [role=status]') && document.querySelector('.op-workspace [role=status]').textContent.includes('synthetic_discovery_failure')" : "!!document.querySelector('.op-workspace [role=status]') && document.querySelector('.op-workspace [role=status]').textContent.includes('No readable channel')");
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
   await until("!!document.body && !document.body.dataset.operator");
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
   await until("!!document.body && document.body.dataset.operator==='true'");
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
    await until("!!document.body && document.body.dataset.operator==='true'");
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
     await until("!!document.querySelector('.op-receipts') && document.querySelector('.op-receipts').textContent.includes('Saved.')");
     assert(`${kind}/${variant}: completion preserves other board`, await ev("document.querySelector('textarea[aria-label*=brd_other]').value==='other board draft'"));
     await click('Close');
     await ev(`window.dispatchEvent(new CustomEvent('yak-operator-open',{detail:{board:'brd_test',ch:'ops',${kind === 'reply' ? "t:'th'" : ''}}}))`);
     await until(`!!document.querySelector(${JSON.stringify(text)})`);
    } else {
     await ev('window.__op.resolveDraft()');
     await until(variant === 'network' ? "!!document.querySelector('.op-receipts') && document.querySelector('.op-receipts').textContent.includes('Retry same intent')" : variant === 'conflict' ? "!!document.querySelector('.op-receipts') && document.querySelector('.op-receipts').textContent.includes('No retry was sent')" : "!!document.querySelector('.op-receipts') && document.querySelector('.op-receipts').textContent.includes('Saved.') && !document.querySelector('.op-pending')?.textContent.includes('Sending')");
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
    await until("!!document.body && !document.body.dataset.operator");
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
   await until("!!document.body && document.body.dataset.operator==='true'"); await open();
   await ev(`window.__op.labelFault={status:${status==='approved'?412:status},current:${JSON.stringify(status==='approved'?'approved':'changes_requested')}};document.querySelector('textarea[aria-label="Gate note t"]').value='original changes';`);
   const action=kind==='approve'?'Approve and unblock':'Request changes';
   await click(action); await confirm(action);
   if(status==='approved') {
    await until("!!document.querySelector('.op-receipts') && document.querySelector('.op-receipts').textContent.includes('Saved.')");
    assert(`${name}: approved conflict finishes without retry`,await count()===2 && await ev("!document.querySelector('.op-receipts button')"));
    continue;
   }
   await until("!!document.querySelector('.op-receipts') && document.querySelector('.op-receipts').textContent.includes('Retry label only')");
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
   await until("!!document.querySelector('.op-receipts') && document.querySelector('.op-receipts').textContent.includes('Retry label only')");
   assert(`${name}: uncertain retry retains receipt and global lock`,await ev("window.__receipt===document.querySelector('.op-pending') && window.__receipt.textContent.includes('Retry label only')") && !await ev(replyEnabled));
   await ev('window.__op.labelFault=null'); await click('Retry label only');
   await until("!document.querySelector('.op-receipts button')");
   assert(`${name}: resolution permits unrelated writes`,await ev(replyEnabled));
   const writes=await ev('window.__op.writes');
   assert(`${name}: first action never repeated`,writes.filter(w=>!w.path.endsWith('/labels')&&w.body?.text!=='unrelated message').length===1);
   assert(`${name}: every label retry is original request`,writes.filter(w=>w.path.endsWith('/labels')).every(w=>JSON.stringify(w)===JSON.stringify(original)));
  } finally {
   await ev("window.__op.deferLabel=false;window.__op.resolveLabel?.();document.querySelector('#operator-logout')?.click()");
   await until('!!document.body && !document.body.dataset.operator');
  }
 }
 assert('all fresh variants executed',fixtures===13 && assertions>=100);
 assert('no browser exceptions',errors.length===0);
 console.log(`OP17-019 executed ${assertions} assertions across ${fixtures} fresh cosmetic receipt fixtures`);
}
async function op17031ConditionalLabelsCase() {
 let assertions=0, fixtures=0;
 const assert=(name,ok)=>{assertions++;check(`OP17-031: ${name}`,ok);};
 const set=(expect,value='approved')=>({op:'set',key:'gate',value,expect});
 const unset=expect=>({op:'unset',key:'gate',expect});
 const variants=[
  ['set stale value','changes_requested',[set({value:'pending'})],412,'changes_requested'],
  ['unset stale value','changes_requested',[unset({value:'pending'})],412,'changes_requested'],
  ['unset matching value','pending',[unset({value:'pending'})],200,null],
  ['set matching value','pending',[set({value:'pending'})],200,'approved'],
  ['absent rejects existing','pending',[set({absent:true})],412,'pending'],
  ['absent rejects empty','',[set({absent:true})],412,''],
  ['absent matches missing',null,[set({absent:true})],200,'approved'],
  ['unset absent missing',null,[unset({absent:true})],200,null],
  ['value rejects missing',null,[unset({value:'pending'})],412,null],
  ['present rejects missing',null,[set({present:true})],412,null],
  ['present matches existing','pending',[set({present:true})],200,'approved'],
  ['atomic mismatch','changes_requested',[{op:'set',key:'priority',value:'high'},set({value:'pending'})],412,'changes_requested'],
  ['expectations read before mutation','pending',[set({value:'pending'},'changes_requested'),set({value:'pending'})],200,'approved'],
  ['invalid opcode','pending',[{op:'remove',key:'gate'}],400,'pending'],
  ['set requires value','pending',[{op:'set',key:'gate'}],400,'pending'],
  ['unset rejects value','pending',[{op:'unset',key:'gate',value:'approved'}],400,'pending'],
  ['invalid expectation','pending',[set({absent:'true'})],400,'pending'],
  ['validation is atomic','pending',[{op:'set',key:'priority',value:'high'},{op:'set',key:'gate'}],400,'pending'],
 ];
 for(const [name,current,operations,status,after] of variants) {
  await freshIdentityFixture(); fixtures++;
  try {
   const result=await ev(`(async()=>{const T=window.__op;${current===null?'delete T.root.labels.gate':`T.root.labels.gate=${JSON.stringify(current)}`};const r=await fetch('/v1/boards/brd_test/messages/root/labels',{method:'PATCH',body:JSON.stringify({operations:${JSON.stringify(operations)}})});return {status:r.status,body:await r.json(),labels:T.root.labels,writes:T.writes};})()`);
   assert(`${name}: status ${status}`,result.status===status);
   assert(`${name}: resulting labels`,(after===null?!Object.hasOwn(result.labels,'gate'):result.labels.gate===after) && !Object.hasOwn(result.labels,'priority'));
   assert(`${name}: captures exactly one request`,result.writes.length===1 && JSON.stringify(result.writes[0].body.operations)===JSON.stringify(operations));
   if(status===412) assert(`${name}: current key/value`,result.body.code==='revision_conflict' && result.body.key==='gate' && result.body.current===current);
  } finally { await cmd('Page.navigate',{url:'about:blank'}); }
 }
 for(const current of ['approved','changes_requested','pending']) {
  await freshIdentityFixture(`window.__op.root.labels.gate=${JSON.stringify(current)};window.__op.failLabel=${current==='pending'?1:0};`); fixtures++;
  try {
   await ev("document.querySelector('#operator-login').checked=true;document.querySelector('#token').value='synthetic-conditional-token';document.querySelector('#login-form').requestSubmit()");
   await until("!!document.body && document.body.dataset.operator==='true'"); await open();
   await click('Approve and unblock'); await confirm('Approve and unblock');
   assert(`${current}: unblock then conditional label`,await ev("window.__op.writes.length===2 && window.__op.writes[0].path.endsWith('/unblock') && window.__op.writes[1].body.operations[0].expect.value==='pending' && window.__op.task.state==='ready'"));
   if(current==='approved') {
    assert('approved conflict is done',await ev("document.querySelector('.op-receipts').textContent.includes('Saved.') && !document.querySelector('.op-receipts button') && window.__op.root.labels.gate==='approved'"));
   } else {
    assert(`${current}: partial receipt`,await ev("document.querySelector('.op-receipts').textContent.includes('Approved; label not updated') && !![...document.querySelectorAll('.op-receipts button')].find(b=>b.textContent==='Retry label only')"));
    if(await ev("!![...document.querySelectorAll('.op-receipts button')].find(b=>b.textContent==='Retry label only')")) {
     await click('Retry label only');
     assert(`${current}: retry sends only original label`,await ev("window.__op.writes.length===3 && JSON.stringify(window.__op.writes[1])===JSON.stringify(window.__op.writes[2])"));
     assert(`${current}: retry outcome`,await ev(current==='pending'?"window.__op.root.labels.gate==='approved' && !document.querySelector('.op-receipts button')":"window.__op.root.labels.gate==='changes_requested' && document.querySelector('.op-receipts').textContent.includes('Approved; label not updated') && !!document.querySelector('.op-receipts button')"));
    }
   }
  } finally {
   await ev("document.querySelector('#operator-logout')?.click()"); await until('!!document.body && !document.body.dataset.operator');
  }
 }
 assert('all variants execute nonzero assertions',fixtures===21 && assertions>=65);
 assert('no browser exceptions',errors.length===0);
 console.log(`OP17-031 executed ${assertions} assertions across ${fixtures} fresh fixtures`);
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
   await until("!!document.body && document.body.dataset.operator==='true'"); await open();
   await ev(`document.querySelector('input[aria-label="Label key root"]').value=${JSON.stringify(`  ${c.key.toUpperCase()}  `)};document.querySelector('input[aria-label="Label value root"]').value='low'`);
   await click(c.action);
   if (c.refused) {
    assert(`${c.name}: no write`, await count()===0);
    assert(`${c.name}: key guard explains refusal`, await ev("document.querySelector('.op-workspace').textContent.includes('Choose an ordinary label key.')"));
    continue;
   }
   await until("!!document.querySelector('.op-receipts') && document.querySelector('.op-receipts').textContent.includes('Saved.')");
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
   await until('!!document.body && !document.body.dataset.operator');
  }
 }
 assert('all nine fresh fixtures executed nonzero assertions', fixtures===9 && assertions===30);
 assert('no browser exceptions', errors.length===0);
 console.log(`OP17-035 executed ${assertions} assertions across ${fixtures} fresh label fixtures`);
}
async function gateEvidenceCase() {
 let assertions = 0;
 const assert = (name, ok, detail = '') => { assertions++; check(`OP17-020: ${name}`, ok, detail); };
 const variants = op17020Cases();
 const gate = variants.find(c => c.approved);
 for (const type of ['task.blocked', 'task.reopened']) {
  const reset = {...gate.candidate, id:'reset', type};
  variants.push({...gate, name:`latest ${type}`, events:[...gate.events, reset], approved:false});
  variants.push({...gate, name:`approval after ${type}`, events:[...gate.events, reset, {...gate.candidate, id:'new-approval'}]});
 }
 for (const c of variants) {
  await freshIdentityFixture(`window.__op.task.state='ready';window.__op.events=${JSON.stringify(c.events)};`);
  try {
   await ev("document.querySelector('#operator-login').checked=true;document.querySelector('#token').value='synthetic-gate-token';document.querySelector('#login-form').requestSubmit()");
   await until("!!document.body && document.body.dataset.operator==='true'");
   await ev("location.hash='#/b/brd_test?v=activity'");
   await until(`[...document.querySelectorAll('#feed .type')].some(el=>el.textContent===${JSON.stringify(c.candidate.type)})`);
   await open();
   await until("!!document.querySelector('.op-workspace') && document.querySelector('.op-workspace').textContent.includes('Task event history')");
   const workspace = await ev("document.querySelector('.op-workspace').textContent");
   assert(`${c.name}: workspace approval`, workspace.includes('Approved by ')===c.approved, workspace);
   if (c.candidate.type==='task.unblocked') {
    const eventApproval = gate.name===c.name || c.name.startsWith('latest ') || c.name.startsWith('approval after ');
    const feed = await ev("[...document.querySelectorAll('#feed li')].find(li=>li.querySelector('.type')?.textContent==='task.unblocked')?.querySelector('.snippet')?.textContent");
    assert(`${c.name}: feed gate evidence`, !!feed && feed.includes("not a person's approval")===!eventApproval, feed);
    assert(`${c.name}: timeline gate evidence`, workspace.includes("not a person's approval")===!eventApproval, workspace);
   } else {
    assert(`${c.name}: completion never shows approval in feed`, await ev("![...document.querySelectorAll('#feed .snippet')].some(el=>el.textContent.includes('Approved by '))"));
   }
   assert(`${c.name}: rendering sends no writes`, await count()===0);
  } finally {
   await ev("document.querySelector('#operator-logout')?.click()");
   await until('!!document.body && !document.body.dataset.operator');
   assert(`${c.name}: cleanup revokes disposable credential only`, await ev("window.__op.writes.length===1 && window.__op.writes[0].path==='/v1/credentials/cred_test'"));
  }
 }
 assert('all thirteen fixtures execute', variants.length===13 && assertions===64);
 assert('no browser exceptions', errors.length===0, errors.join(' | '));
 console.log(`OP17-020 executed ${assertions} assertions across ${variants.length} fresh fixtures`);
}
async function briefGenerationCase() {
 let assertions = 0;
 const assert = (name, ok, detail = '') => { assertions++; check(`OP17-036: ${name}`, ok, detail); };
 const variants = op17036Cases();
 for (const c of variants) {
  await freshIdentityFixture(`window.__op.task.state='ready';window.__op.events=${JSON.stringify(c.events)};`);
  try {
   await ev("document.querySelector('#operator-login').checked=true;document.querySelector('#token').value='synthetic-generation-token';document.querySelector('#login-form').requestSubmit()");
   await until("!!document.body && document.body.dataset.operator==='true'");
   await open();
   await until("!!document.querySelector('.op-workspace') && document.querySelector('.op-workspace').textContent.includes('Task event history')");
   const workspace = await ev("document.querySelector('.op-workspace').textContent");
   assert(`${c.name}: current approval`, workspace.includes('Approved by ')===!!c.approval, workspace);
   if (!c.approval) assert(`${c.name}: explicitly not approved`, workspace.includes('not approved'), workspace);
   assert(`${c.name}: rendering sends no writes`, await count()===0);
  } finally {
   await ev("document.querySelector('#operator-logout')?.click()");
   await until('!!document.body && !document.body.dataset.operator');
   assert(`${c.name}: cleanup revokes disposable credential only`, await ev("window.__op.writes.length===1 && window.__op.writes[0].path==='/v1/credentials/cred_test'"));
  }
 }
 assert('all fourteen fresh fixtures execute assertions', variants.length===14 && assertions===49);
 assert('no browser exceptions', errors.length===0, errors.join(' | '));
 console.log(`OP17-036 executed ${assertions} assertions across ${variants.length} fresh fixtures`);
}
async function op17009ActorPaginationCase() {
 let assertions = 0, fixtures = 0;
 const assert = (name, ok, detail = '') => { assertions++; check(`OP17-009: ${name}`, ok, detail); };
 for (const variant of ['page two human', 'empty terminal', 'unknown', 'abort', 'removed']) {
  await freshIdentityFixture(`window.__op.enableFeedPagination(${JSON.stringify(variant)});`);
  fixtures++;
  try {
   await ev(`(async()=>{
    const {createFeed}=await import('/feed.js'); const {h}=await import('/skin-kit.js');
    window.__op.feed=createFeed({api:window.__op.feedApi,h:(...args)=>{window.__op.feedDraws++;return h(...args)},
      time:at=>h('time',{datetime:at},at),href:()=> '#',onTask:()=>{},onUnauthorized:()=>{throw new Error('unexpected authorization')}});
    window.__op.feed.setVisible(true);window.__op.feed.start();
   })()`);
   await until("window.__op.actorReads.length===1 && !!window.__op.removeFeedBoard && document.querySelectorAll('#feed .actor').length===2");
   if (variant==='abort' || variant==='removed') {
    if (variant==='abort') await ev('window.__op.feed.stop()');
    else { await ev('window.__op.removeFeedBoard()'); await until("!!document.querySelector('#feed-status') && document.querySelector('#feed-status').textContent.includes('0 boards')"); }
    await ev("window.__op.drawsBefore=window.__op.feedDraws;window.__op.actorReads[0].resolve({actors:[{id:'a',handle:'alice',kind:'human'}],next_page_token:null})");
    await sleep(100);
    assert(`${variant}: late page never renders`,await ev('window.__op.feedDraws===window.__op.drawsBefore'));
    // Retained feed entries reference the removed record; rendering them detects stale map commits.
    await ev('window.__op.feed.setVisible(true)');
    assert(`${variant}: late actor map never commits`,await ev("!document.querySelector('#feed').textContent.includes('@alice')"));
    assert(`${variant}: no extra page request`,await ev('window.__op.actorReads.length===1'));
   } else {
    await ev("window.__op.actorReads[0].resolve({actors:[{id:'bot',handle:'bot',kind:'agent'}],next_page_token:'opaque +/page2'})");
    await sleep(100);
    assert(`${variant}: follows opaque continuation`,await ev("window.__op.actorReads.length===2 && window.__op.actorReads[1].params.page_token==='opaque +/page2'"));
    assert(`${variant}: first page does not publish partial actors`,await ev("![...document.querySelectorAll('#feed .actor')].some(el=>el.textContent.includes('@bot'))"));
    // Continue even on the red implementation so every variant executes its assertions.
    await ev(`window.__op.actorReads[1]?.resolve({actors:${variant==='empty terminal' ? '[]' : "[{id:'a',handle:'alice',kind:'human'}]"},next_page_token:null})`);
    await sleep(100);
    const text=await ev("[...document.querySelectorAll('#feed li')].find(li=>li.querySelector('.actor')?.textContent.startsWith("+JSON.stringify(variant==='unknown'?'missing-hu':'@alice')+"))?.textContent || ''");
    if (variant==='page two human') {
     assert('page two human: handle and kind',text.includes('@alice (human)'),text);
     assert('page two human: direct gate approval',!!text && !text.includes("not a person's approval"),text);
     assert('page two human: credential time and reason', ['synthetic-page-credential','2026-11-20T00:00:00Z','page two approval'].every(v=>text.includes(v)),text);
    } else if (variant==='unknown') {
     assert('unknown: short ID stays unknown',!!text && !text.includes('@alice') && !text.includes('(human)'),text);
     assert('unknown: never inferred human approval',text.includes("not a person's approval"),text);
    } else assert('empty terminal: absent human remains unknown',await ev("[...document.querySelectorAll('#feed .actor')].some(el=>el.textContent==='a') && document.querySelector('#feed').textContent.includes(\"not a person's approval\")"));
    assert(`${variant}: all requests use same active signal and max page size`,await ev('window.__op.actorReads.length===2 && window.__op.actorReads.every(r=>r.signal===window.__op.feedReads[0].signal && !r.signal.aborted && r.params.limit===200)'));
    assert(`${variant}: terminal page stops pagination`,await ev('window.__op.actorReads.length===2'));
    assert(`${variant}: first page retained in complete map`,await ev("[...document.querySelectorAll('#feed .actor')].some(el=>el.textContent==='@bot (agent)')"));
   }
   assert(`${variant}: rendering sends no writes`,await count()===0);
  } finally {
   await ev('window.__op.feed?.stop()');
   assert(`${variant}: cleanup aborts disposable reads`,await ev('window.__op.feedReads[0].signal.aborted && window.__op.writes.length===0'));
  }
 }
 assert('all five fresh variants execute assertions',fixtures===5 && assertions>=35);
 assert('no browser exceptions',errors.length===0,errors.join(' | '));
 console.log(`OP17-009 executed ${assertions} assertions across ${fixtures} fresh actor pagination fixtures`);
}
async function op17021ActorRetryCase() {
 let assertions = 0, fixtures = 0;
 const assert = (name, ok) => { assertions++; check(`OP17-021: ${name}`, ok); };
 for (const variant of ['recovery', 'persistent', 'restart', 'unauthorized', 'stale unauthorized']) {
  await freshIdentityFixture('window.__op.enableFeedRetry();'); fixtures++;
  try {
   await ev(`(async()=>{
    const {createFeed}=await import('/feed.js'); const {h}=await import('/skin-kit.js');
    window.__op.feed=createFeed({api:window.__op.feedApi,h:(...args)=>{window.__op.feedDraws++;return h(...args)},
      time:at=>h('time',{datetime:at},at),href:()=> '#',onTask:()=>{},onUnauthorized:()=>{window.__op.authFailures++}});
    window.__op.feed.setVisible(true);window.__op.feed.start();
   })()`);
   await until("window.__op.actorReads.length===1 && !!window.__op.removeFeedBoard && document.querySelectorAll('#feed .actor').length===2");
   await ev("window.__op.actorReads[0].reject(Object.assign(new Error('synthetic lookup failure'),{status:503}))");
   await sleep(50);
   assert(`${variant}: failure preserves unknown identity`,await ev("!document.querySelector('#feed').textContent.includes('@alice') && document.querySelector('#feed').textContent.includes(\"not a person's approval\")"));
   await ev('window.__op.relistFeed()'); await sleep(50);
   assert(`${variant}: next relist retries`,await ev('window.__op.actorReads.length===2'));
   await ev('Promise.all([window.__op.relistFeed(),window.__op.relistFeed(),window.__op.relistFeed()])');
   assert(`${variant}: delayed load has one traversal`,await ev('window.__op.actorReads.length===2'));
   if (variant==='restart' || variant==='stale unauthorized') {
    await ev('window.__op.feed.stop();window.__op.feed.start()'); await sleep(50);
    assert(`${variant}: restart creates independent traversal`,await ev('window.__op.actorReads.length===3 && window.__op.actorReads[0].signal.aborted'));
    await ev(`window.__op.drawsBefore=window.__op.feedDraws;window.__op.actorReads[1]?.${variant==='restart' ? "resolve({actors:[{id:'a',handle:'stale',kind:'human'}],next_page_token:'stale-page'})" : "reject(Object.assign(new Error('stale unauthorized'),{status:401}))"}`);
    await sleep(50);
    assert(`${variant}: old response cannot publish, paginate or log out`,await ev("window.__op.feedDraws===window.__op.drawsBefore && window.__op.actorReads.length===3 && window.__op.authFailures===0 && !document.querySelector('#feed').textContent.includes('@stale')"));
    await ev("window.__op.actorReads.at(-1).resolve({actors:[{id:'a',handle:'alice',kind:'human'}],next_page_token:null})");
   } else if (variant==='unauthorized') {
    await ev("window.__op.actorReads[1]?.reject(Object.assign(new Error('unauthorized'),{status:401}))"); await sleep(50);
    assert('unauthorized: uses existing unauthorized path once',await ev('window.__op.authFailures===1'));
   } else if (variant==='persistent') {
    await ev("window.__op.actorReads[1]?.reject(new TypeError('offline'))"); await sleep(50);
    await ev('window.__op.relistFeed()'); await sleep(50);
    assert('persistent: later relist retries again',await ev('window.__op.actorReads.length===3'));
    await ev("window.__op.actorReads[2]?.reject(Object.assign(new Error('still offline'),{status:503}))"); await sleep(50);
    assert('persistent: never invents human identity or approval',await ev("!document.querySelector('#feed').textContent.includes('(human)') && document.querySelector('#feed').textContent.includes(\"not a person's approval\")"));
   } else {
    await ev("window.__op.actorReads[1]?.resolve({actors:[{id:'bot',handle:'bot',kind:'agent'}],next_page_token:'retry-page-two'})"); await sleep(50);
    assert('recovery: retry uses paginated loader',await ev("window.__op.actorReads.length===3 && window.__op.actorReads[2].params.page_token==='retry-page-two'"));
    await ev("window.__op.actorReads[2]?.resolve({actors:[{id:'a',handle:'alice',kind:'human'}],next_page_token:null})"); await sleep(50);
    assert('recovery: past event redraws human and correct gate interpretation',await ev("[...document.querySelectorAll('#feed li')].some(li=>li.textContent.includes('@alice (human)') && !li.textContent.includes(\"not a person's approval\") && li.textContent.includes('synthetic-page-credential'))"));
    await ev('window.__op.relistFeed()');
    assert('recovery: complete directory does not reload',await ev('window.__op.actorReads.length===3'));
   }
   if (!variant.includes('restart') && variant!=='stale unauthorized') {
    assert(`${variant}: event loop and cursor preserved`,await ev("window.__op.feedReads.filter(r=>r.path.endsWith('/events')).length===2 && window.__op.feedReads.filter(r=>r.path.endsWith('/events'))[1].params.cursor==='feed-cursor' && window.__op.actorReads.every(r=>r.signal===window.__op.feedReads[0].signal)"));
   }
   assert(`${variant}: no writes`,await count()===0);
  } finally {
   await ev('window.__op.feed?.stop()');
   assert(`${variant}: cleanup aborts all traversals`,await ev('window.__op.actorReads.every(r=>r.signal.aborted) && window.__op.writes.length===0'));
  }
 }
 assert('all five fixtures executed',fixtures===5 && assertions>=35);
 assert('no browser exceptions',errors.length===0);
 console.log(`OP17-021 executed ${assertions} assertions across ${fixtures} fresh actor retry fixtures`);
}
async function op17010StoreActorsCase() {
 let assertions = 0, fixtures = 0;
 const assert = (name, ok, detail = '') => { assertions++; check(`OP17-010: ${name}`,ok,detail); };
 for (const skin of ['terminal','irc']) {
  await freshIdentityFixture("window.__op.scope=['read'];window.__op.enableStorePagination();sessionStorage.setItem('yak-viewer-token','synthetic-pagination-token');"); fixtures++;
  try {
   await until("!!document.body && document.body.dataset.view==='boards'");
   await ev(`location.hash=${JSON.stringify(skin==='irc'?'#/irc?b=brd_test':'#/b/brd_test?ch=ops&t=th')}`);
   await until(skin==='irc' ? "!!document.querySelector('.i-nick')" : "!!document.querySelector('.t-who')");
   await sleep(150);
   const rows = await ev(`Array.from(document.querySelectorAll(${JSON.stringify(skin==='irc'?'.i-nick':'.t-who')}),e=>e.textContent)`);
   assert(`${skin}: page two human message shows handle and kind`,rows.some(t=>t.includes('pageHuman') && t.includes('human')),JSON.stringify(rows));
   assert(`${skin}: delegated message shows both handles and kinds`,rows.some(t=>t.includes('bot') && t.includes('agent') && t.includes('pagePrincipal') && t.includes('human')),JSON.stringify(rows));
   if (skin==='terminal') {
    await ev("location.hash='#/'"); await until("!!document.querySelector('#feed') && document.querySelector('#feed').textContent.includes('page human event')");
   }
   const text = await ev(`document.querySelector(${JSON.stringify(skin==='irc'?'#skin':'#feed')}).textContent`);
   assert(`${skin}: human task event resolves page two kind`,text.includes('pageHuman (human)'),text);
   assert(`${skin}: delegated task event resolves both handles`,text.includes('bot (agent) for '+(skin==='terminal'?'@':'')+'pagePrincipal'),text);
   assert(`${skin}: follows opaque actor token with page size`,await ev("window.__op.storeActorReads.some(r=>r.token==='opaque +/actors2' && r.limit==='200')"));
   assert(`${skin}: read-only rendering sends no operator writes`,await count()===0);
  } finally { await ev("document.querySelector('#forget').click()"); await until("!!document.querySelector('#login') && !document.querySelector('#login').hidden"); }
 }
 for (const variant of ['later failure','later unauthorized','replacement abort']) {
  await freshIdentityFixture('window.__op.enableStoreProbe();'); fixtures++;
  try {
   await ev(`(async()=>{
    const {createStore}=await import('/skin-kit.js');
    window.__op.probe=createStore({api:window.__op.storeApi,boardId:'brd_test',
      onChange:()=>window.__op.storePublications.push([...window.__op.probe.S.actors.keys()]),
      onStatus:s=>window.__op.storeStatuses.push(s),onUnauthorized:()=>window.__op.storeAuth++});
   })()`);
   await until("window.__op.storeReads.some(r=>r.path.endsWith('/actors'))");
   await ev("window.__op.storeReads.find(r=>r.path.endsWith('/actors')).resolve({actors:[{id:'old',handle:'old',kind:'human'}],next_page_token:null})");
   await until("window.__op.probe.S.cursor==='probe-cursor' && !!window.__op.storeResync");
   await ev('window.__op.storeResync()');
   await until("window.__op.storeReads.filter(r=>r.path.endsWith('/actors')).length===2");
   await ev("window.__op.storeReads.filter(r=>r.path.endsWith('/actors'))[1].resolve({actors:[{id:'partial',handle:'partial',kind:'human'}],next_page_token:'opaque +/later'})");
   await sleep(100);
   assert(`${variant}: replacement waits for later actor page`,await ev("window.__op.storeReads.filter(r=>r.path.endsWith('/actors')).length===3"));
   assert(`${variant}: incomplete directory does not replace accepted actors`,await ev("window.__op.probe.S.actors.has('old') && !window.__op.probe.S.actors.has('partial')"));
   if (variant==='replacement abort') {
    await ev("window.__op.probe.stop();window.__op.storeReads.filter(r=>r.path.endsWith('/actors'))[2]?.resolve({actors:[{id:'late',handle:'late',kind:'human'}],next_page_token:null})");
   } else {
    await ev(`window.__op.storeReads.filter(r=>r.path.endsWith('/actors'))[2]?.reject(Object.assign(new Error('synthetic later-page failure'),{status:${variant==='later unauthorized'?401:503}}))`);
   }
   await sleep(100);
   assert(`${variant}: late failure or abort cannot publish actors`,await ev("window.__op.probe.S.actors.has('old') && !window.__op.probe.S.actors.has('partial') && !window.__op.probe.S.actors.has('late') && window.__op.storePublications.every(ids=>ids.length===1 && ids[0]==='old')"));
   assert(`${variant}: no replacement snapshot starts`,await ev("window.__op.writes.filter(r=>r.method==='POST').length===1"));
   assert(`${variant}: preserves authorization semantics`,await ev(`window.__op.storeAuth===${variant==='later unauthorized'?1:0}`));
   if (variant==='later failure') assert('later failure: existing reconnect status',await ev("window.__op.storeStatuses.at(-1)==='reconnecting'"));
  } finally {
   await ev('window.__op.probe.stop()');
   assert(`${variant}: cleanup aborts all reads`,await ev("window.__op.storeReads.filter(r=>r.path.endsWith('/actors')).every(r=>r.signal.aborted)"));
  }
 }
 assert('all five fresh fixtures execute assertions',fixtures===5 && assertions>=30);
 assert('no browser exceptions',errors.length===0,errors.join(' | '));
 console.log(`OP17-010 executed ${assertions} assertions across ${fixtures} fresh fixtures`);
}
async function op17013ProjectionCase() {
 let assertions=0, fixtures=0;
 const assert=(name,ok)=>{assertions++;check(`OP17-013: ${name}`,ok)};
 for (const kind of ['thread','channels','unknown']) for (const variant of ['reverse','snapshot','dispose',...(kind==='channels'?[]:['payload','message','revision'])]) {
  await freshIdentityFixture('window.__op.enableProjectionProbe();'); fixtures++;
  const name=`${kind} ${variant}`;
  try {
   await ev(`(async()=>{
    const T=window.__op, {createStore}=await import('/skin-kit.js');
    T.probe=createStore({api:T.projectionApi,boardId:'brd_test',onChange:()=>T.projectionChanges++});
    T.projectionEvent=(id)=>({id,type:${JSON.stringify(kind==='channels'?'channel.created':kind==='unknown'?'message.created':'thread.view_invalidated')},thread_id:${JSON.stringify(kind==='unknown'?'unknown':'th')},payload:${kind==='unknown'?"{message:{id,thread_id:'unknown',seq:1,created_at:'2026-11-20T00:00:00Z'}}":"{}"}});
    T.projectionReply=(label,revision='9007199254740993')=>${kind==='channels'?"({channels:[{path:label}]})":`({thread:{...T.projectionThread,id:${JSON.stringify(kind==='unknown'?'unknown':'th')},labels:{gate:label},message_count:1,revision}})`};
    T.projectionSend=(events)=>T.projectionPolls.at(-1).resolve({events,next_cursor:'projection-cursor'});
   })()`);
   await until('window.__op.projectionPolls.length===1');
   await ev("window.__op.deferProjection=true;window.__op.projectionSend([window.__op.projectionEvent('first')])");
   await until('window.__op.projectionReads.length===1 && window.__op.projectionPolls.length===2');
   if (variant==='reverse') {
    await ev("window.__op.projectionSend([window.__op.projectionEvent('second')])");
    await sleep(50);
    assert(`${name}: repeated invalidation coalesces in flight`,await ev('window.__op.projectionReads.length===1'));
    await ev("window.__op.projectionReads[0].resolve(window.__op.projectionReply('old'))");
    await until('window.__op.projectionReads.length===2');
    await ev("window.__op.projectionReads[1].resolve(window.__op.projectionReply('new'))");await sleep(50);
    assert(`${name}: newer response publishes`,await ev(kind==='channels'?"window.__op.probe.S.channels[0].path==='new'":"window.__op.probe.S.threads.get(window.__op.projectionReply('x').thread.id).labels.gate==='new'"));
   } else if (variant==='snapshot') {
    await ev("window.__op.deferProjection=false;window.__op.projectionThread.labels={gate:'replacement'};window.__op.projectionPolls.at(-1).reject(Object.assign(new Error('resync'),{status:409,problem:{code:'resync_required'}}))");
    await until('window.__op.projectionSnapshots===2 && window.__op.projectionPolls.length===3');
    assert(`${name}: generation aborts old read`,await ev('window.__op.projectionReads[0].signal.aborted'));
   } else if (variant==='dispose') {
    await ev('window.__op.probe.stop()');
    assert(`${name}: disposal aborts old read`,await ev('window.__op.projectionReads[0].signal.aborted'));
   } else {
    await ev(`window.__op.projectionSend([{id:'payload',type:'message.created',payload:${variant==='message'?`{message:{id:'new-message',thread_id:${JSON.stringify(kind==='unknown'?'unknown':'th')},seq:9,created_at:'2026-11-20T00:00:00Z'},thread:window.__op.projectionReply('payload').thread}`:"{thread:window.__op.projectionReply('payload').thread}"}}])`);
    await until('window.__op.probe.S.seen.has("payload")');
    if (variant==='message') {
     // A subsequent message-only update must also invalidate a read of a known thread.
     await ev("window.__op.projectionSend([{id:'third',type:'thread.view_invalidated',thread_id:window.__op.projectionReply('x').thread.id}])");
     await ev("window.__op.projectionReads[0].resolve(window.__op.projectionReply('old'))");
     await until('window.__op.projectionReads.length===2');
     await ev("window.__op.projectionSend([{id:'message-only',type:'message.created',payload:{message:{id:'latest',thread_id:window.__op.projectionReply('x').thread.id,seq:10,created_at:'2026-11-20T00:00:01Z'}}}])");
     await until('window.__op.probe.S.seen.has("message-only")');
    }
    if (variant==='revision') {
     await ev("window.__op.projectionSend([{id:'revision-read',type:'thread.view_invalidated',thread_id:window.__op.projectionReply('x').thread.id}])");
     await ev("window.__op.projectionReads[0].resolve(window.__op.projectionReply('old'))");
     await until('window.__op.projectionReads.length===2');
     await ev("window.__op.projectionReads[1].resolve(window.__op.projectionReply('lower','9007199254740992'))");await sleep(50);
     assert(`${name}: latest read rejects exact lower revision`,await ev("window.__op.probe.S.threads.get(window.__op.projectionReply('x').thread.id).labels.gate==='payload'"));
    }
   }
   await ev('window.__op.beforeProjection=JSON.stringify({threads:[...window.__op.probe.S.threads],channels:window.__op.probe.S.channels});window.__op.beforeChanges=window.__op.projectionChanges');
   await ev("window.__op.projectionReads.forEach(r=>r.resolve(window.__op.projectionReply('old','9007199254740993')))");await sleep(80);
   assert(`${name}: old read cannot mutate state`,await ev('JSON.stringify({threads:[...window.__op.probe.S.threads],channels:window.__op.probe.S.channels})===window.__op.beforeProjection'));
   assert(`${name}: old read cannot notify`,await ev('window.__op.projectionChanges===window.__op.beforeChanges'));
   assert(`${name}: only snapshot lifecycle writes`,await ev("window.__op.writes.length===window.__op.projectionSnapshots*2 && window.__op.writes.every(w=>w.path.includes('/snapshots'))"));
  } finally { await ev('window.__op.probe?.stop()'); }
 }
 assert('all fresh variants executed',fixtures===15 && assertions>=45);
 assert('no browser exceptions',errors.length===0);
 console.log(`OP17-013 executed ${assertions} assertions across ${fixtures} fresh projection fixtures`);
}
async function op17024ProjectionRetryCase() {
 let assertions=0, fixtures=0;
 const assert=(name,ok)=>{assertions++;check(`OP17-024: ${name}`,ok)};
 for (const kind of ['thread','channels','unknown']) for (const variant of ['recover','backoff','snapshot','dispose','unauthorized','coalesce']) {
  const name=`${kind} ${variant}`;
  await freshIdentityFixture('window.__op.enableProjectionRetry();'); fixtures++;
  try {
   await ev(`(async()=>{
    const T=window.__op, {createStore,gateHint}=await import('/skin-kit.js');
    T.probe=createStore({api:T.projectionApi,boardId:'brd_test',onStatus:s=>T.projectionStatuses.push(s),onUnauthorized:()=>T.projectionUnauthorized++,onChange:()=>{
      T.projectionChanges++;
      document.querySelector('#me').textContent=gateHint({state:'blocked',direct_action_required:true},T.probe.labels({thread_id:'th'}))||'';
    }});
    T.retryEvent=id=>({id,type:${JSON.stringify(kind==='channels'?'channel.created':kind==='unknown'?'message.created':'thread.view_invalidated')},thread_id:'th',payload:${kind==='unknown'?"{message:{id,thread_id:'unknown',seq:1,created_at:'2026-11-20T00:00:00Z'}}":"{}"}});
    T.retryReply=label=>${kind==='channels'?"({channels:[{path:label}]})":`({thread:{...T.projectionThread,id:${JSON.stringify(kind==='unknown'?'unknown':'th')},labels:{gate:label},revision:'9007199254740993'}})`};
    T.sendRetry=events=>T.projectionPolls.at(-1).resolve({events,next_cursor:'advanced-cursor'});
   })()`);
   await until('window.__op.projectionPolls.length===1');
   await ev("window.__op.deferProjection=true;window.__op.sendRetry([window.__op.retryEvent('invalidated')])");
   await until('window.__op.projectionReads.length===1 && window.__op.projectionPolls.length===2');
   assert(`${name}: cursor advanced once`,await ev("window.__op.probe.S.cursor==='advanced-cursor' && window.__op.probe.S.events===1"));
   if (variant==='coalesce') {
    await ev("window.__op.sendRetry([window.__op.retryEvent('second'),window.__op.retryEvent('third')])"); await sleep(60);
    assert(`${name}: one in flight`,await ev('window.__op.projectionReads.length===1'));
    await ev("window.__op.projectionReads[0].resolve(window.__op.retryReply('old'))"); await sleep(60);
    assert(`${name}: superseded response not published`,await ev("![...window.__op.probe.S.threads.values()].some(t=>t.labels.gate==='old') && !window.__op.probe.S.channels.some(c=>c.path==='old')"));
    assert(`${name}: latest ticket gets one read`,await ev('window.__op.projectionReads.length===2'));
    await ev("window.__op.projectionReads.at(-1).resolve(window.__op.retryReply('approved'))");
   } else {
    await ev(`window.__op.projectionReads[0].reject(Object.assign(new Error('synthetic failure'),{status:${variant==='unauthorized'?401:503}}))`); await sleep(60);
    if (variant==='unauthorized') {
     assert(`${name}: unauthorized once and poll aborted`,await ev('window.__op.projectionUnauthorized===1 && window.__op.projectionPolls.at(-1).signal.aborted'));
     await ev('window.__op.advanceRetry(30000)'); await sleep(60);
     assert(`${name}: credential never retried`,await ev('window.__op.projectionReads.length===1 && window.__op.projectionPolls.length===2 && window.__op.retryTimers.size===0'));
    } else {
     assert(`${name}: failure exposes reconnecting`,await ev("window.__op.projectionStatuses.at(-1)==='reconnecting'"));
     await ev('window.__op.sendRetry([])'); await sleep(60);
     assert(`${name}: healthy poll cannot hide stale projection`,await ev("window.__op.projectionStatuses.at(-1)==='reconnecting'"));
     if (variant==='snapshot' || variant==='dispose') {
      if (variant==='snapshot') {
       await ev("window.__op.deferProjection=false;window.__op.projectionPolls.at(-1).reject(Object.assign(new Error('resync'),{status:409,problem:{code:'resync_required'}}))");
       await until('window.__op.projectionSnapshots===2');
      } else await ev('window.__op.probe.stop()');
      await ev('window.__op.advanceRetry(30000)'); await sleep(60);
      assert(`${name}: canceled retry cannot read`,await ev('window.__op.projectionReads.length===1 && window.__op.retryTimers.size===0'));
     } else {
      if (variant==='backoff') {
       await ev("window.__op.sendRetry([window.__op.retryEvent('again'),window.__op.retryEvent('latest')])"); await sleep(40);
       assert(`${name}: invalidations preserve retry deadline`,await ev('window.__op.projectionReads.length===1 && window.__op.retryTimers.size===1'));
      }
      const delays=variant==='backoff'?[1000,2000,4000,8000,16000,30000,30000]:[1000];
      for (const [i,delay] of delays.entries()) {
       await ev(`window.__op.advanceRetry(${delay-1})`); await sleep(25);
       assert(`${name}: attempt ${i+1} waits`,await ev(`window.__op.projectionReads.length===${i+1}`));
       await ev('window.__op.advanceRetry(1)'); await sleep(60);
       const ran=await ev(`window.__op.projectionReads.length===${i+2}`);
       assert(`${name}: attempt ${i+1} retries without events`,ran);
       if (!ran) break;
       if (i<delays.length-1) { await ev("window.__op.projectionReads.at(-1).reject(Object.assign(new Error('still unavailable'),{status:503}))");await sleep(25); }
       else await ev("window.__op.projectionReads.at(-1).resolve(window.__op.retryReply('approved'))");
      }
     }
    }
   }
   await sleep(60);
   if (['recover','backoff','coalesce'].includes(variant)) {
    assert(`${name}: current projection recovered`,await ev(kind==='channels'?"window.__op.probe.S.channels[0].path==='approved'":`window.__op.probe.S.threads.get('${kind==='unknown'?'unknown':'th'}')?.labels.gate==='approved'`));
    assert(`${name}: recovery is live and clean`,await ev("window.__op.projectionStatuses.at(-1)==='live' && window.__op.retryTimers.size===0"));
    if (kind==='thread') assert(`${name}: current gate warning rendered`,await ev("document.querySelector('#me').textContent.includes('label says approved; task is still blocked')"));
   }
   if (['recover','backoff','coalesce'].includes(variant)) assert(`${name}: retries never rewind cursor or replay events`,await ev(`window.__op.probe.S.cursor==='advanced-cursor' && window.__op.probe.S.events===${variant==='recover'?1:3}`));
   assert(`${name}: only snapshot lifecycle writes`,await ev("window.__op.writes.length===window.__op.projectionSnapshots*2 && window.__op.writes.every(w=>w.path.includes('/snapshots'))"));
  } finally { await ev('window.__op.probe?.stop()'); }
 }
 assert('all variants execute',fixtures===18 && assertions>100);
 assert('no browser exceptions',errors.length===0);
 console.log(`OP17-024 executed ${assertions} assertions across ${fixtures} fresh fixtures`);
}
async function op17012IrcContextCase() {
 let assertions=0, fixtures=0;
 const assert=(name,ok)=>{assertions++;check(`OP17-012: ${name}`,ok)};
 for (const variant of ['initial fallback','stale other-channel topic','status','no readable thread']) {
  await freshIdentityFixture(); fixtures++;
  try {
   await ev("document.querySelector('#operator-login').checked=true;document.querySelector('#token').value='synthetic-irc-context';document.querySelector('#login-form').requestSubmit()");
   await until("!!document.body && document.body.dataset.operator==='true'");
   await ev('window.__op.mountIrcContext()');
   if (variant!=='initial fallback') {
    await ev("document.querySelector('.i-link[data-thread=stale]').click()");
    assert(`${variant}: divider selects explicit topic`,await ev("document.querySelector('.i-topic').textContent.includes('Moved topic')"));
   }
   if (variant==='stale other-channel topic') await ev("{const C=window.__op.ircContext;C.stale.channel_path='elsewhere';C.view.render(C.st);}");
   if (variant==='status') await ev("document.querySelector('.i-tab[data-channel=status]').click()");
   if (variant==='no readable thread') await ev("{const C=window.__op.ircContext;C.S.threads.clear();C.S.tasks.clear();C.view.render(C.st);}");
   const hasThread=variant==='initial fallback'||variant==='stale other-channel topic';
   assert(`${variant}: topic displays resolved context`,await ev(hasThread?"document.querySelector('.i-topic a').getAttribute('href').endsWith('t=th')":"!document.querySelector('.i-topic a')"));
   assert(`${variant}: fresh fixture has no writes`,await count()===0);
   await ev("document.querySelector('.i-input').click()");
   await sleep(250);
   assert(`${variant}: event matches displayed thread`,await ev(`window.__op.ircOpened.length===1 && window.__op.ircOpened[0].board==='brd_test' && window.__op.ircOpened[0].ch===${JSON.stringify(variant==='status'?'status':'ops')} && window.__op.ircOpened[0].t===${hasThread?"'th'":'null'}`));
   if (hasThread) {
    if (await ev("window.__op.ircOpened.at(-1).t==='th'")) await until("!!document.querySelector('.op-workspace [aria-label=\"Task actions\"]')");
    const selected=await ev("document.querySelector('.op-workspace select[aria-label=Thread]')?.value==='th' && !!document.querySelector('.op-workspace [aria-label=\"Task actions\"]')");
    assert(`${variant}: task controls select displayed task`,selected);
    if (selected) {
     await click('Assign to myself');
     assert(`${variant}: confirmation names displayed task`,await ev("document.querySelector('.op-confirm').textContent.includes(window.__op.task.title) && document.querySelector('.op-confirm').textContent.includes('Channel: ops')"));
     assert(`${variant}: confirmation sends nothing before approval`,await count()===0);
     await confirm('Assign to @alice');
     assert(`${variant}: confirmed write targets displayed task`,await ev("window.__op.writes.length===1 && window.__op.writes[0].path==='/v1/boards/brd_test/tasks/t/assignee' && window.__op.writes[0].body.assignee_actor_id==='a'"));
    }
   } else {
    if (await ev("window.__op.ircOpened.at(-1).t===null")) await until("!!document.querySelector('.op-workspace textarea')");
    assert(`${variant}: new-thread composer selects no task`,await ev("document.querySelector('.op-workspace select[aria-label=Thread]')?.value==='' && !document.querySelector('.op-workspace [aria-label=\"Task actions\"]') && [...document.querySelectorAll('.op-workspace h3')].some(e=>e.textContent==='New thread')"));
   }
   assert(`${variant}: opening context sends no unintended writes`,await count()===(hasThread && await ev("window.__op.task.assignee_actor_id==='a'")?1:0));
  } finally {
   if (await ev("!!document.querySelector('.op-workspace')")) await click('Close');
   await ev('window.__op.disposeIrcContext()');
   await ev("document.querySelector('#forget').click()");
   await until("!!document.querySelector('#login') && !document.querySelector('#login').hidden");
  }
 }
 assert('all four fresh variants executed nonzero assertions',fixtures===4 && assertions>=23);
 assert('no browser exceptions',errors.length===0);
 console.log(`OP17-012 executed ${assertions} assertions across ${fixtures} fresh fixtures`);
}
async function op17014IrcProvenanceCase() {
 let assertions=0,fixtures=0;
 const assert=(name,ok,detail='')=>{assertions++;check(`OP17-014: ${name}`,ok,detail)};
 for (const variant of ['snapshot','task.created','task.claimed','task.completed','task.failed','task.completed conflicting','task.failed conflicting']) {
  const [type,conflicting]=variant.split(' ');
  const detail=type==='task.completed'?'recorded summary':'recorded failure';
  const recorded=text=>text.includes(detail)&&!['mutable outcome substitute','mutable reason substitute','false top-level summary','false top-level reason'].some(value=>text.includes(value));
  await freshIdentityFixture(); fixtures++;
  try {
   await ev("document.querySelector('#operator-login').checked=true;document.querySelector('#token').value='synthetic-irc-provenance';document.querySelector('#login-form').requestSubmit()");
   await until("!!document.body && document.body.dataset.operator==='true'");
   await ev('window.__op.mountIrcProvenance()');
   const before=await ev("document.querySelector('.i-text').textContent");
   assert(`${variant}: snapshot has no invented task history`,!['opened task','claimed task','completed task','mutable outcome substitute'].some(t=>before.includes(t)),before);
   const messages=await ev("[...document.querySelectorAll('.i-text .i-line')].filter(e=>e.querySelector('.i-nick')).map(e=>e.textContent)");
   assert(`${variant}: visible delegated message retains both kinds`,messages.some(t=>t.includes('visible delegated message')&&t.includes('bot')&&t.includes('(agent) for alice (human)')));
   assert(`${variant}: deleted delegated message retains both kinds`,messages.some(t=>t.includes('message was deleted')&&t.includes('bot')&&t.includes('(agent) for alice (human)')));
   assert(`${variant}: deleted content stays hidden`,!before.includes('hidden deleted content'));
   if (type!=='snapshot') {
    await ev(`{const T=window.__op,C=T.ircContext,e=T.ircTaskEvent(${JSON.stringify(type)},true,${!!conflicting});C.view.event(e,C.st);C.view.event(e,C.st);C.view.render(C.st);}`);
    const rows=await ev("[...document.querySelectorAll('.i-text .i-line')].filter(e=>e.textContent.includes('credential')).map(e=>e.textContent)");
    assert(`${variant}: event appears once with actor, principal kinds and credential`,rows.length===1&&rows[0].includes('bot (agent) for alice (human)')&&rows[0].includes('synthetic-event-credential'),JSON.stringify(rows));
    const time=await ev("new Date('2026-11-20T17:43:00Z').toLocaleTimeString([],{hour:'2-digit',minute:'2-digit',hourCycle:'h23'})");
    assert(`${variant}: uses recorded event time`,rows.length===1&&rows[0].startsWith('['+time+']'));
    const action={'task.created':'opened task','task.claimed':'claimed task','task.completed':'completed task','task.failed':'failed task'}[type];
    assert(`${variant}: corresponding task action displayed`,rows.length===1&&rows[0].includes(action));
    if (type==='task.completed'||type==='task.failed')
     assert(`${variant}: channel displays only recorded outcome detail`,rows.length===1&&recorded(rows[0]),JSON.stringify(rows));
    assert(`${variant}: never substitutes mutable owner/outcome or approval`,rows.length===1&&!rows[0].includes('mutable outcome substitute')&&!rows[0].includes('* alice (human) '+action)&&!rows[0].includes('approved'));
    await ev("document.querySelector('.i-tab[data-channel=status]').click()");
    const status=await ev("document.querySelector('.i-text').textContent");
    assert(`${variant}: status preserves provenance once`,status.split('synthetic-event-credential').length===2&&status.includes('bot (agent) for alice (human)'));
    if (type==='task.completed'||type==='task.failed')
     assert(`${variant}: Status displays only recorded outcome detail`,recorded(status),status);
    await ev("document.querySelector('.i-tab[data-channel=ops]').click();{const T=window.__op,C=T.ircContext;C.view.event(T.ircTaskEvent('task.blocked',false),C.st);C.view.render(C.st);}");
    assert(`${variant}: ordinary direct task event remains visible`,await ev("[...document.querySelectorAll('.i-act')].some(e=>e.textContent.includes('alice (human) blocked task: recorded reason')&&!e.textContent.includes(' for '))"));
   }
   if (type==='task.completed'||type==='task.failed') {
    await ev(`{const T=window.__op,C=T.ircContext;C.view.status('resyncing',C.st);T.task.outcome={...T.task.outcome,summary:'mutable outcome substitute after resync',reason:'mutable reason substitute after resync'};C.view.render(C.st);C.view.event(T.ircTaskEvent(${JSON.stringify(type)},true,${!!conflicting}),C.st);C.view.status('live',C.st);}`);
    const after=await ev("[...document.querySelectorAll('.i-text .i-line')].filter(e=>e.textContent.includes('bot (agent) for alice (human)')&&e.textContent.includes('credential')).map(e=>e.textContent)");
    assert(`${variant}: resync render retains recorded channel outcome once`,after.length===1&&recorded(after[0]),JSON.stringify(after));
    await ev("document.querySelector('.i-tab[data-channel=status]').click()");
    const afterStatus=await ev("document.querySelector('.i-text').textContent");
    assert(`${variant}: resync render retains recorded Status outcome once`,recorded(afterStatus)&&afterStatus.split(detail).length===2,afterStatus);
   }
   assert(`${variant}: read-only projection sends no writes`,await count()===0);
  } finally {
   await ev('window.__op.disposeIrcContext()');
   await ev("document.querySelector('#forget').click()");
   await until("!!document.querySelector('#login') && !document.querySelector('#login').hidden");
  }
 }
 assert('all variants executed meaningful assertions',fixtures===7&&assertions>=87);
 assert('no browser exceptions',errors.length===0,JSON.stringify(errors));
 console.log(`OP17-014 executed ${assertions} assertions across ${fixtures} fresh fixtures`);
}
async function op17040IrcModeCase() {
 let assertions=0;
 const assert=(name,ok)=>{assertions++;check(`OP17-040: ${name}`,ok)};
 await freshIdentityFixture();
 try {
  await ev("document.querySelector('#operator-login').checked=true;document.querySelector('#token').value='synthetic-irc-mode';document.querySelector('#login-form').requestSubmit()");
  await until("!!document.body && document.body.dataset.operator==='true'");
  await ev("location.hash='#/irc?b=brd_test'");
  await until("!!document.querySelector('.i-titletext') && !!document.querySelector('.i-link[data-thread=th]')");
  assert('operator title describes active mode',await ev("document.querySelector('.i-titletext').textContent.endsWith('Operator mode') && !document.querySelector('.i-titletext').textContent.includes('Read-only')"));
  assert('operator toolbar describes mode and tools',await ev("document.querySelector('.i-tools').textContent.includes('Operator mode') && document.querySelector('.i-tools').textContent.includes('Operator tools') && !document.querySelector('.i-tools').textContent.includes('Read-only')"));
  assert('operator Reply / task actions opener is enabled',await ev("document.querySelector('button.i-input')?.textContent==='Reply / task actions' && !document.querySelector('button.i-input').disabled"));
  await ev("document.querySelector('.i-input').click()");
  await until("!!document.querySelector('.op-workspace [aria-label=\"Task actions\"]')");
  assert('operator opener exposes reply and task actions',await ev("!!document.querySelector('.op-workspace textarea') && !!document.querySelector('.op-workspace [aria-label=\"Task actions\"]')"));
  await click('Close');
  await ev("document.querySelector('.i-tab[data-channel=status]').click()");
  assert('operator status keeps commands disabled with tools guidance',await ev("document.querySelector('.i-text').textContent.includes('Use Operator tools') && document.querySelector('.i-text').textContent.includes('/commands are disabled')"));
  assert('operator navigation and tools opening send no writes',await count()===0);
  await ev("document.querySelector('#operator-logout').click()");
  await until("!!document.querySelector('#login') && !document.querySelector('#login').hidden && !!document.body && !document.body.dataset.operator");
  assert('logout clears operator workspace and revokes only own credential',await ev("!document.querySelector('.op-workspace') && window.__op.writes.length===1 && window.__op.writes[0].method==='DELETE' && window.__op.writes[0].path==='/v1/credentials/cred_test'"));
  await ev("window.__op.revoked=false;window.__op.scope=['read'];document.querySelector('#operator-login').checked=false;document.querySelector('#token').value='synthetic-irc-read';document.querySelector('#login-form').requestSubmit()");
  await until("!!document.querySelector('#login') && document.querySelector('#login').hidden && !!document.querySelector('.i-titletext') && !!document.body && !document.body.dataset.operator");
  assert('read-only remount restores title',await ev("document.querySelector('.i-titletext').textContent.endsWith('Read-only viewer')"));
  assert('read-only remount restores toolbar',await ev("document.querySelector('.i-tools').textContent.includes('Read-only viewer') && !document.querySelector('.i-tools').textContent.includes('Operator mode')"));
  assert('read-only remount has no write opener and input is disabled',await ev("!document.querySelector('button.i-input') && document.querySelector('input.i-input')?.disabled && !document.querySelector('#operator-tools')?.offsetWidth"));
  await ev("document.querySelector('.i-tab[data-channel=status]').click()");
  assert('read-only status and input keep commands disabled',await ev("document.querySelector('.i-text').textContent.includes('This window is read-only. /commands are disabled.') && document.querySelector('input.i-input').placeholder.includes('/commands and messages are disabled')"));
  assert('read-only navigation sends no further writes',await count()===1);
 } finally {
  await ev("document.querySelector('#forget').click()");
  await until("!!document.querySelector('#login') && !document.querySelector('#login').hidden");
 }
 assert('no browser exceptions',errors.length===0);
 if (assertions!==13) throw new Error(`OP17-040 expected 13 assertions, executed ${assertions}`);
 console.log(`OP17-040 executed ${assertions} assertions across operator login and read-only remount`);
}
async function op17044WireCase() {
 let assertions=0;
 const assert=(name,ok)=>{assertions++;check(`OP17-044: ${name}`,ok)};
 for(const gate of [false,true]) {
  await freshIdentityFixture();
  try {
   await ev("document.querySelector('#operator-login').checked=true;document.querySelector('#token').value='synthetic-wire-token';document.querySelector('#login-form').requestSubmit()");
   await until("!!document.body && document.body.dataset.operator==='true'");await open();
   await ev(`document.querySelector('input[aria-label^="Task title"]').value='Wire task';document.querySelector('textarea[aria-label^="Message"]').value='Wire brief';document.querySelector('select[aria-label="Task assignee"]').value=${JSON.stringify(gate?'a':'')};document.querySelector('input[aria-label^="Needs my approval"]').checked=${gate}`);
   const before=await count();await click('Create task');await confirm('Create task');
   assert(`create ${gate} single write`,await count()===before+1);
   assert(`create ${gate} exact nested body`,await ev(`JSON.stringify(window.__op.writes.at(-1).body)===JSON.stringify({thread:{channel:'ops',title:'Wire task',text:'Wire brief'},assignee_actor_id:${JSON.stringify(gate?'a':null)},direct_action_required:${gate}})`));
   assert(`create ${gate} created task/thread/root`,await ev(`window.__op.created?.task.title==='Wire task' && window.__op.created.task.direct_action_required===${gate} && window.__op.created.thread.channel_path==='ops' && window.__op.created.root.text==='Wire brief'`));
   assert(`create ${gate} successful receipt`,await ev("document.querySelector('.op-receipts').textContent.includes('Saved.')"));
   const statuses=await ev(`(async()=>{const send=(method,path,body,headers={})=>fetch('/v1/boards/brd_test/'+path,{method,body:JSON.stringify(body),headers}).then(r=>r.status);return Promise.all([
    send('POST','tasks',{invalid_thread:{channel:'ops',title:'bad',text:'bad'},assignee_actor_id:null,direct_action_required:false}),
    send('POST','tasks/t/release',{reason:'release'}),
    send('POST','tasks/t/claims/q/renew',{lease_seconds:1},{'Yak-Claim-Proof':'synthetic'}),
    send('PATCH','tasks/t',{direct_action_required:'true'}),send('POST','unknown',{}),send('PUT','tasks',{})]);})()`);
   assert(`fixture ${gate} rejects malformed writes`,JSON.stringify(statuses)==='[400,428,400,400,404,405]');
  } finally {await ev("document.querySelector('#operator-logout')?.click()");}
 }
 if(assertions!==10)throw new Error(`OP17-044 executed ${assertions}, expected 10`);
 console.log(`OP17-044 executed ${assertions} assertions across two fresh fixtures`);
}
// OP17-028: exercise the advertised preview process without its never-ending branch.
async function op17028PreviewCase() {
 let assertions=0;
 const assert=(name,ok,detail='')=>{assertions++;check(`OP17-028: ${name}`,ok,detail)};
 await freshIdentityFixture();
 assert('normal fixture uses exactly one persistent CDP installation',op17028FixtureInstallations===1);
 assert('normal fixture starts with a fresh empty write capture',await count()===0);
 const normalHTML=await (await fetch(ORIGIN)).text();
 assert('normal HTML does not load fixture endpoint',!normalHTML.includes('/fixture.js'));
 assert('fixture endpoint is 404 outside preview',(await fetch(ORIGIN+'/fixture.js')).status===404);
 const normalSession=session;
 const preview=spawn(process.execPath,[fileURLToPath(import.meta.url)],{
  env:{...process.env,VIEWER_PREVIEW:'1'},stdio:['ignore','pipe','pipe'],
 });
 const exited=new Promise(resolve=>preview.once('exit',(code,signal)=>resolve({code,signal})));
 let stdout='',stderr='',timer,target;
 preview.stdout.on('data',d=>{stdout+=d});preview.stderr.on('data',d=>{stderr+=d});
 // This is an external deadline; never await the preview module itself.
 const deadline=setTimeout(()=>preview.kill('SIGKILL'),15000);
 try {
  const origin=await new Promise((resolve,reject)=>{
   const poll=()=>{const match=/Synthetic viewer: (http:\/\/127\.0\.0\.1:\d+)/.exec(stdout);
    if(match){resolve(match[1]);return}
    if(preview.exitCode!==null || preview.signalCode!==null){reject(new Error(`preview exited before startup: ${stderr}`));return}
    timer=setTimeout(poll,25);
   };poll();
  });
  assert('bounded external preview process advertises a disposable origin',!!origin);
  const html=await (await fetch(origin,{signal:AbortSignal.timeout(3000)})).text();
  const fixtureTag=html.indexOf('<script src="/fixture.js"></script>'),appTag=html.indexOf('<script type="module"');
  assert('preview HTML loads fixture before app module',fixtureTag>=0 && appTag>fixtureTag);
  const script=await fetch(origin+'/fixture.js',{signal:AbortSignal.timeout(3000)});
  assert('preview fixture returns 200 JavaScript',script.status===200 && script.headers.get('content-type')?.includes('javascript'),`status=${script.status}`);
  session=undefined;
  target=(await cmd('Target.createTarget',{url:'about:blank'})).result.targetId;
  session=(await cmd('Target.attachToTarget',{targetId:target,flatten:true})).result.sessionId;
  await cmd('Runtime.enable');await cmd('Page.enable');
  // Observe timing without installing the fixture in this fresh browser target.
  await cmd('Page.addScriptToEvaluateOnNewDocument',{source:`Object.defineProperty(window,'__op',{configurable:true,get(){return this.__op17028Value},set(value){this.__op17028Value=value;this.__op17028Boot={script:document.currentScript?.getAttribute('src'),state:document.readyState}}})`});
  await cmd('Page.navigate',{url:origin+'/#/boards'});
  for(let n=0;n<70;n++){if(await ev("!!window.__op && document.body?.dataset.view==='boards'"))break;await sleep(50)}
  assert('preview installs fixture during parsing before module boot',await ev("window.__op17028Boot?.script==='/fixture.js' && window.__op17028Boot?.state==='loading'"));
  assert('preview app boots with synthetic read-only session',await ev("!!window.__op && window.__op.scope?.length===1 && window.__op.scope[0]==='read' && sessionStorage.getItem('yak-viewer-token')==='synthetic-preview-read-token' && document.body.dataset.view==='boards' && !document.body.dataset.operator"));
  assert('preview boot sends zero writes',await ev("Array.isArray(window.__op?.writes) && window.__op.writes.length===0"));
  assert('preview has no ReferenceError or other browser exceptions',errors.length===0,errors.join(' | '));
 } finally {
  clearTimeout(timer);
  if(target)await cmd('Target.closeTarget',{targetId:target});
  session=normalSession;
  preview.kill('SIGTERM');
  const stopped=await exited;
  clearTimeout(deadline);
  assert('external smoke process terminates and is reaped',stopped.signal==='SIGTERM' && preview.signalCode==='SIGTERM',JSON.stringify({stopped,stderr}));
 }
 assert('normal persistent CDP installation remains exactly once',op17028FixtureInstallations===1);
 assert('all preview and normal assertions execute',assertions===13);
 console.log(`OP17-028 executed ${assertions} assertions across fresh normal and preview fixtures`);
}
async function op17004PagehideCase() {
 let assertions=0;
 const assert=(name,ok)=>{assertions++;check(`OP17-004: ${name}`,ok)};
 for(const variant of ['pending','reject','logout','release401']) {
  await freshIdentityFixture("window.__op.task.state='ready';");
  try {
   await ev(`(async()=>{const {OperatorSession}=await import('/operator.js');const hold=OperatorSession.prototype.hold;
    window.__op004={hold};OperatorSession.prototype.hold=function(...args){window.__op004.session=this;return hold.apply(this,args)};})()`);
   await ev("document.querySelector('#operator-login').checked=true;document.querySelector('#token').value='synthetic-close-token';document.querySelector('#login-form').requestSubmit()");
   await until("!!document.body && document.body.dataset.operator==='true'");await open();
   await click('Work on this now (while this tab is open)');await confirm('Claim');
   await until("!!window.__op004.session && !!document.querySelector('.op-lease')");
   await ev(`(()=>{const x=window.__op004,s=x.session;x.claims=[...s.claims.values()];
    s.hold('/v1/boards/brd_test/tasks/second',{claim:{id:'second',lease_seconds:300},proof:'synthetic-second'});x.claims.push(s.claims.get('/v1/boards/brd_test/tasks/second'));
    x.run=s.run;x.credentialPath='/v1/credentials/'+s.me.credential.id;x.fetch=window.fetch;x.calls=[];x.waiting=[];x.timers=[];
    const unschedule=s.unschedule;s.unschedule=id=>{x.timers.push(id);unschedule(id)};
    window.fetch=(path,options)=>{if(options?.method==='POST'&&String(path).endsWith('/release')||options?.method==='DELETE'){
     x.calls.push({path,options,active:s.active,run:s.run,stopped:x.timers.length>=2});
     if(${JSON.stringify(variant)}==='reject')return Promise.reject(new Error('synthetic close failure'));
     if(${JSON.stringify(variant)}==='release401')return Promise.resolve(new Response('',{status:401}));
     return new Promise(resolve=>x.waiting.push(()=>resolve(new Response(null,{status:204}))));
    }return x.fetch(path,options)};
    ${['pending','reject'].includes(variant)?"window.dispatchEvent(new Event('pagehide'));":"document.querySelector('#operator-logout').click();"}
    x.sameTask=x.calls.map(c=>c.options.method);x.cleared=!s.active&&s.me===null&&s.run===null&&s.claims.size===0&&s.pending.size===0&&x.claims.every(c=>c.proof===null);
   })()`);
   if(['pending','reject'].includes(variant)) {
    assert(`${variant}: all three requests dispatched in pagehide task`,await ev("JSON.stringify(window.__op004.sameTask)===JSON.stringify(['POST','POST','DELETE'])"));
    assert(`${variant}: memory and proofs immediately cleared`,await ev('window.__op004.cleared'));
    assert(`${variant}: immutable authenticated keepalive arguments`,await ev("window.__op004.calls.length===3&&window.__op004.calls.every(c=>c.active&&c.stopped&&c.run===window.__op004.run&&c.options.keepalive===true&&c.options.headers.Authorization==='Bearer synthetic-close-token'&&c.options.headers['Yak-Run-Id']===window.__op004.run)&&window.__op004.calls[0].options.headers['Yak-Claim-Proof']==='fake-secret-proof'&&window.__op004.calls[1].options.headers['Yak-Claim-Proof']==='synthetic-second'&&window.__op004.calls[2].path===window.__op004.credentialPath"));
   } else if(variant==='logout') {
    assert('logout sends only first release initially',await ev("JSON.stringify(window.__op004.sameTask)==='[\"POST\"]'"));
    await ev('window.__op004.waiting.shift()()');await until('window.__op004.calls.length===2');
    assert('logout waits for second release before revoke',await ev("window.__op004.calls.every(c=>c.options.method==='POST')"));
    await ev('window.__op004.waiting.shift()()');await until('window.__op004.calls.length===3');
    assert('logout revoke follows both responses',await ev("window.__op004.calls[2].options.method==='DELETE'&&window.__op004.calls.every(c=>c.options.keepalive===false)"));
    await ev('window.__op004.waiting.shift()()');await until('!window.__op004.session.active');
   } else {
    await until('!window.__op004.session.active');
    assert('release401 sends no subsequent credential request',await ev('window.__op004.calls.length===1'));
   }
   assert(`${variant}: renewal loops stopped`,await ev('window.__op004.timers.length>=2'));
   assert(`${variant}: final local state cleared`,await ev('!window.__op004.session.active&&window.__op004.session.me===null&&window.__op004.claims.every(c=>c.proof===null)'));
  } finally {
   await ev(`(async()=>{const x=window.__op004;if(!x)return;window.fetch=x.fetch||window.fetch;const {OperatorSession}=await import('/operator.js');OperatorSession.prototype.hold=x.hold;
    if(x.session?.active){x.session.ending=false;await x.session.end({unauthorized:true});}for(const resolve of x.waiting||[])resolve();})()`);
   await cmd('Page.navigate',{url:'about:blank'});
  }
 }
 assert('required variants executed',assertions>=18);assert('no unhandled browser exceptions',errors.length===0);
 console.log(`OP17-004 executed ${assertions} assertions across 4 fresh fixtures`);
}
const operatorCases = new Map([['OP17-004', op17004PagehideCase], ['OP17-028', op17028PreviewCase], ['OP17-032', op17032AutomaticRenewalCase], ['OP17-033', op17033ConflictCase], ['OP17-030', op17030ConfirmationValuesCase], ['OP17-027', op17027NativeEnterCase], ['OP17-026', op17026ConfirmationCase], ['OP17-031', op17031ConditionalLabelsCase], ['OP17-044', op17044WireCase], ['OP17-040', op17040IrcModeCase], ['OP17-014', op17014IrcProvenanceCase], ['OP17-012', op17012IrcContextCase], ['OP17-024', op17024ProjectionRetryCase], ['OP17-013', op17013ProjectionCase], ['OP17-010', op17010StoreActorsCase], ['OP17-021', op17021ActorRetryCase], ['OP17-009', op17009ActorPaginationCase], ['OP17-036', briefGenerationCase], ['OP17-020', gateEvidenceCase], ['OP17-035', labelOpcodeCase], ['OP17-019', cosmeticReceiptCase], ['OP17-018', delayedDraftCase], ['OP17-007', refreshDiscoveryCase], ['OP17-006', openRaceCase], ['OP17-005', boardSwitchCase], ['OP17-003', identityNavigationCase], ['OP17-029', responseBodyFailureCase], ['OP17-042', renewalClaimViewCase]]);
const selectedCase = process.env.OPERATOR_CASE;
try {
 await cmd('Page.navigate',{url:ORIGIN+'/#/b/brd_test?ch=ops&t=th'});await until("!!document.querySelector('#operator-option') && !document.querySelector('#operator-option').hidden");
 if (selectedCase && !operatorCases.has(selectedCase)) throw new Error(`Unknown OPERATOR_CASE: ${selectedCase}`);
 if (!selectedCase) {
 // Unsafe credentials fail closed without writing or persistence.
 for(const [name,setup]of [['agent',"window.__op.kind='agent'"],['broad scope',"window.__op.scope=['*']"],['credential flag',"window.__op.flags=false"]]){
  await ev(`${setup};document.querySelector('#operator-login').checked=true;document.querySelector('#token').value='fake-operator';document.querySelector('#login-form').requestSubmit()`);await sleep(150);
  check(`${name} refused without storage or writes`,await ev("!document.querySelector('#login').hidden && !sessionStorage.getItem('yak-viewer-token') && !window.__op.writes.length"));
  await ev("window.__op.kind='human';window.__op.scope=null;window.__op.flags=true");
 }
 await ev("document.querySelector('#token').value='fake-operator';document.querySelector('#login-form').requestSubmit()");await until("!!document.body && document.body.dataset.operator==='true'");
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
  await click('Refresh permissions and state');await until(`!!document.querySelector('.op-workspace') && document.querySelector('.op-workspace').textContent.includes("not a person's approval")`);
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
 await ev("window.__op.task.state='blocked';window.__op.root.labels.gate='pending';window.__op.failLabel=1");await click('Refresh permissions and state');await sleep(100);await matrix('Approve and unblock');
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
 check('412 displays conflict review guidance',await ev("document.querySelector('.op-receipts').textContent.includes('Previously read:') && document.querySelector('.op-receipts').textContent.includes('No retry was sent')"));
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
 await ev("document.querySelector('#operator-logout').click()");await until("!!document.querySelector('#login') && !document.querySelector('#login').hidden");
 check('self logout revokes only own credential and clears session',await ev("window.__op.revoked && window.__op.writes.at(-1).path==='/v1/credentials/cred_test' && !document.body.dataset.operator && !document.querySelector('.op-workspace') && !sessionStorage.length"));
 check('logout releases held claim before revoke',await ev("window.__op.writes.at(-2).path.endsWith('/release')"));
 await ev("window.__op.revoked=false;document.querySelector('#token').value='fake-operator';document.querySelector('#login-form').requestSubmit()");await until("!!document.body && document.body.dataset.operator==='true'");await open();
 await ev("window.__op.expired=true");await click('Refresh permissions and state');await until("!!document.querySelector('#login') && !document.querySelector('#login').hidden");
 const failures=await ev('window.__op.authFailures');await sleep(400);
 check('stale authorization ends session without retry',await ev(`!document.body.dataset.operator && window.__op.authFailures===${failures}`));
 await cmd('Page.reload');await sleep(500);
 check('reload starts at read-only login without token',await ev("!document.querySelector('#login').hidden && !document.body.dataset.operator && !sessionStorage.length && !window.__op.writes.length"));

 await ev("document.querySelector('#operator-login').checked=true;document.querySelector('#token').value='fake-operator';document.querySelector('#login-form').requestSubmit()");await until("!!document.body && document.body.dataset.operator==='true'");await open();
 await ev("window.__op.task.state='ready'");await click('Refresh permissions and state');await sleep(100);await click('Work on this now (while this tab is open)');await confirm('Claim');
 await ev("window.__realNow=Date.now;Date.now=()=>window.__realNow()+840000");await until("!!document.querySelector('#operator-identity') && document.querySelector('#operator-identity').textContent.includes('idle logout')");
 check('idle warning appears at fourteen minutes',await ev("!document.querySelector('#operator-session').hidden"));
 await ev("Date.now=()=>window.__realNow()+901000");await until("!!document.querySelector('#login') && !document.querySelector('#login').hidden");
 check('idle timeout releases then self-revokes without polling extending it',await ev("window.__op.writes.at(-2).path.endsWith('/release') && window.__op.revoked && !document.body.dataset.operator"));
 check('no browser exceptions',errors.length===0,errors.join(' | '));
 }
 for (const [name, run] of operatorCases) if (!selectedCase || name===selectedCase) await run();
} catch(e) {check('operator browser run',false,[e.message,...errors,await ev("document.querySelector('.op-workspace')?.textContent")].join(' | '));}
finally {browser.close();chrome.kill();server.close();await sleep(300);await rm(profile,{recursive:true,force:true}).catch(()=>{});}
process.exit(failed?1:0);
