#!/usr/bin/env python3
"""Generate coder prompt variants. Only difference between arms is the inserted block."""
import pathlib, sys

EXP = pathlib.Path(__file__).parent
BASE = (EXP / "base/.orquestalite/packs/development/5/prompts/coder.md").read_text()

ANCHOR = "Inspect the existing code first."
assert ANCHOR in BASE, "anchor not found in pack coder.md"

CONVENTIONS = """## Hard conventions (authoritative — these override your defaults)

{{CONVENTIONS}}

When the block above is empty, infer style from the surrounding code. When it
names a rule, follow it exactly; a reviewer will check this change against it.

"""

MEMORY = """## What previous iterations learned about this repository

{{MEMORY}}

Treat this as a map, not as truth: it tells you where to look first. Verify
anything you are about to depend on.

"""

REPO_MAP = """## Repository map (generated this run, before you started)

{{REPO_MAP}}

Treat this as a map, not as truth: it tells you where to look first. Verify
anything you are about to depend on.

"""

ARMS = {
    "A0-baseline": "",
    "A1-conventions": CONVENTIONS,
    "A2-memory": MEMORY,
    "A3-repomap": REPO_MAP,
    "A4-all": CONVENTIONS + MEMORY + REPO_MAP,
}

out = EXP / "arms"
out.mkdir(exist_ok=True)
for name, block in ARMS.items():
    text = BASE.replace(ANCHOR, block + ANCHOR) if block else BASE
    p = out / f"coder-{name}.md"
    p.write_text(text)
    extra = len(text) - len(BASE)
    print(f"{name:<18} prompt bytes={len(text):>5}  (+{extra} vs baseline)  placeholders="
          f"{[v for v in ('CONVENTIONS','MEMORY','REPO_MAP') if '{{'+v+'}}' in text]}")
