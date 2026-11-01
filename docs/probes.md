# Probe intervals

A probe turns one signal into a condition reading. Its name is the condition it feeds.
The [condition cards](conditions.md) define what each reading means.
This page explains the polling cost and delay instead of repeating those definitions.

## Cadences and tradeoffs

These are the defaults in [`adapters.All`](../internal/produce/adapters/adapters.go).
The tradeoffs below describe what the reads do. They are not measurements showing that a
particular interval is optimal.

| Probe / condition | Default interval and source | What it reads | Why the cadence matters; larger / smaller intervals |
| --- | --- | --- | --- |
| `risk.preempt` | 5s, [`preempt.go`](../internal/produce/adapters/preempt.go) | The observer's summary and unit record, projected into a platform announcement. | An announcement spends the job's remaining grace period. Larger: less time remains to write. Smaller: more world-file reads, with no fresher input until the observer writes. |
| `risk.shutdown` | 5s, [`job.go`](../internal/produce/adapters/job.go) | Presence of `.ckpt-governor/stopping`. | The job has a stop deadline. Larger: more of its grace period passes before detection. Smaller: more file stats. |
| `store.low-space` | 5s, [`store.go`](../internal/produce/adapters/store.go) and [`storeEvery`](../internal/produce/adapters/adapters.go) | Free bytes on the checkpoint filesystem. | The next write needs room for the old and new copies. Larger: free space can fall further before detection. Smaller: more filesystem queries. |
| `store.blocked` | 5s, [`store.go`](../internal/produce/adapters/store.go) and [`storeEvery`](../internal/produce/adapters/adapters.go) | Whether a temporary file can be created and removed in the checkpoint directory. | A refusal is a diagnostic. Larger: it can remain unreported longer. Smaller: more temporary-file creation and removal on the checkpoint volume. |
| `job.ending` | 10s, [`job.go`](../internal/produce/adapters/job.go) | Presence of `.ckpt-governor/ending`. | Training completion needs a final write. Larger: that request can be delayed. Smaller: more file stats. |
| `host.memory-pressure` | 10s, [`host.go`](../internal/produce/adapters/host.go) | cgroup v2 `memory.current` and `memory.max`. | Memory pressure is a local risk signal. Larger: the job can reach its limit between reads. Smaller: more cgroup reads; this does not guarantee notice before an abrupt kill. |
| `host.cpu-throttled` | 10s, [`cpu.go`](../internal/produce/adapters/cpu.go) | `throttled_usec` and `usage_usec` from cgroup v2 `cpu.stat`. | This is a diagnostic based on cumulative counters. Larger: reporting is delayed. Smaller: more reads of the same counters, not a shorter measurement window. |
| `job.step-slow` | 10s, [`slow.go`](../internal/produce/adapters/slow.go) | `.ckpt-governor/step-rate` and `step-baseline`. | The job supplies both measurements. Larger: slower steps can remain unreported longer. Smaller: more reads, with no new measurement until the job updates the files. |
| `store.slow` | 10s, [`slow.go`](../internal/produce/adapters/slow.go) | `.ckpt-governor/write-rate` and `write-baseline`. | The job supplies the write measurements. Larger: slower writes can remain unreported longer. Smaller: more reads of job-produced files. |
| `risk.eviction-likely` | 15s, [`eviction.go`](../internal/produce/adapters/eviction.go) | The observer's projected resource ratios, pending higher-priority pods and cordon state. | This is an inference, not an announcement. Larger: pressure can grow before the next reading. Smaller: more world-file reads; polling cannot make the inference certain. |
| `host.gpu-fault` | 5s by default, [`absent.go`](../internal/produce/adapters/absent.go) via `storeEvery` | Nothing; returns `Absent` on this build. | The row keeps the condition visible as absent. Larger / smaller: changes sampler bookkeeping frequency, not GPU detection. |
| `host.gpu-hot` | 5s by default, [`absent.go`](../internal/produce/adapters/absent.go) via `storeEvery` | Nothing; returns `Absent` on this build. | The row keeps the condition visible as absent. Larger / smaller: changes sampler bookkeeping frequency, not GPU detection. |
| `net.long-outage` | 5s by default, [`absent.go`](../internal/produce/adapters/absent.go) via `storeEvery` | Nothing; returns `Absent` on this build. | The row keeps the condition visible as absent. Larger / smaller: changes sampler bookkeeping frequency, not network detection. |

`watch.broken` is derived by the [sampler](../internal/produce/sampler.go) each round.
It has no separate probe or interval. A probe not yet due retains its last reading.
Lengthening an interval also lengthens the time needed to accumulate consecutive unreadable
readings. Shortening it spends that failure budget sooner.

The storage cadence can be supplied through `storeEvery` in `conditions.json` or
`--store-every`; see [`config.go`](../internal/produce/adapters/config.go) and
[`run.go`](../cmd/ckptctl/run.go). The other active probe intervals above are fixed in
their adapters. This page adds no configuration setting.

## Bound

The probe interval is one part of detection latency, not a platform-to-write deadline.
For `risk.preempt`, the path is:

1. The platform records the announcement.
2. The observer reads the cluster and publishes the world files.
3. A controller round samples the probe when it is due.
4. The job reads the published plan and performs the write, if it fits.

The observer defaults to a 5s delay between reads
([`contrib/observer/main.go`](../contrib/observer/main.go)). The preemption probe's interval
is also 5s. With timely rounds and successful reads, polling therefore adds up to one
observer interval plus one probe interval before the controller sees a new announcement.
Cluster reads, world publication and controller work take additional time.

[`Sampler.Sample`](../internal/produce/sampler.go) only checks whether a probe is due when
called. It schedules the next read from that round's time. A slower or delayed controller
can read later than the interval in the table. `run --every` defaults to 5s and waits after
each round; faster in-flight rounds do not bypass a probe's own cadence
([`run.go`](../cmd/ckptctl/run.go)).

For `risk.shutdown`, there is no cluster observer in the path: the job writes the local
`stopping` file. Probe cadence, controller scheduling and the job's response still matter.

The detection-plus-write arithmetic in [decision 0019](decisions/0019-detection-bound.md)
must be read with these upstream and scheduling costs. A polling interval alone does not
guarantee a completed checkpoint within that time. Unreadable or stalled inputs do not
provide the healthy-read bound. A death with no announcement has no detection guarantee.

