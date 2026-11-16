const ISSUE_EVENTS = [
  "issue.created", "issue.updated", "issue.checked_out",
  "issue.document.created", "issue.document.updated",
];
const RUN_EVENTS = ["agent.run.started", "agent.run.finished", "agent.run.failed", "agent.run.cancelled"];
const STATE_KEY = "orquesta-correlation";

export function correlation(description) {
  const matches = typeof description === "string" && [...description.matchAll(/<!-- orq-lite: (\{[^\n]*\}) -->/g)];
  const match = matches && matches.at(-1);
  if (!match) return null;
  try {
    const parsed = JSON.parse(match[1]);
    if (!["flowRunId", "stepId", "attemptId"].every(key =>
      typeof parsed[key] === "string" && parsed[key].length > 0 && parsed[key].length <= 256
    )) return null;
    if (parsed.sourceKey != null && (typeof parsed.sourceKey !== "string" || parsed.sourceKey.length > 256)) return null;
    return {flowRunId: parsed.flowRunId, stepId: parsed.stepId, attemptId: parsed.attemptId, sourceKey: parsed.sourceKey || ""};
  } catch { return null; }
}

function endpoint(value) {
  const url = new URL(value);
  if (!["https:", "http:"].includes(url.protocol) || url.username || url.password || url.search || url.hash) {
    throw new Error("Orquesta URL must be an HTTP(S) origin without embedded credentials");
  }
  url.pathname = `${url.pathname.replace(/\/$/, "")}/webhooks/paperclip`;
  return url.href;
}

export async function forwardEvent(ctx, event) {
  if (!event.eventId || !event.companyId) return false;
  const payload = event.payload || {};
  let issueId = event.entityType === "issue" ? event.entityId : payload.issueId;
  let runId = event.entityType === "run" ? event.entityId : payload.runId;
  let assignment;
  if (issueId) {
    const issue = await ctx.issues.get(issueId, event.companyId);
    if (!issue || issue.companyId !== event.companyId) return false;
    assignment = correlation(issue.description);
    runId = runId || issue.executionRunId;
    if (assignment && runId) {
      await ctx.state.set({scopeKind: "run", scopeId: runId, stateKey: STATE_KEY}, {...assignment, issueId});
    }
  } else if (runId) {
    const saved = await ctx.state.get({scopeKind: "run", scopeId: runId, stateKey: STATE_KEY});
    if (saved) { assignment = saved; issueId = saved.issueId; }
  }
  if (!assignment || !issueId) return false;
  const config = await ctx.config.get(event.companyId);
  const target = endpoint(config.orquestaUrl);
  const token = await ctx.secrets.resolve(config.orquestaToken, {
    companyId: event.companyId, configPath: "orquestaToken",
  });
  const body = JSON.stringify({
    id: event.eventId, type: event.eventType,
    flowRunId: assignment.flowRunId, stepId: assignment.stepId,
    attemptId: assignment.attemptId, sourceKey: assignment.sourceKey,
    references: {issueId, runId: runId || ""},
  });
  const response = await ctx.http.fetch(target, {
    method: "POST", redirect: "error", signal: AbortSignal.timeout(10000),
    headers: {"content-type": "application/json", authorization: `Bearer ${token}`}, body,
  });
  if (!response.ok) throw new Error(`Orquesta notification was not accepted (HTTP ${response.status})`);
  const receipt = await response.json();
  if (receipt.received !== true || receipt.attemptId !== assignment.attemptId) {
    throw new Error("Orquesta did not return a matching durable receipt");
  }
  return true;
}

export function installRelay(ctx) {
  for (const name of [...ISSUE_EVENTS, ...RUN_EVENTS]) {
    ctx.events.on(name, event => forwardEvent(ctx, event));
  }
}
