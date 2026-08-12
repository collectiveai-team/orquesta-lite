#!/usr/bin/env python3
"""One single-step flow per arm. Only the vars block differs."""
import json, pathlib

EXP = pathlib.Path(__file__).parent
ticket = (EXP / "ticket.json").read_text().strip()
state = (EXP / "state.json").read_text().strip()
repo_map = (EXP / "repo-map.txt").read_text().strip()

ARMS = ["A0-baseline", "A1-conventions", "A2-memory", "A3-repomap", "A4-all"]

out = EXP / "flows"
out.mkdir(exist_ok=True)
for arm in ARMS:
    vars_block = {
        "FEATURES_PATH": {"$ref": "inputs.features_path"},
        "WORKFLOW_STATE": state,
        "TICKET": ticket,
    }
    # REPO_MAP is not a runtime-injected var; the flow supplies it, standing in
    # for what a command.run step would produce. Only the arms whose prompt has
    # the placeholder need it, but supplying it everywhere keeps the vars map
    # identical except where the prompt actually consumes it.
    if "repomap" in arm or arm == "A4-all":
        vars_block["REPO_MAP"] = repo_map

    flow = {
        "apiVersion": "orq.dev/v2",
        "kind": "Flow",
        "metadata": {"name": f"coder-probe-{arm.split('-')[0].lower()}", "version": "1"},
        "inputs": {"features_path": {"schema": "schema:path@1"}},
        "steps": [
            {
                "id": "implement_ticket",
                "uses": "activity:agent.invoke@1",
                "with": {
                    "role": "coder",
                    "outputSchema": "schema:ticket-implementation@1",
                    "vars": vars_block,
                },
            }
        ],
        "outputs": {"implementation": {"$ref": "steps.implement_ticket.output"}},
    }
    p = out / f"{arm}.json"
    p.write_text(json.dumps(flow, indent=2))
    print(f"{arm:<18} {p.name:<20} vars={sorted(vars_block)}")
