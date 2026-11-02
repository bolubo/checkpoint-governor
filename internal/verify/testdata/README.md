# Fixtures

Each tree is a shape a real checkpoint directory takes, and the state it is meant to produce. The
classifier is tested against these, so a fixture that stands for two states at once would make the test
lie.

| tree | meant to produce |
|---|---|
| `sealed-ok` | `verified` with a content check, `sealed` without one — every shard present, sizes and hashes agree |
| `short-shard` | `broken` / short — a write killed mid-shard, one file truncated against its manifest |
| `torn-tail` | `broken` / hash — right size, wrong last bytes: the kill that tore the final block |
| `no-manifest` | `waiting`, then `expired` past the deadline — a write in flight, or a writer that never sealed |
| `stale-generation` | `broken` / stale — whole and sealed, but older than the generation being asked for |
| `evil-path` | refused — a shard name that climbs out of the generation directory |
| `unknown-field` | refused — a manifest from a newer writer; half-reading it would honour half a contract |
| `sealed-no-tail` | `broken` / hash — no declared tail hash, so the cheap check has nothing to compare and must not guess |

`sealed-ok` and `stale-generation` also round-trip byte-for-byte through `Parse` and `Marshal`: the fixture
is the contract, and a parser that reshapes it would make "the file says X" untrustworthy.

Shard names are **relative to the generation directory** — a manifest that named its own parent would break
the moment the directory was renamed.

| `extra-file` | `verified`, plus one unlisted file reported — the only visible trace of a second writer |
