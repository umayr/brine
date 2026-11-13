const { test } = require('node:test');
const assert = require('node:assert/strict');
const { createStreamStatus } = require('../internal/web/static/ui.js');

// Synthetic producer-shaped receipt, not copied qualification evidence.
function receipt() {
  return {version:'dill.operation/1', operationId:'op_'+'1'.repeat(32), operation:'stop',
    target:{engine:'docker',endpointDigest:'sha256:'+'2'.repeat(64),qualifiedIdentityDigest:'sha256:'+'3'.repeat(64)},
    stack:'synthetic',outcome:'succeeded',effects:'complete',changesDetected:false,
    resources:[{kind:'container',identityDigest:'sha256:'+'4'.repeat(64),action:'stop',effect:'complete'}],resourcesTruncated:false,errors:[]};
}
function body(value, final='[brine:done] stop complete\n') {return '[brine:operation] '+JSON.stringify(value)+'\n'+final;}
function consume(text, chunkSize=17) {
  const stream=createStreamStatus({qualified:true,operation:'stop',stack:'synthetic'});
  for(let i=0;i<text.length;i+=chunkSize)stream.push(text.slice(i,i+chunkSize));
  stream.finish();return stream;
}
test('actual producer-shaped terminal succeeds across every marker split',()=>{
  const text=body({receipt:receipt(),exitCode:0,status:'succeeded'});
  for(let i=1;i<=text.length;i++){
    const stream=consume(text,i);
    assert.equal(stream.status.result,'succeeded');
    assert.match(stream.summary(),/Operation ID: op_/);
    assert.match(stream.summary(),/Endpoint selector digest \(not daemon identity\)/);
    assert.match(stream.summary(),/Qualified identity digest \(received\)/);
  }
});
test('qualified successful no-op preserves independent none effects',()=>{
  const r=receipt();r.effects='none';r.resources=[];
  const stream=consume(body({receipt:r,exitCode:0,status:'succeeded'}));
  assert.equal(stream.status.result,'succeeded');assert.match(stream.summary(),/Effects: none/);assert.doesNotMatch(stream.summary(),/Effects: complete/);
});
test('cleanup error retains known succeeded/complete axes but invocation fails',()=>{
  const r=receipt();r.errors=[{code:'COORDINATION_RELEASE_FAILED',message:'Engine outcome was recorded; local coordination cleanup failed.'}];
  const stream=consume(body({receipt:r,exitCode:1,status:'failed'},'[brine:error] stop failed\n'));
  assert.equal(stream.status.result,'failed');assert.match(stream.summary(),/Outcome: succeeded\nEffects: complete/);assert.match(stream.summary(),/Process exit: 1/);assert.match(stream.summary(),/COORDINATION_RELEASE_FAILED/);
});
test('cancelled and unknown axes are not overwritten by old final markers',()=>{
  for(const result of ['cancelled','unknown']){
    const r=receipt();r.outcome=result;r.effects='unknown';
    assert.equal(consume(body({receipt:r,exitCode:1,status:result})).status.result,result);
  }
});
test('missing, malformed, unbound, unknown effects and ambiguous terminals never complete',()=>{
  const good={receipt:receipt(),exitCode:0,status:'succeeded'};
  const bad=[];
  bad.push('[brine:done] stop complete\n',body(good,''),body(good).slice(0,-1),'[brine:operation] {oops}\n[brine:done]\n');
  for(const mutate of [r=>r.effects='future',r=>r.version='dill.operation/2',r=>r.stack='other',r=>r.operationId='bad',r=>r.resources[0].effect='future',r=>delete r.target.qualifiedIdentityDigest,r=>r.target.extra=true]){
    const r=receipt();mutate(r);bad.push(body({receipt:r,exitCode:0,status:'succeeded'}));
  }
  bad.push(body(good)+body(good),body(good).replace('"status":"succeeded"','"status":"failed","status":"succeeded"'),body(good)+'RAW SDK DIAGNOSTIC\n');
  for(const text of bad) assert.equal(consume(text).status.result,'unknown',text.slice(0,100));
  assert.equal(consume(body({receipt:receipt(),exitCode:1,status:'succeeded'})).status.result,'unknown');
  assert.equal(consume(body({receipt:null,exitCode:-1,status:'unknown'},'[brine:error] failed\n')).status.result,'unknown');
});
test('unexpected raw output is never included in receipt summary',()=>{
  const stream=consume('RAW_SUBPROCESS_SENTINEL\n[brine:done]\n');
  assert.equal(stream.status.result,'unknown');assert.doesNotMatch(stream.summary(),/RAW_SUBPROCESS/);
});
