#!/usr/bin/env python3
"""Regenerate the SHA-256 file digests in pack/pack.json.

Run from anywhere: python3 examples/governed-pack/regen-digests.py
Rewrites pack.json's "files" map from the actual pack/ directory contents
(every file except pack.json itself), preserving apiVersion/name/version.
"""
import hashlib
import json
import pathlib

pack_dir = pathlib.Path(__file__).resolve().parent / "pack"
manifest_path = pack_dir / "pack.json"
manifest = json.loads(manifest_path.read_text())

files = {}
for path in sorted(pack_dir.rglob("*")):
    if path.is_dir() or path == manifest_path:
        continue
    relative = path.relative_to(pack_dir).as_posix()
    files[relative] = hashlib.sha256(path.read_bytes()).hexdigest()

manifest["files"] = files
manifest_path.write_text(json.dumps(manifest, indent=2, sort_keys=False) + "\n")
print(f"pack.json: {len(files)} files digested")
