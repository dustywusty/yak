import { op17020Cases, op17042ClaimCorpus } from './operator-fixture.mjs';
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { op17036Cases } from './operator-fixture.mjs';
import { credentialRefusal, allowedRequest, OperatorSession, approvalRecord, taskIntent } from '../static/operator.js';
const now=Date.now();
const me=()=>({actor:{id:'a',handle:'alice',kind:'human'},credential:{id:'c',scopes:['read','post','label','claim','task_manage'],board_ids:['b'],expires_at:new Date(now+28800000).toISOString(),may_manage_credentials:false,may_delegate:false}});
const server={capabilities:['yak.credentials.restrictions'],limits:{claim_lease_max_seconds:300}};
test('credential ceiling fails closed with specific reasons',()=>{
 assert.equal(credentialRefusal(me(),server,now),'');
 for(const [key,value] of [['scopes',['*']],['scopes',['strange']],['board_ids',null],['expires_at',null],['expires_at',new Date(now+86400000).toISOString()],['may_delegate',true],['may_manage_credentials',true]]) {const m=me();m.credential[key]=value;assert.ok(credentialRefusal(m,server,now),key)}
 for(const edit of [m=>m.actor.kind='agent',m=>m.delegation={},m=>m.credential.delegation_id='d']) {const m=me();edit(m);assert.ok(credentialRefusal(m,server,now))}
 assert.ok(credentialRefusal(me(),server,NaN));
});
test('client allowlist is exact and self revoke only',()=>{
 assert.ok(allowedRequest('DELETE','/v1/credentials/c','c'));
 for(const [method,path] of [['GET','/v1/credentials'],['DELETE','/v1/credentials/other'],['POST','/v1/delegations'],['POST','/v1/boards/b/tasks/t/complete'],['PATCH','/v1/boards/b/snapshots/s'],['POST','/v1/boards/b/tasks/t/../cancel']]) assert.equal(allowedRequest(method,path,'c'),false,path);
 assert.ok(allowedRequest('POST','/v1/boards/b/tasks/t/unblock','c'));
});
test('confirmed intents use read revisions and limit gate patch fields',()=>{
 const t={id:'t',revision:5,brief_revision:2,state:'blocked',direct_action_required:true,thread_id:'th'};
 const i=taskIntent('flag','/v1/boards/b',t,{value:false});
 assert.deepEqual(i.steps[0].body,{direct_action_required:false});assert.equal(i.steps[0].headers['If-Match'],'"5"');
 const a=taskIntent('approve','/v1/boards/b',t,{root:'m',note:'ship'});
 assert.ok(a.steps[0].path.endsWith('/unblock'));assert.equal(a.steps[0].body.reason,'gate approved: ship');
 assert.deepEqual(a.steps[1].body.operations[0].expect,{value:'pending'});
});
test('ambiguous retries reuse key and never repeat successful first step',async()=>{
 const calls=[];let fail=true;
 const s=new OperatorSession({send:async(method,path,body,headers)=>{calls.push({path,headers});if(path.endsWith('labels')&&fail){fail=false;throw Object.assign(new Error('network'),{status:0})}return {}},changed:()=>{}});
 s.start(me(),server,now);
 const i=s.intent({steps:[{method:'POST',path:'/v1/boards/b/tasks/t/unblock',body:{}},{method:'PATCH',path:'/v1/boards/b/messages/m/labels',body:{}}]});
 await assert.rejects(s.perform(i));await s.perform(i);
 assert.equal(calls.length,3);assert.equal(calls[1].headers['Idempotency-Key'],calls[2].headers['Idempotency-Key']);assert.notEqual(calls[0].headers['Idempotency-Key'],calls[1].headers['Idempotency-Key']);
 await s.end();
});
test('logout releases before self revoke, idle ignores polling, 401 is not retried',async()=>{
 let mono=0;const calls=[];
 const s=new OperatorSession({send:async(m,p)=>{calls.push(p);return {}},mono:()=>mono,changed:()=>{}});s.start(me(),server,now);
 mono=800000;s.hold('/v1/boards/b/tasks/t',{claim:{id:'q',lease_seconds:300},proof:'SECRET'});
 mono=900001;await s.tick();assert.deepEqual(calls,['/v1/boards/b/tasks/t/release','/v1/credentials/c']);assert.equal(s.active,false);assert.equal(s.claims.size,0);
});
test('approval comes from direct known human event after latest block only',()=>{
 const actors=[{id:'a',kind:'human',handle:'alice'},{id:'bot',kind:'agent',handle:'bot'}];
 const ev=(type,id,extra={})=>({type,actor_id:id,recorded_at:'2026-01-01',payload:{task:{id:'t',direct_action_required:true},reason:'gate approved'},...extra});
 const e=ev('task.unblocked','a');assert.equal(approvalRecord([e],actors,'t'),e);
 for(const x of [ev('task.unblocked','bot'),ev('task.completed','a'),ev('task.unblocked','a',{on_behalf_of_actor_id:'p'}),ev('task.unblocked','missing')]) assert.equal(approvalRecord([x],actors,'t'),null);
 assert.equal(approvalRecord([e,ev('task.blocked','bot')],actors,'t'),null);
});
test('412 is a known rejection, same confirmed revision is never replaced',async()=>{
 const seen=[];const s=new OperatorSession({send:async(m,p,b,h)=>{seen.push(h);throw Object.assign(new Error('stale'),{status:412,problem:{revision:9}})}});s.start(me(),server,now);
 const i=s.intent(taskIntent('flag','/v1/boards/b',{id:'t',revision:2},{value:true}));await assert.rejects(s.perform(i));assert.equal(seen.length,1);assert.equal(seen[0]['If-Match'],'"2"');s.send=async()=>{};await s.end();
});
test('renewal retries receipt without extending local deadline from replay time',async()=>{
 let mono=0,fail=true;const keys=[];
 const s=new OperatorSession({mono:()=>mono,schedule:()=>0,send:async(m,p,b,h)=>{if(p.endsWith('/renew')){keys.push(h['Idempotency-Key']);if(fail){fail=false;mono=4000;throw Object.assign(new Error('network'),{status:0})}}return {}}});s.start(me(),server,now);
 s.hold('/v1/boards/b/tasks/t',{claim:{id:'q',lease_seconds:30},proof:'secret'});const c=s.claims.values().next().value;
 await s.renew('/v1/boards/b/tasks/t',c);mono=7000;await s.renew('/v1/boards/b/tasks/t',c);
 assert.equal(keys[0],keys[1]);assert.equal(c.deadline,30000);await s.end();
});
test('terminal renew failure drops proof and stops renewal',async()=>{
 const s=new OperatorSession({schedule:()=>0,send:async()=>{throw Object.assign(new Error('lost'),{status:409})}});s.start(me(),server,now);s.hold('/v1/boards/b/tasks/t',{claim:{id:'q',lease_seconds:300},proof:'secret'});const c=s.claims.values().next().value;await s.renew('/v1/boards/b/tasks/t',c);assert.equal(c.proof,null);assert.equal(s.claims.size,0);s.send=async()=>{};await s.end();
});
test('401 ends once without release or revoke retry; revoke failure clears memory',async()=>{
 const calls=[];const s=new OperatorSession({send:async(m,p)=>{calls.push(p);throw Object.assign(new Error('unauthenticated'),{status:401})}});s.start(me(),server,now);await assert.rejects(s.perform(s.intent({steps:[{method:'POST',path:'/v1/boards/b/threads',body:{}}]})));assert.equal(s.active,false);assert.equal(calls.length,1);assert.equal(s.me,null);
 s.send=async()=>{throw Object.assign(new Error('no'),{status:503})};s.start(me(),server,now);await s.end();assert.equal(s.me,null);assert.match(s.notice,/revocation failed/);
});
test('expiry ends at server-relative monotonic deadline and pagehide sends keepalive',async()=>{
 let mono=0;const calls=[];const s=new OperatorSession({mono:()=>mono,send:async(m,p,b,h,o)=>{calls.push({p,o});return {}}});const m=me();m.credential.expires_at=new Date(now+60000).toISOString();s.start(m,server,now);mono=60001;await s.tick();assert.equal(s.active,false);assert.equal(calls[0].p,'/v1/credentials/c');
 s.start(me(),server,now);await s.end({keepalive:true});assert.equal(calls.at(-1).o.keepalive,true);
});
test('second-stage approved label conflict is completed without another unblock',async()=>{
 const calls=[];const s=new OperatorSession({send:async(m,p)=>{calls.push(p);if(p.endsWith('/labels'))throw Object.assign(new Error('already approved'),{status:412,problem:{key:'gate',current:'approved'}});return {}}});s.start(me(),server,now);const i=s.intent(taskIntent('approve','/v1/boards/b',{id:'t',revision:1},{root:'m'}));await s.perform(i);assert.equal(i.done,true);assert.equal(calls.length,2);await s.end();
});
test('late 401 from an old intent cannot end a replacement session',async()=>{
 let rejectOld;const s=new OperatorSession({send:async()=>new Promise((resolve,reject)=>{rejectOld=reject})});s.start(me(),server,now);
 const pending=s.perform(s.intent({steps:[{method:'POST',path:'/v1/boards/b/threads',body:{}}]}));
 await s.end({unauthorized:true});s.start(me(),server,now);rejectOld(Object.assign(new Error('old token'),{status:401}));await assert.rejects(pending);assert.equal(s.active,true);await s.end({unauthorized:true});
});
test('late lost-renew response cannot drop a replacement claim',async()=>{
 let rejectOld;const s=new OperatorSession({schedule:()=>0,send:async()=>new Promise((resolve,reject)=>{rejectOld=reject})});s.start(me(),server,now);
 const path='/v1/boards/b/tasks/t';s.hold(path,{claim:{id:'old',lease_seconds:300},proof:'old'});const renewing=s.renew(path,s.claims.get(path));
 s.hold(path,{claim:{id:'new',lease_seconds:300},proof:'new'});rejectOld(Object.assign(new Error('old claim lost'),{status:409}));await renewing;assert.equal(s.claims.get(path)?.id,'new');await s.end({unauthorized:true});
});
test('idle cleanup still runs after sleep when the browser monotonic clock pauses',async()=>{
 let wall=0;const s=new OperatorSession({mono:()=>0,wall:()=>wall,send:async()=>({})});s.start(me(),server,now);wall=900001;await s.tick();assert.equal(s.active,false);
});
test('first input after the idle deadline cannot revive the session',async()=>{
 let mono=0;const s=new OperatorSession({mono:()=>mono,send:async()=>({})});s.start(me(),server,now);mono=900001;s.input();await new Promise(resolve=>setImmediate(resolve));assert.equal(s.active,false);
});
test('overview gate warnings never treat a display label as approval',async()=>{
 const {gateHint}=await import('../static/skin-kit.js');
 assert.equal(gateHint({state:'blocked',direct_action_required:true},{gate:'approved'}),'label says approved; task is still blocked');
 assert.equal(gateHint({state:'ready',direct_action_required:false},{gate:'pending'}),'label only; not a gate');
 assert.equal(gateHint({state:'ready',direct_action_required:true},{gate:'approved'}),'');
});


for (const method of ['PUT', 'DELETE']) {
 for (const key of ['like', '+1', '%2B1', '%F0%9F%91%8D']) {
  test(`OP17-001 ${method} accepts ${key}`, () => {
   assert.equal(allowedRequest(method, `/v1/boards/b/messages/m/reactions/${key}`, 'c'), true);
  });
 }
 for (const key of ['', '%', '%2', '%GG', '%2f', '%2F', '%5c', '%5C', '%252f', '%252B1', '.', '..', '%2e', '%2E%2e', 'a%2e%2eb', '%00', '%1f', '%7f', '%C2%85', '%FF', '%C0%AF', 'like/extra', 'like?x=y', 'like?', 'like#fragment', 'a\\b']) {
  test(`OP17-001 ${method} refuses ${JSON.stringify(key)}`, () => {
   assert.equal(allowedRequest(method, `/v1/boards/b/messages/m/reactions/${key}`, 'c'), false);
  });
 }
}
test('OP17-001 other methods and encoded non-key segments remain refused', () => {
 for (const method of ['GET', 'POST', 'PATCH', 'HEAD', 'OPTIONS']) {
  for (const key of ['like', '+1', '%2B1', '%F0%9F%91%8D']) assert.equal(allowedRequest(method, `/v1/boards/b/messages/m/reactions/${key}`, 'c'), false, `${method} ${key}`);
 }
 for (const path of ['/v1/boards/b%2fx/messages/m/reactions/like', '/v1/boards/b/messages/m%2Fx/reactions/like', '/v1/boards/b%41/messages/m/reactions/like', '/v1/boards/b/tasks/t%41/assignee', '/v1/boards/b/snapshots/s%41', '/v1/boards/b/messages/m%41']) {
  for (const method of ['GET', 'POST', 'PATCH', 'PUT', 'DELETE']) assert.equal(allowedRequest(method, path, 'c'), false, `${method} ${path}`);
 }
});

// Execute the production transport without booting the DOM or owning a token.
async function op17029Transport(fetch, performance = globalThis.performance) {
 const { readFile } = await import('node:fs/promises');
 const { runInNewContext } = await import('node:vm');
 const source = await readFile(new URL('../static/app.js', import.meta.url), 'utf8');
 const error = source.slice(source.indexOf('class ApiError'), source.indexOf('\nasync function api('));
 const transport = source.slice(source.indexOf('async function write('), source.indexOf('\nsession = new OperatorSession'));
 return runInNewContext(`${error}\n${transport}\nwrite`, { fetch, performance, S: { operatorEnabled: true, token: 'synthetic' }, session: { active: true, me: me() }, parseWireJSON, stringifyWireJSON, allowedRequest, AbortController, setTimeout, clearTimeout });
}
for (const [name, status, response] of [
 ['claim headers then rejected body read', 200, () => ({ ok: true, status: 200, text: async () => { throw new TypeError('stream interrupted'); } })],
 ['broken 200 JSON', 200, () => new Response('{broken', { status: 200 })],
 ['truncated 201 body', 201, () => new Response('{"id":', { status: 201 })],
]) {
 test(`OP17-029 ${name} retains the same intent/index/key for replay`, async () => {
  const calls = []; let first = true;
  const send = await op17029Transport(async (path, options) => {
   calls.push({ path, headers: options.headers });
   if (first) { first = false; return response(); }
   return Response.json({ claim: { id: 'q', lease_seconds: 300 }, proof: 'synthetic-proof' });
  });
  const s = new OperatorSession({ send, schedule: () => 0 }); s.start(me(), server, now);
  try {
   const i = s.intent(taskIntent('claim', '/v1/boards/b', { id: 't', brief_revision: 1 }));
   await assert.rejects(s.perform(i), e => e.status === 0 && e.problem.code === 'network');
   assert.equal(i.index, 0); assert.equal(i.done, undefined); assert.equal(i.busy, false);
   assert.equal(s.claims.size, 0); assert.equal(s.notice, null); assert.ok(s.pending.has(i)); assert.equal(s.pending.size, 1);
   await s.perform(i);
   assert.equal(i.done, true); assert.equal(i.index, 1); assert.equal(s.pending.size, 0);
   assert.equal(s.claims.get('/v1/boards/b/tasks/t').proof, 'synthetic-proof');
   assert.equal(calls.length, 2); assert.deepEqual(calls[0], calls[1]);
  } finally { await s.end({ unauthorized: true }); }
 });
}
for (const status of [401, 412]) for (const broken of ['json', 'stream', 'null']) {
 test(`OP17-029 malformed ${status} ${broken} retains the known HTTP rejection`, async () => {
  let calls = 0;
  const send = await op17029Transport(async () => { calls++; return broken === 'stream' ? { ok: false, status, text: async () => { throw new TypeError('stream interrupted'); } } : new Response(broken === 'null' ? 'null' : '{broken', { status }); });
  const s = new OperatorSession({ send }); s.start(me(), server, now);
  try {
   const i = s.intent(taskIntent('flag', '/v1/boards/b', { id: 't', revision: 2 }, { value: true }));
   await assert.rejects(s.perform(i), e => e.status === status && typeof e.problem === 'object');
   assert.equal(calls, 1); assert.equal(i.index, 0); assert.equal(i.done, undefined);
   assert.equal(s.active, status !== 401);
   if (status === 401) { await s.perform(i); assert.equal(calls, 1); assert.equal(s.pending.size, 0); }
   else assert.ok(s.pending.has(i));
  } finally { await s.end({ unauthorized: true }); }
 });
}
test('OP17-029 204 succeeds without reading a body', async () => {
 let reads = 0;
 const send = await op17029Transport(async () => ({ ok: true, status: 204, text: async () => { reads++; throw new Error('must not read'); } }));
 assert.equal(await send('POST', '/v1/boards/b/tasks', {}), null); assert.equal(reads, 0);
});
test('OP17-029 replay without proof explicitly completes as proof unavailable', async () => {
 const send = await op17029Transport(async () => Response.json({ claim: { id: 'q' } }));
 const s = new OperatorSession({ send }); s.start(me(), server, now);
 try {
  const i = s.intent(taskIntent('claim', '/v1/boards/b', { id: 't', brief_revision: 1 })); await s.perform(i);
  assert.equal(i.done, true); assert.equal(s.claims.size, 0); assert.match(s.notice, /proof unavailable/);
 } finally { await s.end({ unauthorized: true }); }
});

// OP17-042 uses the consumer's Go-marshalled views, not claim-create envelopes.
const op17042Date = Date.parse('2026-11-20T00:00:00Z');
const op17042Path = '/v1/boards/b/tasks/t';
function op17042Response(edits = {}, timing = { serverDate: op17042Date, requestMono: 5000 }) {
 const view = { ...op17042ClaimCorpus[0], ...edits };
 if (timing) Object.defineProperty(view, '__responseTiming', { value: timing });
 return view;
}
async function op17042Run(send, run) {
 let mono = 0; const timers = new Set();
 const s = new OperatorSession({ mono: () => mono, wall: () => -9e12, send, schedule: fn => { timers.add(fn); return fn; }, unschedule: fn => timers.delete(fn) });
 s.start(me(), server, now); s.hold(op17042Path, { claim: { id: 'q', lease_seconds: 30 }, proof: 'synthetic' });
 const c = s.claims.get(op17042Path); timers.clear(); mono = 5000;
 try { await run(s, c, timers, value => mono = value); } finally { await s.end({ unauthorized: true }); }
}
for (const view of op17042ClaimCorpus) test(`OP17-042 Go ClaimView ${view.status}`, async () => {
 await op17042Run(async () => op17042Response(view), async (s, c, timers) => {
  await s.renew(op17042Path, c);
  if (view.status === 'active') { assert.equal(c.proof, 'synthetic'); assert.equal(c.deadline, 34000); assert.equal(timers.size, 1); }
  else { assert.equal(c.proof, null); assert.equal(s.claims.size, 0); assert.equal(timers.size, 0); assert.match(s.notice, /claim ended.*Refresh/); }
 });
});
for (const [name, edits] of [ ['wrong id', { id: 'other' }], ['missing status', { status: undefined }], ['unknown status', { status: 'unknown' }], ['zero lease', { lease_seconds: 0 }], ['fractional lease', { lease_seconds: 1.5 }], ['oversized lease', { lease_seconds: Number.MAX_SAFE_INTEGER }], ['invalid expiry', { expires_at: 'invalid' }], ['invalid acquisition', { acquired_at: null }], ['inverted timestamps', { acquired_at: '2026-11-20T00:01:00Z' }], ['active ended', { ended_at: '2026-11-20T00:00:01Z' }], ['active end recorded', { end_recorded_at: '2026-11-20T00:00:01Z' }] ]) test(`OP17-042 rejects ${name}`, async () => {
 await op17042Run(async () => op17042Response(edits), async (s, c, timers) => { await s.renew(op17042Path, c); assert.equal(c.proof, null); assert.equal(timers.size, 0); assert.match(s.notice, /Refresh/); });
});
// Keep the other timestamp and server Date sane so normalization alone causes rejection.
for (const value of [
 '2026-11-20T24:00:00Z', '2027-02-30T00:00:00Z', '2027-02-29T00:00:00Z',
 '1900-02-29T00:00:00Z', '2026-04-31T00:00:00Z', '2026-00-20T00:00:00Z',
 '2026-13-20T00:00:00Z', '2026-11-00T00:00:00Z', '2026-11-32T00:00:00Z',
 '2026-11-20T25:00:00Z', '2026-11-20T00:60:00Z', '2026-11-20T00:00:60Z',
 '2026-11-20T00:00:00+24:00', '2026-11-20T00:00:00+00:60',
]) for (const field of ['acquired_at', 'expires_at']) test(`OP17-042 rejects ${field} calendar/time ${value}`, async () => {
 const normalized = Date.parse(value), base = Number.isFinite(normalized) ? normalized : op17042Date;
 const serverDate = field === 'acquired_at' ? base : base - 30000;
 const edits = { acquired_at: new Date(serverDate).toISOString(), expires_at: new Date(serverDate + 30000).toISOString(), [field]: value };
 await op17042Run(async () => op17042Response(edits, { serverDate, requestMono: 5000 }), async (s, c, timers) => {
  await s.renew(op17042Path, c);
  assert.equal(c.proof, null); assert.equal(s.claims.size, 0); assert.equal(timers.size, 0); assert.match(s.notice, /claim ended.*Refresh/);
 });
});
for (const [acquired_at, expires_at] of [
 ['2028-02-29T23:59:30Z', '2028-03-01T00:00:00Z'],
 ['2000-02-29T00:00:00Z', '2000-02-29T00:00:30Z'],
 ['2026-11-20T23:59:29.123456789+23:59', '2026-11-20T23:59:59.123456789+23:59'],
 ['2026-11-19T00:01:00.5-23:59', '2026-11-19T00:01:30.5-23:59'],
]) test(`OP17-042 accepts calendar/time ${acquired_at}`, async () => {
 const timing = { serverDate: Math.floor(Date.parse(acquired_at) / 1000) * 1000, requestMono: 5000 };
 await op17042Run(async () => op17042Response({ acquired_at, expires_at }, timing), async (s, c, timers) => {
  await s.renew(op17042Path, c);
  assert.equal(c.proof, 'synthetic'); assert.equal(s.claims.size, 1); assert.equal(timers.size, 1); assert.equal(s.notice, null);
  assert.equal(c.deadline, Math.min(35000, 5000 + Date.parse(expires_at) - timing.serverDate - 1000));
 });
});
for (const [name, edits, timing, receive, expected] of [
 ['old replay expiry', { expires_at: '2026-11-20T00:00:10Z' }, { serverDate: op17042Date, requestMono: 5000 }, 7000, 14000],
 ['delayed response and skewed wall', {}, { serverDate: op17042Date, requestMono: 5000 }, 20000, 34000],
 ['elapsed started lease', {}, { serverDate: op17042Date, requestMono: 5000 }, 35001, null],
 ['elapsed server expiry', { expires_at: '2026-11-20T00:00:01Z' }, { serverDate: op17042Date, requestMono: 5000 }, 7000, null],
 ['missing timing retains old bound', {}, null, 7000, 30000],
 ['invalid timing retains old bound', {}, { serverDate: NaN, requestMono: 5000 }, 7000, 30000],
 ['future dispatch retains old bound', {}, { serverDate: op17042Date, requestMono: 9000 }, 7000, 30000],
 ['unknown timing after old deadline', {}, null, 30001, null],
]) test(`OP17-042 ${name}`, async () => {
 let advance; await op17042Run(async () => { advance(receive); return op17042Response(edits, timing); }, async (s, c, timers, setMono) => {
  advance = setMono; await s.renew(op17042Path, c);
  if (expected === null) { assert.equal(c.proof, null); assert.equal(timers.size, 0); }
  else { assert.equal(c.deadline, expected); assert.equal(c.proof, 'synthetic'); assert.equal(timers.size, 1); if (!timing || !Number.isFinite(timing.serverDate) || timing.requestMono > receive) assert.match(s.notice, /Refresh/); }
 });
});
test('OP17-042 accepted lease uses original ambiguous dispatch', async () => {
 let advance, fail = true; const keys = [];
 await op17042Run(async (m, p, b, h) => { keys.push(h['Idempotency-Key']); if (fail) { fail = false; throw new Error('network'); } return op17042Response({ lease_seconds: 20 }, { serverDate: op17042Date, requestMono: 10000 }); }, async (s, c, timers, setMono) => {
  advance = setMono; await s.renew(op17042Path, c); advance(10000); timers.clear(); await s.renew(op17042Path, c);
  assert.equal(c.deadline, 25000); assert.equal(c.lease, 20); assert.equal(keys[0], keys[1]); assert.equal(c.key, null);
 });
});
test('OP17-042 late successful terminal response cannot drop replacement', async () => {
 let resolve; await op17042Run(async () => new Promise(r => resolve = r), async (s, c) => {
  const pending = s.renew(op17042Path, c); s.hold(op17042Path, { claim: { id: 'new', lease_seconds: 30 }, proof: 'new' });
  resolve(op17042Response({ status: 'revoked' })); await pending; assert.equal(s.claims.get(op17042Path).proof, 'new'); assert.equal(s.notice, null);
 });
});
test('OP17-042 transport samples dispatch before delayed body and hides metadata', async () => {
 let mono = 5000;
 const send = await op17029Transport(async () => { mono = 9000; return new Response(JSON.stringify(op17042ClaimCorpus[0]), { headers: { Date: 'Fri, 20 Nov 2026 00:00:00 GMT' } }); }, { now: () => mono });
 const result = await send('POST', op17042Path + '/claims/q/renew', {});
 assert.equal(result.__responseTiming.serverDate, op17042Date); assert.equal(result.__responseTiming.requestMono, 5000);
 assert.equal(Object.keys(result).includes('__responseTiming'), false); assert.deepEqual(JSON.parse(JSON.stringify(result)), { ...op17042ClaimCorpus[0], revision: '2', brief_revision: '1' });
});

for (const ending of ['logout', 'idle', 'expiry', 'pagehide']) test(`OP17-039 ${ending} shares session provenance across actions, renewals, releases and revoke`, async () => {
 let mono = 0, finishRevoke; const calls = [];
 const path = '/v1/boards/b/tasks/t';
 const s = new OperatorSession({ mono: () => mono, wall: () => 0, schedule: () => 0, send: async (method, path, body, headers, options) => {
  calls.push({ method, path, headers: { ...headers }, options, memoryRun: s.run });
  if (method === 'DELETE') return new Promise(resolve => finishRevoke = resolve);
  return { ...op17042ClaimCorpus[0] };
 } });
 s.start(me(), server, now); const run = s.run;
 try {
  assert.match(run, /^operator\/[0-9a-f]{32}$/);
  await s.perform(s.intent(taskIntent('flag', '/v1/boards/b', { id: 't', revision: 1 }, { value: true })));
  s.hold(path, { claim: { id: 'q', lease_seconds: 300 }, proof: 'synthetic' });
  await s.renew(path, s.claims.get(path));
  await s.perform(s.intent(taskIntent('release', '/v1/boards/b', { id: 't' })));
  if (ending === 'idle') mono = 800000;
  s.hold(path, { claim: { id: 'q', lease_seconds: 300 }, proof: 'synthetic' });
  if (ending === 'idle') mono = 900001;
  if (ending === 'expiry') s.expiry = 0;
  const pending = ending === 'idle' || ending === 'expiry' ? s.tick() : s.end({ keepalive: ending === 'pagehide' });
  // Cleanup awaits release before dispatching revoke, then retains memory until it settles.
  await new Promise(resolve => setImmediate(resolve));
  assert.deepEqual(calls.map(c => c.path), [path, `${path}/claims/q/renew`, `${path}/release`, `${path}/release`, '/v1/credentials/c']);
  assert.equal(s.run, run); assert.equal(s.active, true); assert.equal(s.ending, true);
  for (const call of calls) {
   assert.equal(call.memoryRun, run, `${call.path} dispatch retains run`);
   assert.equal(call.headers['Yak-Run-Id'], run, `${call.path} carries session run`);
  }
  for (const call of calls.slice(-2)) assert.equal(call.options.keepalive, ending === 'pagehide');
  finishRevoke(); await pending;
  assert.equal(s.run, null); assert.equal(s.active, false); assert.equal(s.me, null); assert.equal(s.claims.size, 0);
 } finally {
  finishRevoke?.();
  await new Promise(resolve => setImmediate(resolve));
  await s.end({ unauthorized: true });
 }
});
test('OP17-039 each new start generates a different run and revoke captures that session', async () => {
 const runs = [], calls = [];
 const s = new OperatorSession({ send: async (m, p, b, h) => { calls.push(h['Yak-Run-Id']); } });
 try {
  for (let i = 0; i < 3; i++) { s.start(me(), server, now); runs.push(s.run); await s.end(); assert.equal(s.run, null); }
  assert.equal(new Set(runs).size, 3); for (const run of runs) assert.match(run, /^operator\/[0-9a-f]{32}$/);
  assert.deepEqual(calls, runs);
 } finally { await s.end({ unauthorized: true }); }
});
for (const source of ['explicit', 'action', 'renew', 'release']) test(`OP17-039 unauthorized ${source} cleanup emits no revoke`, async () => {
 const calls = []; const path = '/v1/boards/b/tasks/t';
 const s = new OperatorSession({ schedule: () => 0, send: async (method, path, body, headers) => {
  calls.push({ method, path, headers }); throw Object.assign(new Error('synthetic unauthorized'), { status: 401 });
 } });
 s.start(me(), server, now); const run = s.run;
 try {
  s.hold(path, { claim: { id: 'q', lease_seconds: 300 }, proof: 'synthetic' });
  if (source === 'explicit') await s.end({ unauthorized: true });
  if (source === 'action') await assert.rejects(s.perform(s.intent(taskIntent('flag', '/v1/boards/b', { id: 't', revision: 1 }, { value: true }))), { status: 401 });
  if (source === 'renew') await s.renew(path, s.claims.get(path));
  if (source === 'release') await s.end();
  assert.equal(calls.length, source === 'explicit' ? 0 : 1);
  assert.equal(calls.some(c => c.method === 'DELETE'), false);
  for (const call of calls) assert.equal(call.headers['Yak-Run-Id'], run);
  assert.equal(s.active, false); assert.equal(s.run, null); assert.equal(s.claims.size, 0);
 } finally { await s.end({ unauthorized: true }); }
});

// Import the standalone tokenizer suite so the existing unit CI runs it too.
const { parseWireJSON, stringifyWireJSON, compareRevision } = await import('./wire-json.test.mjs');

test('OP17-043 task confirmation snapshot and If-Match keep exact digits', () => {
 for (const revision of ['5', '9007199254740993', '9223372036854775807']) {
  const task = parseWireJSON(`{"id":"t","revision":${revision},"brief_revision":${revision}}`);
  for (const kind of ['flag', 'approve', 'assign']) {
   const intent = taskIntent(kind, '/v1/boards/b', task, { value: true, assignee: 'a', root: 'm' });
   assert.equal(intent.task.revision, revision);
   assert.equal(intent.steps[0].headers['If-Match'], `"${revision}"`);
  }
  const claim = taskIntent('claim', '/v1/boards/b', task);
  assert.equal(stringifyWireJSON(claim.steps[0].body), `{"lease_seconds":300,"expect_brief_revision":${revision}}`);
 }
});

test('OP17-043 production read/write transports retain exact nested revisions', async () => {
 const { readFile } = await import('node:fs/promises');
 const { runInNewContext } = await import('node:vm');
 const source = await readFile(new URL('../static/app.js', import.meta.url), 'utf8');
 const start = source.indexOf('class ApiError');
 const apiSource = source.slice(start, source.indexOf('// ---------- token'));
 const request = runInNewContext(`${apiSource}\napi`, { parseWireJSON, stringifyWireJSON, fetch: async () => new Response('{"thread":{"revision":9007199254740993},"messages":[{"revision":9223372036854775807}]}'), S: { abort: new AbortController() }, session: null, location: { origin: 'http://synthetic.invalid' }, performance, URL, DOMException });
 const result = await request('GET', '/v1/boards/b/threads/th');
 assert.equal(result.thread.revision, '9007199254740993');
 assert.equal(result.messages[0].revision, '9223372036854775807');
 let sent;
 const write = await op17029Transport(async (path, options) => { sent = options; return new Response('{"revision":9223372036854775807}'); });
 const updated = await write('POST', '/v1/boards/b/tasks/t/claims', { lease_seconds: 300, expect_brief_revision: '9007199254740993' });
 assert.equal(sent.body, '{"lease_seconds":300,"expect_brief_revision":9007199254740993}');
 assert.equal(updated.revision, '9223372036854775807');
});

test('OP17-043 production putTask compares revision strings numerically', async () => {
 const { readFile } = await import('node:fs/promises');
 const { runInNewContext } = await import('node:vm');
 const source = await readFile(new URL('../static/skin-kit.js', import.meta.url), 'utf8');
 const body = source.slice(source.indexOf('const putTask ='), source.indexOf('\n\n  async function snapshot'));
 const S = { tasks: new Map(), order: [] };
 const putTask = runInNewContext(`${body}\nputTask`, { S, compareRevision });
 for (const revision of ['9', '10', '9007199254740992', '9007199254740993']) {
  putTask({ id: 't', revision }); assert.equal(S.tasks.get('t').revision, revision);
 }
 putTask({ id: 't', revision: '9007199254740992' }); assert.equal(S.tasks.get('t').revision, '9007199254740993');
 assert.deepEqual(S.order, ['t']);
});

test('OP17-043 production message edit header interpolates the exact parsed revision', async () => {
 const { readFile } = await import('node:fs/promises');
 const { runInNewContext } = await import('node:vm');
 const source = await readFile(new URL('../static/operator-ui.js', import.meta.url), 'utf8');
 const body = source.slice(source.indexOf('function messageControls('), source.indexOf('\n  function composer('));
 for (const revision of ['5', '9007199254740993', '9223372036854775807']) {
  let save, intent;
  const make = runInNewContext(`${body}\nmessageControls`, { h: () => ({ append() {} }), provenance: () => '', session: { me: { actor: { id: 'a' } }, server: {} }, current: { B: '/v1/boards/b', thread: {} }, rights: () => true, draft: () => ({ dataset: {} }), writeButton: (name, click) => { save = click; return {}; }, field: () => ({}), enc: encodeURIComponent, send: (button, spec) => { intent = spec; } });
  make(parseWireJSON(`{"id":"m","author_actor_id":"a","text":"old","revision":${revision}}`));
  save(); assert.equal(intent.steps[0].headers['If-Match'], `"${revision}"`);
 }
});

test('OP17-043 production confirmation displays exactly the revision sent', async () => {
 const { readFile } = await import('node:fs/promises');
 const { runInNewContext } = await import('node:vm');
 const source = await readFile(new URL('../static/operator-ui.js', import.meta.url), 'utf8');
 const extract = (start, end) => {
  const first = source.indexOf(start), last = source.indexOf(end, first);
  assert.ok(first >= 0 && last > first, `missing or unordered production anchors: ${start} / ${end}`);
  return source.slice(first, last);
 };
 const helpers = [
  extract('const valid =', '\n  function dismissConfirmations('),
  extract('function writeButton(', '\n  function say('),
  extract('async function refreshTask(', '\n  function pendingButtons('),
  extract('async function confirmTask(', '\n  function action(')
 ].join('\n');
 for (const revision of ['5', '9007199254740993', '9223372036854775807']) {
  const task = parseWireJSON(`{"id":"t","thread_id":"th","title":"Synthetic","revision":${revision},"brief_revision":${revision},"state":"ready"}`);
  const freshThread = { root_message_id: 'm', labels: { gate: 'pending', ordinary: 'fresh' } };
  const view = Object.freeze({ B: '/v1/boards/b', generation: 7, task, thread: { root_message_id: 'm', labels: { ordinary: 'old' } } });
  let confirmed;
  const reads = [];
  const context = { sequence: 11, current: view, guards: new WeakMap(), enc: encodeURIComponent,
   button: () => ({ dataset: {} }), manager: () => true, actor: () => '@synthetic',
   session: { generation: 7, active: true, ending: false, pending: new Set(), me: { actor: { id: 'a' } } },
   read: async (method, path) => {
    assert.equal(method, 'GET');
    assert.equal(path, ['/v1/boards/b/tasks/t', '/v1/boards/b/threads/th'][reads.length]);
    reads.push(path); return reads.length === 1 ? task : { thread: freshThread };
   },
   taskIntent, structuredClone, confirmation: (name, details, spec) => { confirmed = { details, spec }; }, say: message => assert.fail(message)
  };
  const { confirmTask, writeButton } = runInNewContext(`${helpers}\n({ confirmTask, writeButton })`, context);
  const sourceButton = writeButton('Require my approval', () => {});
  await confirmTask('flag', 'Require my approval', sourceButton, { value: true });
  assert.ok(confirmed);
  assert.equal(reads.length, 2);
  assert.strictEqual(context.current, view);
  assert.ok(Object.isFrozen(context.current));
  assert.deepEqual(confirmed.spec.labels, freshThread.labels);
  assert.notStrictEqual(confirmed.spec.labels, freshThread.labels);
  assert.ok(confirmed.details.includes(`Revision: ${revision}; brief: ${revision}`));
  assert.equal(confirmed.spec.steps[0].headers['If-Match'], `"${revision}"`);
 }
});

// OP17-019: cosmetic receipts must not own the global lock after known rejection.
for (const kind of ['approve', 'changes']) {
 for (const status of [403, 404, 412, 0, 502, 503]) {
  test(`OP17-019 ${kind} label ${status}: original continuation and write lock`, async () => {
   const calls=[]; let failure=status, release;
   const s=new OperatorSession({send:async(method,path,body,headers)=>{
    calls.push(structuredClone({method,path,body,headers}));
    if(path.endsWith('/labels')) {
     if(release==='defer') await new Promise(resolve=>release=resolve);
     if(failure!==null) throw Object.assign(new Error('synthetic label rejection'),{status:failure,problem:{key:'gate',current:'changes_requested'}});
    }
    return {};
   }});
   s.start(me(),server,now);
   try {
    const i=s.intent(taskIntent(kind,'/v1/boards/b',{id:'t',thread_id:'th',revision:3},{root:'root',note:'original note'}));
    const generation=i.generation, steps=i.steps;
    await assert.rejects(s.perform(i));
    const ambiguous=[0,502,503].includes(status);
    assert.equal(i.index,1); assert.equal(i.done,undefined);
    assert.equal(s.pending.size,ambiguous?1:0,'only active or uncertain intents block unrelated writes');
    const original=calls[1];
    if(!ambiguous) {
     const message=s.intent({kind:'message',steps:[{method:'POST',path:'/v1/boards/b/threads/other/messages',body:{text:'unrelated'}}]});
     const before=calls.length;
     await s.perform(i,{requeue:true});
     assert.equal(calls.length,before,'retry cannot conflict with another pending intent');
     await s.perform(message); assert.equal(message.done,true);
    }
    for(let n=0;n<3;n++) {
     await assert.rejects(s.perform(i,{requeue:true}));
     assert.equal(s.pending.size,ambiguous?1:0,'repeated retries cannot grow pending');
     assert.deepEqual(calls.at(-1),original);
    }
    failure=503;
    await assert.rejects(s.perform(i,{requeue:true}));
    assert.equal(s.pending.size,1,'uncertain retry reestablishes the global lock');
    assert.equal(i.index,1); assert.deepEqual(calls.at(-1),original);
    failure=null; release='defer';
    const retry=s.perform(i,{requeue:true});
    assert.equal(s.pending.size,1); assert.equal(i.busy,true);
    const count=calls.length; await s.perform(i,{requeue:true}); assert.equal(calls.length,count,'rapid retry sends once');
    release(); await retry;
    assert.equal(i.done,true); assert.equal(i.index,2); assert.equal(s.pending.size,0);
    assert.equal(i.generation,generation); assert.equal(i.steps,steps);
    assert.equal(calls.filter(c=>!c.path.endsWith('/labels')&&!c.path.includes('/other/')).length,1,'never repeat unblock or request-changes reply');
    for(const call of calls.filter(c=>c.path.endsWith('/labels'))) assert.deepEqual(call,original);
   } finally { await s.end({unauthorized:true}); }
  });
 }
}
for(const field of ['current','current_value']) test(`OP17-019 approve 412 ${field} approved completes receipt`,async()=>{
 const calls=[]; const s=new OperatorSession({send:async(m,p)=>{calls.push(p);if(p.endsWith('/labels'))throw Object.assign(new Error('already approved'),{status:412,problem:{key:'gate',[field]:'approved'}});return {}}});
 s.start(me(),server,now);
 try {const i=s.intent(taskIntent('approve','/v1/boards/b',{id:'t',revision:3},{root:'root'}));await s.perform(i);assert.equal(i.done,true);assert.equal(i.index,2);assert.equal(s.pending.size,0);assert.equal(calls.length,2);} finally {await s.end({unauthorized:true});}
});

for (const c of op17020Cases()) test(`OP17-020 ${c.name}`, () => {
 const actors = [{id:'a',kind:'human'}, {id:'bot',kind:'agent'}];
 assert.equal(approvalRecord(c.events, actors, 't'), c.approved ? c.candidate : null);
 assert.equal(approvalRecord(c.events, actors, 'other'), null, 'different task cannot approve');
});
for (const type of ['task.blocked', 'task.reopened']) test(`OP17-020 latest ${type} resets approval generation`, () => {
 const actors = [{id:'a',kind:'human'}], c = op17020Cases().find(c => c.approved);
 const reset = {...c.candidate, type};
 assert.equal(approvalRecord([...c.events, reset], actors, 't'), null);
 assert.equal(approvalRecord([...c.events, reset, c.candidate], actors, 't'), c.candidate);
});

for (const c of op17036Cases()) test(`OP17-036 ${c.name}`, () => {
 const actors = [{id:'a',kind:'human'}, {id:'bot',kind:'agent'}];
 assert.equal(approvalRecord(c.events, actors, 't'), c.approval);
 assert.equal(approvalRecord(c.events, actors, 'unrelated'), null);
});
