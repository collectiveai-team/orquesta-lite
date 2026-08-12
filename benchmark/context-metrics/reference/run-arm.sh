#!/bin/bash
# Run one experiment arm from a pristine repo state and snapshot everything.
# usage: run-arm.sh <arm> <rep>
set -uo pipefail
ARM="$1"; REP="${2:-1}"
EXP="$(cd "$(dirname "$0")" && pwd)"
REPO="$EXP/base"
TAG="${ARM}-r${REP}"
OUT="$EXP/results/$TAG"

case "$ARM" in
  A0-baseline)    FLOW=ctxprobe/coder-probe-a0@1; MEM=no  ;;
  A1-conventions) FLOW=ctxprobe/coder-probe-a1@1; MEM=no  ;;
  A2-memory)      FLOW=ctxprobe/coder-probe-a2@1; MEM=yes ;;
  A3-repomap)     FLOW=ctxprobe/coder-probe-a3@1; MEM=no  ;;
  A4-all)         FLOW=ctxprobe/coder-probe-a4@1; MEM=yes ;;
  A5-repomapv2)   FLOW=ctxprobe/coder-probe-a5@1; MEM=no  ;;
  *) echo "unknown arm $ARM"; exit 2 ;;
esac

rm -rf "$OUT"; mkdir -p "$OUT"
cd "$REPO" || exit 1

# --- pristine repo ---
git reset -q --hard exp-base
git clean -qfd -e .venv -e .orquestalite
rm -rf .orquestalite/results .orquestalite/runs .orquestalite/run.log \
       .orquestalite/workflows.db .orquestalite/sessions.json .orquestalite/memory.md

# --- arm-specific inputs ---
if [ "$MEM" = yes ]; then cp "$EXP/memory-seed.md" .orquestalite/memory.md; fi

python3 - "$EXP/arms/coder-$ARM.md" <<'PY'
import json, pathlib, sys
prompt = sys.argv[1]
t = json.load(open("team.json"))
t["agents"]["claude_sonnet"] = {
    "provider": "claude", "model": "claude-sonnet-5",
    "dangerously_skip_permissions": True,
    "rate_limit_pattern": "(?i)(rate_?limit|429|quota|session limit|usage limit)",
}
t["roles"]["coder"] = {
    "agents": ["claude_sonnet"], "prompt": prompt,
    "result_path": ".orquestalite/results/coder.json", "timeout_seconds": 2400,
}
t["lint_argv"] = ["uv", "run", "ruff", "check", "."]
t["test_argv"] = ["uv", "run", "pytest", "-q"]
json.dump(t, open("team.json", "w"), indent=2)
PY

echo "=== $TAG === flow=$FLOW memory=$MEM prompt=coder-$ARM.md"
START=$(date +%s)
"$EXP/orq-lite" flow run "$FLOW" features_path=features.md > "$OUT/flow.stdout" 2> "$OUT/flow.stderr"
RC=$?
END=$(date +%s)
echo "exit=$RC wall=$((END-START))s"

# --- gates, measured by us not by the agent ---
uv run ruff check . > "$OUT/ruff.txt" 2>&1; echo "$?" > "$OUT/ruff.exit"
uv run pytest -q  > "$OUT/pytest.txt" 2>&1; echo "$?" > "$OUT/pytest.exit"

# --- snapshot evidence ---
git diff --stat > "$OUT/diff.stat" 2>&1
git status --short > "$OUT/status.txt" 2>&1
git diff > "$OUT/diff.patch" 2>&1
{ git status --short | awk '$1=="??"{print $2}' | while read -r f; do
    echo "--- NEW FILE: $f"; cat "$f"; done; } > "$OUT/newfiles.txt" 2>&1
cp -R .orquestalite/runs "$OUT/runs" 2>/dev/null
cp .orquestalite/results/coder.json "$OUT/coder.result.json" 2>/dev/null
cp .orquestalite/run.log "$OUT/run.log" 2>/dev/null
cp team.json "$OUT/team.used.json" 2>/dev/null

cat > "$OUT/meta.json" <<EOF
{"arm":"$ARM","rep":$REP,"flow":"$FLOW","memory_seeded":"$MEM","flow_exit":$RC,
 "wall_s":$((END-START)),"ruff_exit":$(cat "$OUT/ruff.exit"),"pytest_exit":$(cat "$OUT/pytest.exit")}
EOF
echo "ruff=$(cat "$OUT/ruff.exit") pytest=$(cat "$OUT/pytest.exit") -> $OUT"
