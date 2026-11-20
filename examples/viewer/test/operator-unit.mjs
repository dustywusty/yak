import { test } from 'node:test';
import assert from 'node:assert/strict';
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
 const ev=(type,id,extra={})=>({type,actor_id:id,recorded_at:'2026-01-01',payload:{task:{id:'t'},reason:'gate approved'},...extra});
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
