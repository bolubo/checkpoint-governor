# Fixtures

Each tree is a shape a real checkpoint directory takes, and the state it is meant to produce. The
classifier (Phase 1) is tested against these, so a fixture that stands for two states at once would make
the test lie.

| tree | meant to produce |
|---|---|
| `sealed-ok` | `sealed`, then `verified` — every shard present, sizes right, hashes match |
| `short-shard` | `broken` / short — a write killed mid-shard, one file truncated against its manifest |
| `torn-tail` | `broken` / hash — right size, wrong last bytes: the kill that tore the final block |
| `no-manifest` | `waiting` — a generation directory with no manifest is a write in flight, not a failure |
| `stale-generation` | `broken` / stale — whole and sealed, but older than the generation being asked for |
| `evil-path` | refused — a shard name that climbs out of the generation directory |
| `unknown-field` | refused — a manifest from a newer writer; half-reading it would honour half a contract |

`sealed-ok` and `stale-generation` also have to round-trip byte-for-byte through `Parse` and `Marshal`:
the fixture is the contract, and a parser that reshapes it would make "the file says X" untrustworthy.

Regenerate by hand when the schema changes; the trees are small on purpose, so a diff is readable.
