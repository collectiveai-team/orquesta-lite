# Examples

[`governed-pack/`](governed-pack/) is a runnable project skeleton for the pack
the binary ships: a `team.json`, a sample `features.md` contract, and the
`CONVENTIONS.md` that `team.json` points at.

The pack itself is **not** here. It is product, and it lives in
[`packs/development/`](../packs/development/) — `orq-lite init` installs from
there, so an edit to the pack changes what every new project gets.

```bash
orq-lite init --lang python /path/to/project
cd /path/to/project
orq-lite doctor
orq-lite flow list
orq-lite factory features.md
```

Custom workflows should be distributed as v2 packs. See
[`docs/pack-format.md`](../docs/pack-format.md) and
[`docs/activity-protocol.md`](../docs/activity-protocol.md).
