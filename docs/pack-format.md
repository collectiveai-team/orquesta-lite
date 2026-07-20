# Local workflow pack format v1

A pack is an already-installed local directory. `orq-lite` never downloads a
pack while compiling or resuming a workflow.

```text
pack.json
flows/
subflows/
schemas/
policies/
prompts/
activities/
```

`pack.json` uses `apiVersion: orq.pack/v1`, a name, semantic version, and a map
of relative file paths to SHA-256 digests. Absolute paths and traversal are
rejected. Every run stores the compiled IR, resolved schemas, activity specs,
resource digests, and policy snapshot so resume never selects newer content.
The verified `pack.json` is also embedded as a `PackSnapshot` in the IR hash.
Resume resolves that exact installed name/version and rejects a replacement
whose manifest digest differs, even if it reused the same version string.

Activity manifests contain `protocol: orq.activity/v1`, argv, and the activity
spec. Orquesta is responsible for installing and selecting pack versions;
orq-lite only validates and executes a local version.
