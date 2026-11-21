// Disposable artifacts from production intent/session code; never a real credential.
import { taskIntent, OperatorSession } from '../static/operator.js';
export async function emitOperatorWire() {
 const requests=[], now=Date.now(), path='/v1/boards/brd_test/tasks/t';
 const s=new OperatorSession({schedule:()=>0,unschedule:()=>{},mono:()=>0,
  send:async(method,path,body,headers)=>{requests.push({method,path,body,headers});return {id:'q',status:'active',lease_seconds:300,acquired_at:new Date(now).toISOString(),expires_at:new Date(now+300000).toISOString(),ended_at:null,end_recorded_at:null};}});
 s.start({actor:{id:'a',kind:'human'},credential:{id:'c',scopes:['read','post','label','claim','task_manage'],board_ids:['brd_test'],may_manage_credentials:false,may_delegate:false,expires_at:new Date(now+3600000).toISOString()}},{capabilities:['yak.credentials.restrictions'],limits:{claim_lease_max_seconds:300}},now);
 const task={id:'t',thread_id:'th',revision:3,brief_revision:1};
 try {
  for(const [kind,values] of [
   ['create',{channel:'ops',title:'Wire task',text:'Wire brief',assignee:null,gate:false}],
   ['create',{channel:'ops',title:'Wire task',text:'Wire brief',assignee:'a',gate:true}],
   ['approve',{root:'root',note:'reviewed'}],['changes',{root:'root',note:'Revise it'}],
   ['flag',{value:true}],['flag',{value:false}],['assign',{assignee:'a'}],['assign',{assignee:null}],['claim',{lease:300}],
  ]) await s.perform(s.intent(taskIntent(kind,'/v1/boards/brd_test',task,values)));
  s.hold(path,{claim:{id:'q',lease_seconds:300},proof:'acp1.q.aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'});
  await s.renew(path,s.claims.get(path));
  await s.perform(s.intent(taskIntent('release','/v1/boards/brd_test',task)));
 } finally {await s.end({unauthorized:true});}
 return requests;
}
if(import.meta.url===new URL(process.argv[1],'file:').href) for(const request of await emitOperatorWire()) console.log(JSON.stringify(request));
