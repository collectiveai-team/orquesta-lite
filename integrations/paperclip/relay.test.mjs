import {test} from "node:test";
import assert from "node:assert/strict";
import {correlation, forwardEvent, installRelay} from "./relay.mjs";

const assignment = {flowRunId:"engine",stepId:"coding",attemptId:"attempt",sourceKey:"orquesta:launch"};
function fixture() {
  const calls = [], state = new Map(), names = [];
  const issue = {id:"issue",companyId:"company",executionRunId:"run",description:`Task\n<!-- orq-lite: ${JSON.stringify(assignment)} -->`};
  const ctx = {
    issues:{async get() {return issue;}},
    config:{async get() {return {orquestaUrl:"https://orquesta.example",orquestaToken:{type:"secret_ref",secretId:"secret"}};}},
    secrets:{async resolve() {return "test-callback-token";}},
    state:{async set(key,value) {state.set(JSON.stringify(key),value);},async get(key) {return state.get(JSON.stringify(key));}},
    events:{on(name) {names.push(name);}},
    http:{async fetch(url, init) {
      calls.push({url,init});
      return new Response(JSON.stringify({received:true,attemptId:"attempt"}),{status:202});
    }},
  };
  return {ctx,calls,issue,names};
}

test("relay forwards only normalized identifiers with a scoped secret and no redirects",async()=>{
  const {ctx,calls}=fixture();
  assert.equal(await forwardEvent(ctx,{eventId:"event",eventType:"issue.updated",entityType:"issue",entityId:"issue",companyId:"company"}),true);
  assert.equal(calls[0].url,"https://orquesta.example/webhooks/paperclip");
  assert.equal(calls[0].init.redirect,"error");
  assert.equal(calls[0].init.headers.authorization,"Bearer test-callback-token");
  assert.deepEqual(JSON.parse(calls[0].init.body),{id:"event",type:"issue.updated",...assignment,references:{issueId:"issue",runId:"run"}});
});

test("finished run resolves durable correlation after issue execution locks clear",async()=>{
  const {ctx,calls,issue}=fixture();
  await forwardEvent(ctx,{eventId:"start",eventType:"issue.checked_out",entityType:"issue",entityId:"issue",companyId:"company"});
  issue.executionRunId=null;
  assert.equal(await forwardEvent(ctx,{eventId:"finish",eventType:"agent.run.finished",entityType:"run",entityId:"run",companyId:"company"}),true);
  assert.equal(JSON.parse(calls[1].init.body).references.issueId,"issue");
});

test("unrelated issues and malformed correlations never produce callbacks",async()=>{
  const {ctx,calls,issue}=fixture();
  issue.description="ordinary work";
  assert.equal(await forwardEvent(ctx,{eventId:"event",entityId:"issue",entityType:"issue",companyId:"company"}),false);
  assert.equal(correlation('<!-- orq-lite: {"attemptId":"a"} -->'),null);
  assert.equal(correlation('<!-- orq-lite: not-json -->'),null);
  assert.equal(calls.length,0);
});

test("unacknowledged or unavailable delivery fails for host retry and does not leak token",async()=>{
  const {ctx}=fixture();
  ctx.http.fetch=async()=>new Response("private provider response",{status:503});
  await assert.rejects(forwardEvent(ctx,{eventId:"event",eventType:"issue.updated",entityType:"issue",entityId:"issue",companyId:"company"}),{message:"Orquesta notification was not accepted (HTTP 503)"});
});

test("operator endpoint is validated and issue plus run notifications are registered",async()=>{
  const {ctx,names}=fixture();
  installRelay(ctx);
  assert.ok(names.includes("issue.updated"));
  assert.ok(names.includes("agent.run.finished"));
  ctx.config.get=async()=>({orquestaUrl:"https://user:password@other.example",orquestaToken:{}});
  await assert.rejects(forwardEvent(ctx,{eventId:"event",entityType:"issue",entityId:"issue",companyId:"company"}),/without embedded credentials/);
});
