# Running orquestalite in Docker

The image bundles `orq-lite` plus the three agent CLIs (`claude`, `codex`,
`gemini`) on a Node 22 slim base with git. Credentials are never baked into
the image — they are mounted from your home directory at runtime.

## 1. Authenticate once on the host

```bash
claude        # Anthropic login -> ~/.claude + ~/.claude.json
codex login   # OpenAI login   -> ~/.codex
gemini        # Google login   -> ~/.gemini
```

You only need the providers your `team.json` references.

## 2. Build the image

```bash
docker compose build
# pin CLI versions for reproducible images:
docker compose build --build-arg CLAUDE_CODE_VERSION=2.1.0 \
                     --build-arg CODEX_VERSION=0.48.0 \
                     --build-arg GEMINI_CLI_VERSION=0.43.0
```

## 3. Point it at a project

`TARGET_PROJECT` selects the repository the factory works on (defaults to the
current directory):

```bash
export TARGET_PROJECT=$HOME/code/my-app

docker compose run --rm factory init                  # scaffold team.json, prompts/
docker compose run --rm factory plan plan.md          # plan only
docker compose run --rm factory run                   # run loops
docker compose run --rm factory factory features.md   # full factory queue
docker compose run --rm factory factory --status
```

The credential mounts are read-write on purpose: the CLIs refresh OAuth
tokens in place. Nothing else from your home directory is exposed.

## 4. Dashboard

```bash
docker compose up dashboard
# open http://localhost:4173
```

The dashboard is read-only (it tails `.orquestalite/` state), so it can run
alongside a `factory` run against the same project mount.

## Browser-real verification (web apps)

Two layers use a real headless browser over `curl` for web apps:

- The **verifier** role (per-cycle, generic black-box) prefers a browser.
- The **feature-level visual verifier** runs once at feature close for slices
  the planner marked `visual: true`, driving [`agent-browser`](https://github.com/vercel-labs/agent-browser)
  (open → snapshot → assert → screenshot). On failure its findings become fix
  tasks and the feature re-runs, bounded by `limits.max_visual_rounds` (default 2).

Provide browser tooling one of two ways:

```bash
# agent-browser (preferred for visual features)
npm i -g agent-browser && agent-browser install

# or bake chromium + playwright into the Docker image (≈400MB extra)
docker compose build --build-arg INSTALL_PLAYWRIGHT=1
```

Without any browser tooling, the verifier falls back to HTML fetching and marks
the limitation in its check evidence.

## Notes

- The container runs as uid 1000 (`node`). If your host user has a different
  uid, ensure the project and credential dirs are writable by uid 1000, or
  override with `user:` in a compose override file.
- `--dangerously-skip-permissions` / `--yolo` style flags are controlled per
  agent in `team.json`, not by the image. The container itself is the
  sandbox boundary.
- For CI, prefer API-key auth via environment (`ANTHROPIC_API_KEY`,
  `OPENAI_API_KEY`, `GEMINI_API_KEY`) added to the `factory` service
  `environment:` block instead of mounting interactive logins.
