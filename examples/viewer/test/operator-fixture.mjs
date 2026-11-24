// OP17-032: execute callbacks in monotonic deadline order, including newly queued work.
export function op17032Clock() {
  let now = 0, next = 0;
  const timers = new Map();
  return {
    now: () => now,
    timers,
    schedule(fn, ms, ...args) {
      const id = ++next;
      timers.set(id, { at: now + Number(ms), fn: () => fn(...args), delay: Number(ms) });
      return id;
    },
    unschedule: id => timers.delete(id),
    async advance(ms) {
      const end = now + ms; let fired = 0;
      for (;;) {
        const entry = [...timers].filter(([, t]) => t.at <= end).sort((a, b) => a[1].at - b[1].at || a[0] - b[0])[0];
        if (!entry) break;
        if (++fired > 10000) throw new Error('OP17-032 timer queue did not converge');
        const [id, t] = entry; timers.delete(id); now = t.at;
        await t.fn();
      }
      now = end;
      return fired;
    },
  };
}
// OP17-020: ordinary and gate unblocks with explicit event-time evidence.
export function op17020Cases() {
  const event = (type = 'task.unblocked', flag = true, extra = {}) => ({
    id: 'candidate', type, actor_id: 'a', on_behalf_of_actor_id: null,
    credential_id: 'synthetic-gate-credential', recorded_at: '2026-11-20T00:00:00Z',
    payload: { task: { id: 't', thread_id: 'th', channel_path: 'ops', title: 'Gate evidence', state: 'ready', ...(flag === null ? {} : { direct_action_required: flag }) }, reason: 'gate approved' }, ...extra,
  });
  const blocked = event('task.blocked'); blocked.id = 'blocked';
  return [
    ['false flag', event('task.unblocked', false), false],
    ['missing flag', event('task.unblocked', null), false],
    ['string flag', event('task.unblocked', 'true'), false],
    ['direct human gate', event(), true],
    ['agent gate', event('task.unblocked', true, { actor_id: 'bot' }), false],
    ['delegated human gate', event('task.unblocked', true, { delegation_id: 'delegated' }), false],
    ['human on behalf', event('task.unblocked', true, { on_behalf_of_actor_id: 'bot' }), false],
    ['unknown actor gate', event('task.unblocked', true, { actor_id: 'missing' }), false],
    ['completed only', event('task.completed'), false],
  ].map(([name, candidate, approved]) => ({ name, candidate, events: [blocked, candidate], approved }));
}
// OP17-042: JSON-marshalled domain.ClaimView corpus (generator recorded in ledger).
export const op17042ClaimCorpus = [{"id":"q","task_id":"t","actor_id":"a","execution_id":"exec","run_id":null,"epoch":1,"brief_revision":1,"status":"active","revision":2,"lease_seconds":30,"acquired_at":"2026-11-20T00:00:00Z","expires_at":"2026-11-20T00:00:30Z","ended_at":null,"end_recorded_at":null},{"id":"q","task_id":"t","actor_id":"a","execution_id":"exec","run_id":null,"epoch":1,"brief_revision":1,"status":"released","revision":2,"lease_seconds":30,"acquired_at":"2026-11-20T00:00:00Z","expires_at":"2026-11-20T00:00:30Z","ended_at":"2026-11-20T00:00:10Z","end_recorded_at":"2026-11-20T00:00:10Z"},{"id":"q","task_id":"t","actor_id":"a","execution_id":"exec","run_id":null,"epoch":1,"brief_revision":1,"status":"expired","revision":2,"lease_seconds":30,"acquired_at":"2026-11-20T00:00:00Z","expires_at":"2026-11-20T00:00:30Z","ended_at":"2026-11-20T00:00:10Z","end_recorded_at":"2026-11-20T00:00:10Z"},{"id":"q","task_id":"t","actor_id":"a","execution_id":"exec","run_id":null,"epoch":1,"brief_revision":1,"status":"completed","revision":2,"lease_seconds":30,"acquired_at":"2026-11-20T00:00:00Z","expires_at":"2026-11-20T00:00:30Z","ended_at":"2026-11-20T00:00:10Z","end_recorded_at":"2026-11-20T00:00:10Z"},{"id":"q","task_id":"t","actor_id":"a","execution_id":"exec","run_id":null,"epoch":1,"brief_revision":1,"status":"failed","revision":2,"lease_seconds":30,"acquired_at":"2026-11-20T00:00:00Z","expires_at":"2026-11-20T00:00:30Z","ended_at":"2026-11-20T00:00:10Z","end_recorded_at":"2026-11-20T00:00:10Z"},{"id":"q","task_id":"t","actor_id":"a","execution_id":"exec","run_id":null,"epoch":1,"brief_revision":1,"status":"revoked","revision":2,"lease_seconds":30,"acquired_at":"2026-11-20T00:00:00Z","expires_at":"2026-11-20T00:00:30Z","ended_at":"2026-11-20T00:00:10Z","end_recorded_at":"2026-11-20T00:00:10Z"}];
// Disposable in-page API. No real token, server, board or task is touched.
export function operatorFixture() {
  const B='brd_test',now=Date.now(),iso=()=>new Date(Date.now()+(window.__op?.clock?.now() || 0)).toISOString();
  const fixtureTimeout = (window.setTimeout || setTimeout).bind(window);
  // Snapshot creation and store polling share a signal; the feed owns another.
  let snapshotSignal = null;
  const actors=[{id:'a',handle:'alice',kind:'human'},{id:'bot',handle:'bot',kind:'agent'}];
  const thread={id:'th',board_id:B,channel_path:'ops',title:'<img onerror=alert(1)> gate',root_message_id:'root',labels:{gate:'pending'},task_id:'t',revision:1,updated_at:iso(),created_at:iso()};
  const task={id:'t',board_id:B,thread_id:'th',channel_path:'ops',title:thread.title,owner_actor_id:'a',assignee_actor_id:null,current_claim:null,revision:3,brief_revision:1,state:'blocked',direct_action_required:true,block:{reason:'waiting for review'},dependencies:[],created_at:iso(),state_changed_at:iso()};
  const root={id:'root',thread_id:'th',channel_path:'ops',seq:1,is_root:true,author_actor_id:'a',created_at:iso(),revision:1,text:'<script>window.pwned=1</script>',mentions:[],labels:{...thread.labels},attribution:{credential_id:'cred_test'}};
  const T=window.__op={writes:[],task,thread,root,events:[],expired:false,failLabel:0,conflict:false,dropUnblock:false,seenKeys:[],revoked:false,kind:'human',scope:null,flags:true,resync:false,snapshots:0,authFailures:0};
  const json=(b,status=200)=>new Response(status===204?null:JSON.stringify(b),{status,headers:{Date:T.renewDate || new Date(Date.now()+(T.clock?.now() || 0)).toUTCString(),'Content-Type':'application/json'}});
  let eventNumber = 0;
  const unblockReceipts = new Map();
  function emit(type, resourceType, resource, payload) {
    T.events.push({id:`gate-event-${++eventNumber}`,type,board_id:B,thread_id:thread.id,
      channel_path:thread.channel_path,actor_id:'a',credential_id:'cred_test',recorded_at:iso(),
      resource:{type:resourceType,id:resource.id,revision:resource.revision},payload:JSON.parse(JSON.stringify(payload))});
  }
  function invalidateThread(reason, before, after) {
    thread.labels = {...after};
    emit('thread.view_invalidated','thread',thread,{thread,reason,labels_before:before,labels_after:after});
  }
  window.fetch=async(url,opts={})=>{
    const u=new URL(url,location.href),p=u.pathname,m=opts.method||'GET';
    if(p==='/viewer-config')return json({operator:true});
    if(p==='/v1/server')return json({capabilities:['yak.credentials.restrictions','yak.tasks','yak.tasks.claims','yak.messages','yak.labels'],limits:{max_page_size:200,max_wait_ms:30000,claim_lease_max_seconds:300}});
    if(T.expired || T.revoked){T.authFailures++;return json({code:'unauthenticated'},401);}
    if(p==='/v1/me')return json({actor:{...actors[0],kind:T.kind},credential:{id:'cred_test',scopes:T.scope||['read','post','label','claim','task_manage','task_admin'],board_ids:[B],expires_at:new Date(now+28800000).toISOString(),may_manage_credentials:!T.flags,may_delegate:false}});
    if(m==='GET') {
      if(p==='/v1/boards')return json({boards:[{id:B,name:'test'}]});
      if(p.endsWith('/actors'))return json({actors});
      if(p.endsWith('/permissions'))return json({capabilities:T.permissions||['read','post','label','claim','task_manage','task_admin']});
      if(p.endsWith('/channels'))return json({channels:[{path:'ops',caller_capabilities:['read','post'],access_mode:'inherited'}]});
      if(p.endsWith('/threads'))return json({threads:[thread],next_page_token:null});
      if(p.endsWith('/threads/th'))return json({thread});
      if(p.endsWith('/messages'))return json({messages:[root],next_page_token:null});
      if(p.endsWith('/tasks/t'))return json(task);
      if(p.endsWith('/tasks'))return json({items:[task],next_page_token:null});
      if(p.endsWith('/events')) {
        if(opts.signal?.aborted)throw new DOMException('aborted','AbortError');
        if(T.resync && snapshotSignal != null && opts.signal === snapshotSignal && u.searchParams.get('cursor')) {
          T.resync=false;return json({code:'resync_required'},409);
        }
        if(u.searchParams.get('wait_ms'))await new Promise(r=>fixtureTimeout(r,100));
        return json({events:T.events,next_cursor:'cur',caught_up:true});
      }
      if(/\/snapshots\//.test(p))return json({items:[{type:'thread',id:thread.id,data:thread},{type:'message',id:root.id,data:root},{type:'task',id:task.id,data:task}],next_page_token:null,continuation_cursor:'cur'});
      if(p.endsWith(B))return json({id:B,name:'test'});
    }
    if(p.endsWith('/snapshots')) {
      if(opts.signal?.aborted)throw new DOMException('aborted','AbortError');
      snapshotSignal = opts.signal ?? null;
      T.snapshots++;return json({id:'s'});
    }
    if(/\/snapshots\//.test(p))return json(null,204);
    const body=opts.body?JSON.parse(opts.body):null;
    T.writes.push({method:m,path:p,body,headers:opts.headers});
    const invalid=()=>json({code:'validation_error'},400);
    const only=(value,keys)=>value && typeof value==='object' && !Array.isArray(value) && Object.keys(value).every(k=>keys.includes(k));
    const lease=()=>body?.lease_seconds===undefined || (Number.isInteger(body.lease_seconds) && body.lease_seconds>=15 && body.lease_seconds<=300);
    if(p===`/v1/boards/${B}/tasks`) {
      if(m!=='POST')return json({code:'method_not_allowed'},405);
      if(!only(body,['thread','assignee_actor_id','direct_action_required']) ||
        !only(body.thread,['channel','title','text','labels']) ||
        !['channel','title','text'].every(k=>typeof body.thread[k]==='string') ||
        !body.thread.channel.trim() || !body.thread.title.trim() ||
        !(body.assignee_actor_id===null || actors.some(a=>a.id===body.assignee_actor_id)) ||
        typeof body.direct_action_required!=='boolean')return invalid();
      const id=`created-${(T.createdCount=(T.createdCount||0)+1)}`;
      const createdThread={...thread,id:`th-${id}`,root_message_id:`root-${id}`,title:body.thread.title,channel_path:body.thread.channel,labels:{},task_id:id};
      const createdRoot={...root,id:`root-${id}`,thread_id:createdThread.id,channel_path:body.thread.channel,text:body.thread.text,labels:{}};
      const createdTask={...task,id,thread_id:createdThread.id,title:body.thread.title,channel_path:body.thread.channel,
        state:'ready',block:null,current_claim:null,revision:1,assignee_actor_id:body.assignee_actor_id,direct_action_required:body.direct_action_required};
      T.created={task:createdTask,thread:createdThread,root:createdRoot};return json(createdTask,201);
    }
    if(p===`/v1/boards/${B}/tasks/t/unblock`) {
      if(m!=='POST')return json({},405);
      const key = opts.headers['Idempotency-Key'], guard = opts.headers['If-Match'];
      T.seenKeys.push(key);
      const receipt = unblockReceipts.get(key);
      if (receipt) {
        if (receipt.guard !== guard || receipt.reason !== body.reason) return json({code:'idempotency_conflict'},409);
        return json(receipt.task);
      }
      if(T.conflict){
        T.conflict=false;
        T.conflictCapture={intent:structuredClone(T.writes.at(-1)),writeCount:T.writes.length,before:structuredClone(task)};
        task.revision++;task.assignee_actor_id='bot';
        T.conflictCapture.after=structuredClone(task);
        return json({code:'revision_conflict',revision:task.revision},412);
      }
      if (guard !== `"${task.revision}"`) return json({code:'revision_conflict',revision:task.revision},412);
      if (task.state !== 'blocked') return json({code:'invalid_transition',current_state:task.state},409);
      const before = {...root.labels,'yak.task.state':task.state};
      task.state = task.dependencies.every(d => d.state === 'completed') && !task.unreadable_dependencies_pending ? 'ready' : 'waiting';
      task.block = null;
      task.revision++;
      task.updated_at = task.state_changed_at = iso();
      root.labels = {...root.labels,'yak.task.state':task.state};
      emit('task.unblocked','task',task,{task,reason:body.reason});
      invalidateThread('task_state_changed',before,root.labels);
      if (key) unblockReceipts.set(key,{guard,reason:body.reason,task:JSON.parse(JSON.stringify(task))});
      if(T.dropUnblock){T.dropUnblock=false;throw new TypeError('dropped response')}
      return json(task);
    }
    if(p===`/v1/boards/${B}/messages/root/labels`) {
      if(m!=='PATCH')return json({},405);
      // Validate the whole request and compare against pre-request labels before mutation.
      if(!only(body,['operations']) || !Array.isArray(body.operations) || !body.operations.length)return invalid();
      for(const op of body.operations) {
        if(!only(op,['op','key','value','expect']) || !['set','unset'].includes(op.op) ||
          typeof op.key!=='string' || !/^[a-z0-9][a-z0-9._-]{0,63}$/i.test(op.key) || /^(yak|abby)\./i.test(op.key) ||
          (op.op==='set'?typeof op.value!=='string':Object.hasOwn(op,'value')))return invalid();
        if(Object.hasOwn(op,'expect') && (!only(op.expect,['absent','present','value']) ||
          Object.entries(op.expect).some(([key,value])=>typeof value!==(key==='value'?'string':'boolean'))))return invalid();
      }
      for(const op of body.operations) {
        const key=op.key.toLowerCase(),present=Object.hasOwn(root.labels,key),expect=op.expect || {};
        if((expect.absent===true && present) || (expect.present===true && !present) ||
          (Object.hasOwn(expect,'value') && (!present || root.labels[key]!==expect.value)))
          return json({code:'revision_conflict',key,current:present?root.labels[key]:null},412);
      }
      if(T.labelFault) {
        if(T.deferLabel) await new Promise(resolve=>T.resolveLabel=resolve);
        if(T.labelFault.status===0) throw new TypeError('synthetic label network uncertainty');
        return json({code:'revision_conflict',key:'gate',current:T.labelFault.current || 'changes_requested'},T.labelFault.status);
      }
      if(T.failLabel){T.failLabel--;return json({code:'revision_conflict',key:'gate',current:root.labels.gate ?? null},412)}
      const before = root.labels, after = {...before};
      for (const op of body.operations) {
        const key = op.key.toLowerCase();
        if (op.op === 'unset') delete after[key];
        else after[key] = op.value;
      }
      if (Object.keys(before).length === Object.keys(after).length &&
          Object.keys(before).every(key => before[key] === after[key])) return json(root);
      root.labels = after;
      root.revision++;
      emit('message.labels_changed','message',root,{message:root,thread_title:thread.title,task_id:task.id,
        labels_before:before,labels_after:after});
      thread.revision++;
      thread.updated_at = iso();
      invalidateThread('root_labels_changed',before,after);
      return json(root);
    }
    if(p===`/v1/boards/${B}/tasks/t/claims`){if(m!=='POST')return json({},405);if(!only(body,['lease_seconds','expect_brief_revision'])||!lease())return invalid();task.state='claimed';task.current_claim={id:T.claimId || 'q',actor_id:'a',lease_seconds:T.testLease || 300,acquired_at:iso(),expires_at:new Date(now+300000).toISOString()};return json({claim:task.current_claim,proof:T.claimProof || 'fake-secret-proof',task,brief:{revision:1}})}
    if(p===`/v1/boards/${B}/tasks/t/claims/${encodeURIComponent(T.claimId || 'q')}/renew`) {
      if(m!=='POST')return json({},405);
      if(!opts.headers?.['Yak-Claim-Proof'])return json({code:'precondition_required'},428);
      if(!only(body,['lease_seconds'])||!lease())return invalid();
      if(opts.headers['Yak-Claim-Proof']!==(T.claimProof || 'fake-secret-proof'))return json({code:'forbidden'},403);
      if(!task.current_claim || task.current_claim.id!==(T.claimId || 'q'))return json({code:'claim_lost'},409);
      if(T.deferRenew) await new Promise(resolve=>T.resolveRenew=resolve);
      return json(T.renewReply || {id:task.current_claim?.id || 'q',task_id:task.id,actor_id:'a',execution_id:'synthetic-exec',run_id:null,
        epoch:1,brief_revision:task.brief_revision,status:'active',revision:2,lease_seconds:body.lease_seconds || 300,
        acquired_at:iso(),expires_at:new Date(Date.now()+(T.clock?.now() || 0)+(body.lease_seconds || 300)*1000).toISOString(),ended_at:null,end_recorded_at:null},200);
    }
    if(p===`/v1/boards/${B}/tasks/t/release`){if(m!=='POST')return json({},405);if(!opts.headers?.['Yak-Claim-Proof'])return json({code:'precondition_required'},428);if(!only(body,['reason']))return invalid();task.state='ready';task.current_claim=null;return json(task)}
    if(p===`/v1/boards/${B}/tasks/t/assignee`){if(m!=='PUT')return json({},405);task.assignee_actor_id=body.assignee_actor_id;task.revision++;return json(task)}
    if(p===`/v1/boards/${B}/tasks/t`){if(m!=='PATCH')return json({},405);if(!only(body,['direct_action_required'])||typeof body.direct_action_required!=='boolean')return invalid();Object.assign(task,body);task.revision++;return json(task)}
    if(p==='/v1/credentials/cred_test'){if(m!=='DELETE')return json({},405);T.revoked=true;return json(null,204)}
    // Explicit disposable composer/edit/reaction routes used by the existing matrix.
    if(m==='POST'&&p===`/v1/boards/${B}/threads/th/messages`)return json({...root,id:'reply',is_root:false,text:body.text});
    if(m==='POST'&&p===`/v1/boards/${B}/threads`)return json({...thread,id:'new-thread',title:body.title,channel_path:body.channel});
    if(m==='PATCH'&&p===`/v1/boards/${B}/messages/reply`)return json({...root,id:'reply',is_root:false,text:body.text});
    if(['PUT','DELETE'].includes(m)&&p===`/v1/boards/${B}/messages/root/reactions/like`)return json({});
    return json({code:'not_found'},404);
  };
  // OP17-033: advance application timers while read polling keeps its real delay.
  T.enableConflictClock = () => {
    const timers=new Map();let clock=0,nextId=0,reads=0;
    const timeout=window.setTimeout,interval=window.setInterval,clearTimeout=window.clearTimeout,clearInterval=window.clearInterval;
    const schedule=(fn,ms,args,repeat)=>{const id=--nextId;timers.set(id,{fn,args,at:clock+Math.max(1,Number(ms)||0),repeat});return id;};
    window.setTimeout=(fn,ms,...args)=>schedule(fn,ms,args,0);
    window.setInterval=(fn,ms,...args)=>schedule(fn,ms,args,Math.max(1,Number(ms)||0));
    window.clearTimeout=window.clearInterval=id=>{if(!timers.delete(id)){clearTimeout(id);clearInterval(id);}};
    const base=window.fetch;
    window.fetch=async(url,opts={})=>{
      const read=(opts.method||'GET')==='GET' && !new URL(url,location.href).pathname.endsWith('/events');
      if(read)reads++;
      try{return await base(url,opts);}finally{if(read)reads--;}
    };
    T.drainConflictClock=async()=>{
      const end=clock+60000;let fired=0;
      const settle=async()=>{for(let n=0;n<100;n++){await new Promise(r=>fixtureTimeout(r,0));if(!reads)return;}throw new Error('OP17-033 refresh did not settle');};
      await settle();
      for(;;){
        const entry=[...timers].filter(([,t])=>t.at<=end).sort((a,b)=>a[1].at-b[1].at)[0];
        if(!entry)break;
        if(++fired>1000)throw new Error('OP17-033 timer drain did not converge');
        const [id,t]=entry;clock=t.at;
        if(t.repeat)t.at+=t.repeat;else timers.delete(id);
        t.fn(...t.args);await settle();
      }
      clock=end;await settle();return {fired,reads};
    };
    T.disposeConflictClock=()=>{timers.clear();window.fetch=base;window.setTimeout=timeout;window.setInterval=interval;window.clearTimeout=clearTimeout;window.clearInterval=clearInterval;};
  };
  // OP17-026: hold the first confirmation response before committing it.
  T.enableConfirmationDelay = () => {
    if (T.confirmationDelayEnabled) return;
    T.confirmationDelayEnabled = true;
    const base = window.fetch;
    window.fetch = async (url, opts = {}) => {
      const path = new URL(url, location.href).pathname;
      if (T.delayConfirmation && (opts.method || 'GET') !== 'GET' &&
          !path.includes('/snapshots') && path !== '/v1/boards/brd_test/tasks/t/claims/q/renew') {
        T.delayConfirmation = false;
        const write = {method:opts.method,path,body:opts.body ? JSON.parse(opts.body) : null,headers:opts.headers};
        T.writes.push(write);
        await new Promise(resolve => T.resolveConfirmation = resolve);
        T.resolveConfirmation = null;
        if (T.rejectConfirmation) return json({code:'synthetic_confirmation_rejection'},412);
        // The base transport captures the committed request exactly once.
        T.writes.splice(T.writes.indexOf(write), 1);
      }
      const response = await base(url, opts);
      if ((opts.method || 'GET') === 'GET' && path === '/v1/boards/brd_test/tasks/t' && response.ok)
        T.confirmationTaskRead = await response.clone().json();
      return response;
    };
  };
  // Fault only the next captured operator write, after its synthetic commit.
  T.enableRenewTest = () => {
    T.testLease = 30; T.mono = 0; T.renewTimers = [];
    Object.defineProperty(performance, 'now', { configurable: true, value: () => T.mono });
    const schedule = window.setTimeout, cancel = window.clearTimeout;
    window.setTimeout = (fn, ms, ...args) => {
      if (String(fn).includes('this.renew(path, c)')) { const timer = { fn, ms }; T.renewTimers.push(timer); return timer; }
      return schedule(fn, ms, ...args);
    };
    window.clearTimeout = id => { if (typeof id === 'object') T.renewTimers = T.renewTimers.filter(t => t !== id); else cancel(id); };
    T.fireRenew = () => { const t = T.renewTimers.shift(); if (!t) throw new Error('missing renewal timer'); return t.fn(); };
  };
  const fetch = window.fetch;
  window.fetch = async (url, opts = {}) => {
    const before = T.writes.length;
    const response = await fetch(url, opts);
    if ((opts.method || 'GET') === 'GET' || T.writes.length === before || !T.responseFailure) return response;
    const fault = T.responseFailure; T.responseFailure = null;
    if (fault === 'stream200') return new Response(new ReadableStream({ start(controller) { controller.error(new TypeError('synthetic body interrupted')); } }), { status: 200 });
    const status = Number(fault.replace(/\D/g, ''));
    return new Response(status === 204 ? null : fault.startsWith('truncated') ? '{"claim":' : '{broken', { status });
  };

  // OP17-010: complete actor provenance and independently controlled store pages.
  T.enableStorePagination = () => {
    const base = window.fetch;
    T.storeActorReads = [];
    const human = {id:'page-human',handle:'pageHuman',kind:'human'};
    const principal = {id:'page-principal',handle:'pagePrincipal',kind:'human'};
    root.author_actor_id = 'bot'; root.on_behalf_of_actor_id = principal.id;
    const direct = {...root,id:'page-direct',seq:2,is_root:false,author_actor_id:human.id,on_behalf_of_actor_id:null,text:'page two human message'};
    const event = {id:'page-event',type:'task.unblocked',actor_id:human.id,credential_id:'synthetic-page-credential',recorded_at:iso(),thread_id:'th',channel_path:'ops',payload:{task:{...task},reason:'page human event'}};
    T.events.push(event,{...event,id:'page-delegated-event',actor_id:'bot',on_behalf_of_actor_id:principal.id});
    window.fetch = async (url, opts = {}) => {
      const u = new URL(url,location.href);
      if ((opts.method || 'GET') === 'GET' && u.pathname.endsWith('/actors')) {
        T.storeActorReads.push({token:u.searchParams.get('page_token'),signal:opts.signal,limit:u.searchParams.get('limit')});
        return json(u.searchParams.get('page_token') ? {actors:[human,principal],next_page_token:null} : {actors,next_page_token:'opaque +/actors2'});
      }
      const response = await base(url,opts);
      if ((opts.method || 'GET') !== 'GET' || !response.ok) return response;
      if (u.pathname.endsWith('/messages')) return json({messages:[root,direct],next_page_token:null});
      if (/\/snapshots\//.test(u.pathname)) {
        const data = await response.json(); data.items.push({type:'message',id:direct.id,data:direct}); return json(data);
      }
      return response;
    };
  };
  T.enableStoreProbe = () => {
    T.storeReads = []; T.storeStatuses = []; T.storePublications = []; T.storeAuth = 0;
    T.storeApi = async (method,path,params,signal) => {
      const read = {method,path,params,signal}; T.storeReads.push(read);
      if (method === 'GET' && path.endsWith('/actors')) return new Promise((resolve,reject) => Object.assign(read,{resolve,reject}));
      if (method === 'GET' && path.endsWith('/channels')) return {channels:[]};
      if (path === '/v1/me') return {actor:actors[0]};
      if (method === 'POST') { T.writes.push({method,path,params}); return {id:'synthetic-store-snapshot'}; }
      if (method === 'DELETE') { T.writes.push({method,path,params}); return {}; }
      if (/\/snapshots\//.test(path)) return {items:[],next_page_token:null,continuation_cursor:'probe-cursor'};
      if (path.endsWith('/events')) return new Promise((resolve,reject) => {
        T.storeResync = () => reject(Object.assign(new Error('resync'),{status:409,problem:{code:'resync_required'}}));
        signal.addEventListener('abort',()=>reject(new DOMException('aborted','AbortError')),{once:true});
      });
      return {id:B,name:'synthetic-store'};
    };
  };

  // OP17-013: deferred projection reads deliberately survive cancellation.
  T.enableProjectionProbe = () => {
    T.projectionReads = []; T.projectionPolls = []; T.projectionChanges = 0;
    T.projectionSnapshots = 0;
    T.projectionThread = { ...thread, labels:{gate:'snapshot'}, revision:'9007199254740991' };
    T.projectionApi = async (method,path,params,signal) => {
      if (method === 'POST') { T.writes.push({method,path}); T.projectionSnapshots++; return {id:'projection-snapshot'}; }
      if (method === 'DELETE') { T.writes.push({method,path}); return {}; }
      if (path.endsWith('/actors')) return {actors, next_page_token:null};
      if (path === '/v1/me') return {actor:actors[0]};
      if (/\/snapshots\//.test(path)) return {items:[{type:'thread',id:'th',data:structuredClone(T.projectionThread)}],next_page_token:null,continuation_cursor:'projection-cursor'};
      if (path.endsWith('/events')) return new Promise((resolve,reject) => {
        T.projectionPolls.push({resolve,reject,signal});
        signal.addEventListener('abort',()=>reject(new DOMException('aborted','AbortError')),{once:true});
      });
      if (path.endsWith('/channels') && !T.deferProjection) return {channels:[{path:'snapshot'}]};
      if (path.endsWith('/channels') || /\/threads\//.test(path)) return new Promise((resolve,reject) => {
        T.projectionReads.push({path,signal,resolve,reject});
      });
      return {id:B,name:'projection probe'};
    };
  };

  // OP17-024: virtual retry deadlines; all I/O remains disposable and deferred.
  T.enableProjectionRetry = () => {
    T.enableProjectionProbe();
    T.retryTime = Date.now(); T.retryTimers = new Map(); T.retryTimerID = 0;
    T.projectionStatuses = []; T.projectionUnauthorized = 0;
    const schedule = window.setTimeout, cancel = window.clearTimeout;
    Date.now = () => T.retryTime;
    window.setTimeout = (fn, ms, ...args) => {
      if (ms >= 1000 && ms <= 30000) {
        const id = `OP17-024-${++T.retryTimerID}`;
        T.retryTimers.set(id, {at:T.retryTime + ms, fn:()=>fn(...args)}); return id;
      }
      return schedule(fn, ms, ...args);
    };
    window.clearTimeout = id => { if (!T.retryTimers.delete(id)) cancel(id); };
    T.advanceRetry = ms => {
      T.retryTime += ms;
      for (const [id, timer] of [...T.retryTimers]) if (timer.at <= T.retryTime) {
        T.retryTimers.delete(id); timer.fn();
      }
    };
  };

  // OP17-009: deferred actor pages can settle even after cancellation/removal.
  T.enableFeedPagination = (variant) => {
    T.actorReads = []; T.feedReads = []; T.feedDraws = 0;
    const candidate = {id:'feed-human', type:'task.unblocked', actor_id:variant === 'unknown' ? 'missing-human' : 'a',
      credential_id:'synthetic-page-credential', recorded_at:'2026-11-20T00:00:00Z',
      payload:{task:{id:'t',title:'Paged gate',direct_action_required:true},reason:'page two approval'}};
    T.feedApi = async (method, path, params, signal) => {
      T.feedReads.push({method,path,params,signal});
      if (method !== 'GET') { T.writes.push({method,path,params}); throw new Error('OP17-009 unexpected write'); }
      if (path === '/v1/boards') return {boards:[{id:B,name:'Paged board'}]};
      if (path.endsWith('/actors')) {
        const read = {params,signal}; T.actorReads.push(read);
        return await new Promise(resolve => read.resolve = resolve);
      }
      if (path.endsWith('/events')) {
        if (!params.cursor) return {events:[candidate,{...candidate,id:'feed-bot',actor_id:'bot'}],next_cursor:'feed-cursor'};
        return await new Promise((resolve,reject) => {
          T.removeFeedBoard = () => reject(Object.assign(new Error('removed'),{status:404}));
          signal.addEventListener('abort', () => reject(new DOMException('aborted','AbortError')), {once:true});
        });
      }
      throw new Error('OP17-009 unexpected path '+path);
    };
  };

  // OP17-021: explicit relists and actor failures without wall-clock waits.
  T.enableFeedRetry = () => {
    T.enableFeedPagination('retry');
    const schedule = window.setInterval, cancel = window.clearInterval;
    window.setInterval = (fn, ms, ...args) => {
      if (ms === 60000) { T.relistFeed = fn; return 'OP17-021-relist'; }
      return schedule(fn, ms, ...args);
    };
    window.clearInterval = id => { if (id !== 'OP17-021-relist') cancel(id); };
    const base = T.feedApi;
    T.feedApi = async (method, path, params, signal) => {
      if (path.endsWith('/actors')) {
        T.feedReads.push({method,path,params,signal});
        const read = {params,signal}; T.actorReads.push(read);
        return await new Promise((resolve,reject) => Object.assign(read,{resolve,reject}));
      }
      return base(method,path,params,signal);
    };
  };

  // OP17-006: hold each initial tools board-list read until the test settles it.
  T.enableOpenRace = () => {
    T.enableBoardSwitch();
    const base = window.fetch;
    T.openReads = [];
    window.fetch = async (url, opts = {}) => {
      if ((opts.method || 'GET') === 'GET' && new URL(url, location.href).pathname === '/v1/boards') {
        await new Promise((resolve, reject) => T.openReads.push({ resolve, reject }));
      }
      return base(url, opts);
    };
  };

  // OP17-005: two disposable boards with distinct destination IDs and delayed discovery.
  T.enableBoardSwitch = () => {
    const base = window.fetch;
    T.boardReads = [];
    const replace = value => {
      if (Array.isArray(value)) return value.map(replace);
      if (value && typeof value === 'object') return Object.fromEntries(Object.entries(value).map(([k,v]) => [k,replace(v)]));
      return ({brd_test:'brd_other',ops:'other',th:'th_other',t:'t_other',root:'root_other',reply:'reply_other'})[value] || value;
    };
    window.fetch = async (url, opts = {}) => {
      const u = new URL(url, location.href), other = u.pathname.includes('/boards/brd_other');
      if ((opts.method || 'GET') === 'GET' && /\/(channels|permissions|threads|actors)(\/|$)/.test(u.pathname)) T.boardReads.push(u.pathname + u.search);
      if (other && u.pathname.endsWith('/channels')) {
        T.discoveryStarted = true;
        if (T.deferBoard) await new Promise(resolve => T.resolveBoard = resolve);
        if (T.failBoard) return json({code:'synthetic_discovery_failure',detail:'synthetic_discovery_failure'},503);
        if (T.emptyBoard) return json({channels:[]});
      }
      const mapped = other ? u.href.replace('/boards/brd_other','/boards/brd_test').replaceAll('th_other','th').replaceAll('t_other','t') : u.href;
      // Capture actual write destinations, without translating them to board A.
      if ((opts.method || 'GET') !== 'GET' && !u.pathname.includes('/snapshots')) {
        if (T.pendingFailurePath === u.pathname) {
          T.writes.push({method:opts.method,path:u.pathname,body:opts.body ? JSON.parse(opts.body) : null,headers:opts.headers});
          T.pendingFailurePath = null; return json({code:'synthetic_ambiguous_failure'},503);
        }
        const before=T.writes.length;
        const response=await base(mapped.replaceAll('reply_other','reply'),opts);
        if(T.writes.length>before)T.writes.at(-1).path=u.pathname;
        return response;
      }
      const response = await base(mapped, opts);
      if (!response.ok || response.status === 204) return response;
      const data = await response.json();
      if (u.pathname === '/v1/boards') data.boards.push({id:'brd_other',name:'other'});
      if (u.pathname === '/v1/me') { data.credential.board_ids.push('brd_other'); data.credential.scopes.push('react'); }
      if (u.pathname === '/v1/server') data.capabilities.push('yak.reactions');
      if (u.pathname.endsWith('/permissions')) data.capabilities.push('react');
      if (u.pathname.endsWith('/messages')) data.messages.push({...root,id:'reply',is_root:false,text:'editable reply'});
      return json(other ? replace(data) : data);
    };
  };

  // OP17-018: settle a captured disposable write only when the test releases it.
  T.enableDraftDelay = () => {
    const base = window.fetch;
    window.fetch = async (url, opts = {}) => {
      const response = await base(url, opts);
      if (!T.delayDraft || (opts.method || 'GET') === 'GET' || new URL(url, location.href).pathname.includes('/snapshots')) return response;
      T.delayDraft = false;
      await new Promise(resolve => T.resolveDraft = resolve);
      if (T.draftOutcome === 'network') throw new TypeError('synthetic draft network failure');
      if (T.draftOutcome === 'conflict') return json({code:'revision_conflict'},409);
      return response;
    };
  };

}

// OP17-036: recorded order determines the current blocked generation.
export function op17036Cases() {
  let seq = 0;
  const event = (type, state, extra = {}) => ({
    id: `generation-${++seq}`, type, actor_id: 'bot', recorded_at: '2026-11-20T00:00:00Z',
    payload: { task: { id: 't', state, direct_action_required: true,
      brief_revision: 1, block: state === 'blocked' ? { at: '2026-11-20T00:00:01Z', reason: 'review' } : null } }, ...extra,
  });
  const human = event('task.unblocked', 'ready', { actor_id: 'a' });
  const brief = event('task.brief_updated', 'blocked');
  const agent = event('task.unblocked', 'ready');
  const unrelated = event('task.assignee_changed', 'blocked');
  const other = event('task.brief_updated', 'blocked'); other.payload.task.id = 'other';
  const sameGeneration = {...human, payload: brief.payload};
  const cases = [
    ['human gate remains current', [human], human],
    ['brief block clears approval', [human, brief], null],
    ['agent after brief cannot revive approval', [human, brief, agent], null],
    ['ready brief preserves approval', [human, event('task.brief_updated', 'ready')], human],
    ['other task block does not interfere', [human, other], human],
    ['unrelated blocked event cannot revive approval', [human, brief, unrelated, agent], null],
    ['another projection entering blocked clears approval', [human, event('task.assignee_changed', 'blocked'), agent], null],
    ['new human after brief approves', [human, brief, agent, human], human],
    ['unchanged blocked projection preserves current generation', [brief, sameGeneration, unrelated], sameGeneration],
    ['changed block identity clears approval', [brief, sameGeneration, {...unrelated, payload: {task: {...brief.payload.task, block: {...brief.payload.task.block, at: '2026-11-20T00:00:02Z'}}}}], null],
  ];
  // The synthetic blocked unblock probes identity tracking on unchanged projections.
  for (const type of ['task.blocked', 'task.reopened']) {
    const reset = event(type, 'blocked');
    cases.push([`${type} clears approval`, [human, reset, agent], null]);
    cases.push([`new human after ${type} approves`, [human, reset, agent, human], human]);
  }
  return cases.map(([name, events, approval]) => ({name, events, approval}));
}
