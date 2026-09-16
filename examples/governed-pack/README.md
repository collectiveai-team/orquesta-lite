# Governed pack example project

The files a project needs around the shipped `development` pack, kept runnable
so the pack's flows always have a real caller to validate against.

| File | What it is |
|---|---|
| `team.json` | Role-to-agent bindings and prompt paths, pointing into the installed pack under `.orquestalite/packs/development/<version>/`. |
| `features.md` | A sample contract written the way [the guide](../../guide.md) describes: one `##` heading per vertical slice. |
| `CONVENTIONS.md` | The house style injected into every role prompt as `{{CONVENTIONS}}`, referenced by `team.json`'s `conventions_file`. |

The pack these point at — flows, subflows, prompts, schemas, policies — is
[`packs/development/`](../../packs/development/). Edit it there, then run
`python3 packs/regen-digests.py`.
