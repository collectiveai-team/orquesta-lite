# Collective AI — Python house style

Conventions distilled from the team's existing repos (qxo, asr-websocket-server,
edge-ai-poc, llm-tools, websocket-asr). Mirror these so generated code looks
like the team wrote it. Point `conventions_file` in `team.json` at this file to
inject it into the coder/critic/reviewer prompts.

When a rule below conflicts with what the specific repo you are editing already
does, follow the repo — these are the defaults, not overrides.

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

## Config & constants

- Read config at module import time as `UPPER_SNAKE` constants with inline
  defaults, not via a settings class:
  ```python
  WEAVIATE_HOST = os.getenv("WEAVIATE_HOST", "localhost")
  WEAVIATE_PORT = int(os.getenv("WEAVIATE_PORT", "8080"))
  ```
- Secrets live in `.secrets/.env`, never committed. Dev defaults in `.env`.

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

## Docstrings & comments

- Sparse, single-line plain-English docstrings on classes and non-trivial
  methods (`"""stop the asr process"""`). No NumPy/Google parameter blocks.
- Inline comments are fine for section headers (`# Silero specific parameters`)
  and unit clarifications.

## Tooling

- **uv** for new repos (`[project]` PEP 621, `uv.lock` committed); poetry in
  older ones. Always commit the lockfile.
- **ruff** line-length 100, rules `["E", "F", "I", "UP", "B"]`, plus **mypy**
  for new repos. (Legacy repos: black line-length 88 + isort + flake8 — match
  the repo.)
- **pytest**, `testpaths = ["test"]` (singular). Dev deps always include
  `pytest`, `python-dotenv`.
- Every repo has a `Makefile` and `docker-compose.yml`; prefer existing `make`
  targets (`core-build`, `build-<service>`, `start-<service>`, `jupyter-run`)
  over ad-hoc commands.
