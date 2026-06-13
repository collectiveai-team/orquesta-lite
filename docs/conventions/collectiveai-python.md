# Collective AI — Python house style

Conventions distilled from the team's repos (qxo, asr-websocket-server,
edge-ai-poc, llm-tools, websocket-asr, catalyst, contract-aiment). Mirror these
so generated code looks like the team wrote it. Point `conventions_file` in
`team.json` at this file to inject it into the coder/critic/reviewer prompts.
For Prefect workflow code, also see `collectiveai-prefect.md`.

When a rule below conflicts with what the specific repo you are editing already
does, follow the repo — these are the defaults, not overrides. The team has
been migrating (poetry→uv, `Optional`→`X | None`, `os.getenv`→pydantic-settings,
black/flake8→ruff+pyrefly); where old and new differ, the **new** convention
below is the target for new code.

## Package layout

- **Flat layout, no `src/`.** The main package sits at the repo root, named
  after the project in `snake_case` (`awss/`, `llm_tools/`, `quedgeai/`, `qxo/`).
- **`<pkg>/meta/`** holds the abstract layer: ABCs, interfaces, and shared
  Pydantic data models. New interfaces go here.
- **`<pkg>/logger/`** holds the logging wrapper (see below).
- Project root also has: `notebooks/`, `resources/` (gitignored, with a
  `.gitkeep`), `reports/`, and `test/` (singular).

## Typing

- Python **3.10+** floor. Prefer modern syntax: `X | None` over `Optional[X]`,
  `list[X]`/`dict[K, V]` over `typing.List`/`Dict`. (Older files use the legacy
  forms — match the file you are in, but write new code with the modern syntax.)
- Annotate public function parameters and return types.
- Use `TYPE_CHECKING` guards for import cycles:
  ```python
  from typing import TYPE_CHECKING
  if TYPE_CHECKING:
      from qxo.backend import Backend
  ```
- No `from __future__ import annotations` — the team uses runtime-valid syntax.

## Data models

- **Pydantic v2 `BaseModel` for every object that crosses a boundary.** Never
  pass bare dicts or dataclasses for structured data.
  ```python
  from pydantic import BaseModel, Field

  class Customization(BaseModel):
      """Model to represent customizations like 'no LETTUCE'."""
      name: str
      options: list[str] = Field(default_factory=list)
  ```
- Use `Field(default_factory=...)` for mutable defaults. Use `model_dump_json()`
  / `model_dump(mode="json")` for serialization.

## Interfaces

- One ABC per subsystem, suffixed `Interface` (or a plain ABC), living in
  `<pkg>/meta/`. Concrete implementations subclass it.
  ```python
  from abc import ABC, abstractmethod

  class TextEncoder(ABC):
      @abstractmethod
      def encode(self, documents: list[str]) -> list[list[float]]: ...
  ```
- **Constructor dependency injection**: pass collaborators into `__init__`,
  never construct them inside methods.
  ```python
  class Retrieval(ABC):
      def __init__(self, encoder: TextEncoder, collection_name: str):
          self.encoder = encoder
  ```

## Logging

- Use the shared `get_logger` wrapper (copy the existing `logger/logger.py` if
  the repo doesn't have one — it is identical across repos):
  ```python
  from <pkg>.logger import get_logger
  logger = get_logger(__name__)
  ```
  The wrapper drives `rich.logging.RichHandler` and reads `LOG_LEVEL` from env.
- **f-strings in log calls**, never `%`-style or `.format()`. The team uses an
  `=>` separator for key/value:
  `logger.info(f"Creating collection => {self.collection_name}")`.
- `rich` is a runtime dependency, not just dev.

## Naming

- `snake_case` functions, methods, modules, and file names.
- `PascalCase` classes; `Interface` suffix for abstract bases.
- `UPPER_SNAKE_CASE` module-level constants.
- `_` prefix for private methods. Extract long functions into `_helper`
  methods rather than leaving 60-line bodies.
- `class Foo(str, Enum)` for finite string sets — never bare string literals in
  comparisons.

## Config (pydantic-settings, not os.getenv)

Centralize configuration in a typed `pydantic_settings.BaseSettings` class —
`os.getenv`/`os.environ` scattered through the code gives no validation, no type
coercion, and no `.env` support, and is a flagged anti-pattern. (Older repos
still use module-level `os.getenv` constants; do not add new ones.)

```python
from pydantic_settings import BaseSettings, SettingsConfigDict

class Settings(BaseSettings):
    database_url: str
    debug: bool = False
    max_connections: int = 10

    model_config = SettingsConfigDict(env_file=".env")

settings = Settings()
```

- One module-level `settings = Settings()` singleton. Use `SecretStr` for
  secrets, a `@model_validator(mode="after")` for cross-field defaults.
- Secrets live in `.env` (`.secrets/.env` in container repos), never committed;
  commit a `.env.example` template.

## Imports

- Group: stdlib, blank line, third-party, blank line, local — and let
  ruff/isort (profile `black`) enforce order.
- Absolute imports across subpackages; relative only within a subpackage.
- Flatten public API via `__init__.py` re-exports, suppressed with a bare
  `# noqa`:
  ```python
  from .logger import get_logger  # noqa
  ```

## Errors

- Raise stdlib exceptions: `ValueError` for bad arguments / not-found,
  `RuntimeError` for operational failures, `NotImplementedError` for stubs.
  Chain with `from e` when re-wrapping.
- `assert isinstance(...)`/length asserts with a message are the team's
  lightweight input guards in `process()`-style methods.

## Async

- Use `async def` only where the framework forces it (FastAPI routes,
  websocket handlers). Annotate their return types. Offload blocking work with
  `asyncio.to_thread` / threads; gather concurrency with `asyncio.gather`.

## Comments (minimal) & docstrings

- **Avoid comments; write self-documenting code.** Comment only what genuinely
  needs explaining — a non-obvious workaround, an external constraint, a
  performance trade-off. Never restate what the code already says, and never
  leave commented-out code (the linter eradicates it):
  ```python
  # Bad
  counter += 1  # increment counter by one

  # Good — the comment explains a non-obvious external constraint
  page = requested_page - 1  # external API uses 1-based page indexing
  ```
- Docstrings: Google convention (enforced by ruff pydocstyle). Short, plain,
  imperative first line ending in a period.
- Organize long files with **one** consistent separator style — light
  `# --- Validation ---` (80 wide), bold `###...` blocks, or `# MARK: ...`
  for IDE navigation. Do not mix styles within a file.

## Dependencies & build

- **uv** for new repos (`[project]` PEP 621, `uv.lock` committed,
  `hatchling`/`setuptools` build backend); poetry in older ones. Always commit
  the lockfile. Pin `requires-python` (newest repos pin a single minor, e.g.
  `>=3.12,<3.13` / `==3.13.*`).
- **pytest** with `testpaths` pointed at the repo's test dir
  (`["test"]`/`["backend/tests"]`), `asyncio_mode = "auto"` for async repos.

## Quality gate (prek pre-commit) — run before every commit

New repos enforce quality with **prek** (a fast pre-commit runner). This is the
expected setup; wire new repos the same way and never commit code that fails it.

```bash
make tools-install   # uv tool install prek ruff pyrefly ast-grep-cli
make prek-setup      # uvx prek install   (installs the git hooks)
make quality-gate    # format -> lint -> typecheck -> scan -> test
```

`prek.toml` (same schema as `.pre-commit-config.yaml`) runs, in order:
`uv-lock`; the standard `pre-commit-hooks` (`check-merge-conflict`,
`check-added-large-files --maxkb=750`, `check-toml`, `check-yaml --unsafe`,
`end-of-file-fixer`, `trailing-whitespace`); `validate-pyproject`;
`ruff-format`; `ruff --fix --exit-non-zero-on-fix`; `pyrefly check`;
`ast-grep scan`.

- **ruff**, line-length **100**, broad rule set — beyond `E,F,B,I,UP` the team
  enables `SIM,RET,C4,DTZ,PTH,LOG,G,S,C90,RUF,FAST,ERA001` and the `D`
  pydocstyle rules with `convention = "google"`; `mccabe.max-complexity = 10`;
  per-file-ignore `S101` (asserts) in tests. `ERA001` means **no commented-out
  code**.
- **pyrefly** is the type checker for new repos (mypy in slightly older ones,
  black/isort/flake8 line-88 in the legacy ones — match the repo you are in).
- **ast-grep** runs structural lint rules from `ast-rules/` (configured by
  `sgconfig.yml`). Add a rule here when a pattern must be enforced structurally.
- Makefiles also carry the docker/run targets — prefer existing `make` targets
  (`core-build`, `build-<service>`, `start-<service>`, `jupyter-run`,
  `quality-gate`) over ad-hoc commands.

## Third-party integrations

Wrap external services (Prefect, vector DBs, LLM providers) in a dedicated
`<pkg>/core/integrations/<service>.py` (or `<pkg>/integrations/`) module rather
than calling their SDKs directly from business logic. See
`collectiveai-prefect.md` for the Prefect workflow conventions.
