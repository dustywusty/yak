// Disposable in-page API. No real token, server, board or task is touched.
export function operatorFixture() {
  const B='brd_test',now=Date.now(),iso=()=>new Date().toISOString();
  const actors=[{id:'a',handle:'alice',kind:'human'},{id:'bot',handle:'bot',kind:'agent'}];
  const thread={id:'th',board_id:B,channel_path:'ops',title:'<img onerror=alert(1)> gate',root_message_id:'root',labels:{gate:'pending'},task_id:'t',revision:1,updated_at:iso(),created_at:iso()};
  const task={id:'t',board_id:B,thread_id:'th',channel_path:'ops',title:thread.title,owner_actor_id:'a',assignee_actor_id:null,current_claim:null,revision:3,brief_revision:1,state:'blocked',direct_action_required:true,block:{reason:'waiting for review'},dependencies:[],created_at:iso(),state_changed_at:iso()};
  const root={id:'root',thread_id:'th',channel_path:'ops',seq:1,is_root:true,author_actor_id:'a',created_at:iso(),revision:1,text:'<script>window.pwned=1</script>',mentions:[],labels:thread.labels,attribution:{credential_id:'cred_test'}};
  const T=window.__op={writes:[],task,thread,root,events:[],expired:false,failLabel:0,conflict:false,dropUnblock:false,seenKeys:[],revoked:false,kind:'human',scope:null,flags:true,resync:false,snapshots:0,authFailures:0};
  const json=(b,status=200)=>new Response(status===204?null:JSON.stringify(b),{status,headers:{Date:new Date().toUTCString(),'Content-Type':'application/json'}});
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
      if(p.endsWith('/events')) {if(T.resync&&u.searchParams.get('cursor')){T.resync=false;return json({code:'resync_required'},409)}if(opts.signal?.aborted)throw new DOMException('aborted','AbortError');if(u.searchParams.get('wait_ms'))await new Promise(r=>setTimeout(r,100));return json({events:T.events,next_cursor:'cur',caught_up:true});}
      if(/\/snapshots\//.test(p))return json({items:[{type:'thread',id:thread.id,data:thread},{type:'message',id:root.id,data:root},{type:'task',id:task.id,data:task}],next_page_token:null,continuation_cursor:'cur'});
      if(p.endsWith(B))return json({id:B,name:'test'});
    }
    if(p.endsWith('/snapshots')){T.snapshots++;return json({id:'s'})}
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
      if(T.failLabel){T.failLabel--;return json({code:'revision_conflict',current:'changes_requested'},412)}
      for(const op of body.operations) {if(op.op==='remove')delete root.labels[op.key];else root.labels[op.key]=op.value;}return json(root);
    }
    if(p.endsWith('/claims')){task.state='claimed';task.current_claim={id:'q',actor_id:'a',lease_seconds:300,acquired_at:iso(),expires_at:new Date(now+300000).toISOString()};return json({claim:task.current_claim,proof:'fake-secret-proof',task,brief:{revision:1}})}
    if(p.endsWith('/release')){task.state='ready';task.current_claim=null;return json(task)}
    if(p.endsWith('/assignee')){task.assignee_actor_id=body.assignee_actor_id;task.revision++;return json(task)}
    if(m==='PATCH'&&p.endsWith('/tasks/t')){Object.assign(task,body);task.revision++;return json(task)}
    if(p.endsWith('/credentials/cred_test')){T.revoked=true;return json(null,204)}
    return json({});
  };
  // Fault only the next captured operator write, after its synthetic commit.
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

}
