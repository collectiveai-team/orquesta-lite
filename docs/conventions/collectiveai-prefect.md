# Collective AI — Prefect workflow conventions

How the team builds Prefect (v3) workflows, distilled from contract-aiment.
Mirror these patterns when writing flows, tasks, deployments, or the API code
that triggers them. Read alongside `collectiveai-python.md`.

## Layout

Worker code is its own top-level package area, separate from the API:

```
<pkg>/
  core/
    integrations/
      prefect.py            # client wrapper: run_flow, get_deployment_by_name, ...
      workflows/<name>.py   # per-workflow submit_* / get_*_result helpers
      workflows/meta/       # event-parsing models (WorkflowsResource, result models)
  workflows/                # the Prefect worker
    deploy.py               # prefect.serve(*deployments) entrypoint
    settings.py             # worker-side settings
    hooks/webhook.py        # flow state-change hook
    <workflow_name>/
      flow.py               # the @flow
      config.py             # per-workflow Config(BaseSettings) + `config` singleton
      tasks/                # one @task per file
      helpers/              # artifacts, etc.
      meta/models.py        # typed result models returned by the flow
```

The API only depends on `prefect-client`; the worker image installs the full
`workflows` dependency group (`prefect`, plus heavy libs like `docling`).

## Flow

Flows are `async`, return a typed pydantic result model, and take an optional
`webhook_url`. `name`/`version` come from the per-workflow `config`, never
hardcoded. Register **all five** state hooks so progress is reported uniformly:

```python
@flow(
    name=config.WORKFLOW_NAME,
    version=config.WORKFLOW_VERSION,
    persist_result=True,            # required to fetch the result later
    log_prints=True,
    on_completion=[webhook_event_flowrun],
    on_cancellation=[webhook_event_flowrun],
    on_failure=[webhook_event_flowrun],
    on_crashed=[webhook_event_flowrun],
    on_running=[webhook_event_flowrun],
)
async def process_document(
    document_id: str | uuid.UUID,
    webhook_url: str | None = None,
) -> ProcessDocumentResult:
    ...
```

- Coerce `str | uuid.UUID` ids at the top of the body.
- Use `get_run_logger()` inside flows/tasks (not the module `get_logger`).
- Run independent tasks concurrently with `asyncio.gather(...)`; `await`
  sequential ones directly. No subflows.

## Task

Default to a bare `@task`; name it only when the run name should be stable.
Async unless it is pure CPU-bound transformation. AI/IO tasks accept an
optional `artifact_key` and emit a markdown artifact when set:

```python
@task
async def classify_document(text: str, artifact_key: str | None = None) -> DocumentClassification:
    result = await ...
    if artifact_key:
        await generate_agent_artifact(key=artifact_key, ...)
    return result
```

Caching is done explicitly with `diskcache` inside the task body, not via
Prefect's `cache_key_fn`.

## Per-workflow config

Each workflow owns a `Config(BaseSettings)` with a module-level `config`
singleton holding `WORKFLOW_NAME`, `WORKFLOW_VERSION`, cache paths, model names,
and webhook retry/timeout/backoff settings:

```python
class Config(BaseSettings):
    model_config = ConfigDict(case_sensitive=True, env_file=env_files, extra="ignore")

    WORKFLOW_NAME: str = "process_document"
    WORKFLOW_VERSION: str = "0.1.0"
    WEBHOOK_MAX_RETRIES: int = 5
    WEBHOOK_TIMEOUT: int = 10
    WEBHOOK_BACKOFF_FACTOR: float = 2

config = Config()
```

## Deployment

Static deployments via `flow.to_deployment(name=..., version=...)` collected and
served together; infra (work pool, image) comes from docker-compose, not code:

```python
def main():
    static_deployments = [
        process_document.to_deployment(
            name=process_document_config.WORKFLOW_NAME,
            version=process_document_config.WORKFLOW_VERSION,
        ),
        ...
    ]
    prefect.serve(*static_deployments)
```

The deployment `name` must match the string the client passes to
`get_deployment_by_name(...)`.

## Triggering from FastAPI (fire-and-forget + webhook result)

The API never blocks on a flow. It submits via the client wrapper, stores the
flow-run id, and gets the result back through a webhook:

```python
# endpoint
webhook_url = f"{settings.WEBHOOK_HOST_URL}/api/v0/webhooks/process_document"
flow_run = await submit_document_processing_workflow(document_id=contract.id, webhook_url=webhook_url)
contract.workflows_id = flow_run.id

# core/integrations/workflows/process_document.py
async def submit_document_processing_workflow(document_id, webhook_url=None) -> FlowRun:
    deployment = await get_deployment_by_name("process_document")
    params = FlowRunParams(parameters={"document_id": str(document_id), "webhook_url": webhook_url})
    return await run_flow(deployment_id=deployment.id, params=params)
```

The flow's state hooks POST a Prefect `Event` to `webhook_url`; the webhook
endpoint parses it with `WorkflowsResource.from_event(event)`, updates the DB
row by `workflows_id`, and on `COMPLETED` calls `get_*_result(flow_run_id)`
(which reads `flow_run.state.result()`) to persist the output. The webhook
handler runs in a FastAPI `BackgroundTask`.

## Client wrapper (`core/integrations/prefect.py`)

Centralize Prefect-client calls here: `get_deployment_by_name`, a
`FlowRunParams` model (`parameters` + `job_variables`), `run_flow(...)` (calls
`run_deployment`, fires-and-forgets with `timeout=0` when a `webhook_url` is
set), `wait_and_cleanup`, and `get_flow_run_result`. Business code calls these,
never `run_deployment`/`get_client` directly.
