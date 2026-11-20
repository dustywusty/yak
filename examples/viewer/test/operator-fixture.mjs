// OP17-042: JSON-marshalled domain.ClaimView corpus (generator recorded in ledger).
export const op17042ClaimCorpus = [{"id":"q","task_id":"t","actor_id":"a","execution_id":"exec","run_id":null,"epoch":1,"brief_revision":1,"status":"active","revision":2,"lease_seconds":30,"acquired_at":"2026-11-20T00:00:00Z","expires_at":"2026-11-20T00:00:30Z","ended_at":null,"end_recorded_at":null},{"id":"q","task_id":"t","actor_id":"a","execution_id":"exec","run_id":null,"epoch":1,"brief_revision":1,"status":"released","revision":2,"lease_seconds":30,"acquired_at":"2026-11-20T00:00:00Z","expires_at":"2026-11-20T00:00:30Z","ended_at":"2026-11-20T00:00:10Z","end_recorded_at":"2026-11-20T00:00:10Z"},{"id":"q","task_id":"t","actor_id":"a","execution_id":"exec","run_id":null,"epoch":1,"brief_revision":1,"status":"expired","revision":2,"lease_seconds":30,"acquired_at":"2026-11-20T00:00:00Z","expires_at":"2026-11-20T00:00:30Z","ended_at":"2026-11-20T00:00:10Z","end_recorded_at":"2026-11-20T00:00:10Z"},{"id":"q","task_id":"t","actor_id":"a","execution_id":"exec","run_id":null,"epoch":1,"brief_revision":1,"status":"completed","revision":2,"lease_seconds":30,"acquired_at":"2026-11-20T00:00:00Z","expires_at":"2026-11-20T00:00:30Z","ended_at":"2026-11-20T00:00:10Z","end_recorded_at":"2026-11-20T00:00:10Z"},{"id":"q","task_id":"t","actor_id":"a","execution_id":"exec","run_id":null,"epoch":1,"brief_revision":1,"status":"failed","revision":2,"lease_seconds":30,"acquired_at":"2026-11-20T00:00:00Z","expires_at":"2026-11-20T00:00:30Z","ended_at":"2026-11-20T00:00:10Z","end_recorded_at":"2026-11-20T00:00:10Z"},{"id":"q","task_id":"t","actor_id":"a","execution_id":"exec","run_id":null,"epoch":1,"brief_revision":1,"status":"revoked","revision":2,"lease_seconds":30,"acquired_at":"2026-11-20T00:00:00Z","expires_at":"2026-11-20T00:00:30Z","ended_at":"2026-11-20T00:00:10Z","end_recorded_at":"2026-11-20T00:00:10Z"}];
// Disposable in-page API. No real token, server, board or task is touched.
export function operatorFixture() {
  const B='brd_test',now=Date.now(),iso=()=>new Date().toISOString();
  // Snapshot creation and store polling share a signal; the feed owns another.
  let snapshotSignal = null;
  const actors=[{id:'a',handle:'alice',kind:'human'},{id:'bot',handle:'bot',kind:'agent'}];
  const thread={id:'th',board_id:B,channel_path:'ops',title:'<img onerror=alert(1)> gate',root_message_id:'root',labels:{gate:'pending'},task_id:'t',revision:1,updated_at:iso(),created_at:iso()};
  const task={id:'t',board_id:B,thread_id:'th',channel_path:'ops',title:thread.title,owner_actor_id:'a',assignee_actor_id:null,current_claim:null,revision:3,brief_revision:1,state:'blocked',direct_action_required:true,block:{reason:'waiting for review'},dependencies:[],created_at:iso(),state_changed_at:iso()};
  const root={id:'root',thread_id:'th',channel_path:'ops',seq:1,is_root:true,author_actor_id:'a',created_at:iso(),revision:1,text:'<script>window.pwned=1</script>',mentions:[],labels:thread.labels,attribution:{credential_id:'cred_test'}};
  const T=window.__op={writes:[],task,thread,root,events:[],expired:false,failLabel:0,conflict:false,dropUnblock:false,seenKeys:[],revoked:false,kind:'human',scope:null,flags:true,resync:false,snapshots:0,authFailures:0};
  const json=(b,status=200)=>new Response(status===204?null:JSON.stringify(b),{status,headers:{Date:T.renewDate || new Date().toUTCString(),'Content-Type':'application/json'}});
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
        if(u.searchParams.get('wait_ms'))await new Promise(r=>setTimeout(r,100));
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
    if(p.endsWith('/unblock')) {
      T.seenKeys.push(opts.headers['Idempotency-Key']);
      if(T.conflict){T.conflict=false;task.revision++;task.assignee_actor_id='bot';return json({code:'revision_conflict',revision:task.revision},412)}
      if(task.state==='blocked'){task.state='ready';task.revision++;T.events.push({id:'approved',type:'task.unblocked',actor_id:'a',credential_id:'cred_test',recorded_at:iso(),payload:{task:{...task},reason:body.reason}});}
      if(T.dropUnblock){T.dropUnblock=false;throw new TypeError('dropped response')}
      return json(task);
    }
    if(p.endsWith('/labels')) {
      if(T.labelFault) {
        if(T.deferLabel) await new Promise(resolve=>T.resolveLabel=resolve);
        if(T.labelFault.status===0) throw new TypeError('synthetic label network uncertainty');
        return json({code:'revision_conflict',key:'gate',current:T.labelFault.current || 'changes_requested'},T.labelFault.status);
      }
      if(T.failLabel){T.failLabel--;return json({code:'revision_conflict',current:'changes_requested'},412)}
      for(const op of body.operations) {if(op.op==='unset')delete root.labels[op.key];else root.labels[op.key]=op.value;}return json(root);
    }
    if(p.endsWith('/claims')){task.state='claimed';task.current_claim={id:T.claimId || 'q',actor_id:'a',lease_seconds:T.testLease || 300,acquired_at:iso(),expires_at:new Date(now+300000).toISOString()};return json({claim:task.current_claim,proof:'fake-secret-proof',task,brief:{revision:1}})}
    if(p.endsWith('/renew')) { if(T.deferRenew) await new Promise(resolve=>T.resolveRenew=resolve); return json(T.renewReply || {}, 200); }
    if(p.endsWith('/release')){task.state='ready';task.current_claim=null;return json(task)}
    if(p.endsWith('/assignee')){task.assignee_actor_id=body.assignee_actor_id;task.revision++;return json(task)}
    if(m==='PATCH'&&p.endsWith('/tasks/t')){Object.assign(task,body);task.revision++;return json(task)}
    if(p.endsWith('/credentials/cred_test')){T.revoked=true;return json(null,204)}
    return json({});
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
        T.writes.push({method:opts.method,path:u.pathname,body:opts.body ? JSON.parse(opts.body) : null,headers:opts.headers});
        if (T.pendingFailurePath === u.pathname) { T.pendingFailurePath = null; return json({code:'synthetic_ambiguous_failure'},503); }
        return json({});
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
