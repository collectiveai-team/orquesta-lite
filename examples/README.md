# Examples

The canonical example is [`governed-pack/`](governed-pack/). It is also the source embedded by `orq-lite init`, so the example, tests, and shipped development pack cannot drift into separate implementations.

```bash
orq-lite init --lang python /path/to/project
cd /path/to/project
orq-lite doctor
orq-lite flow list
orq-lite factory features.md
```

Custom workflows should be distributed as v2 packs. See [`docs/pack-format.md`](../docs/pack-format.md) and [`docs/activity-protocol.md`](../docs/activity-protocol.md).
