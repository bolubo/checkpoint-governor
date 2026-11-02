package simulate

import (
	"time"

	"github.com/bolubo/checkpoint-governor/internal/policy"
)

// WriteModel prices one write from what it contains, how many streams carry it, and what the
// storage charges before any byte moves. It exists so the axes inside a write can be studied in
// replay before any producer changes: shards, latency, and the per-write overhead that sets the
// smallest honest period.
//
// It is an environment property, not a measurement: a generated trace knows its storage, a recorded
// one carries only what happened. When a trace has no model, the replay charges the recorded costs.
type WriteModel struct {
	// Overhead is the per-write cost that does not depend on bytes: metadata operations, latency,
	// the manifest. Once the period gets short, this is what sets the floor, not bandwidth.
	Overhead time.Duration
	// Bandwidth is bytes per second per stream.
	Bandwidth float64
	// Aggregate caps the bytes per second across all streams; zero means no cap.
	Aggregate float64
	// Streams is how many parallel streams the writer uses by default.
	Streams int
	// RequestBytes and Latency price the round trips: a writer that sends RequestBytes per request
	// pays Latency for each one, and parallel streams pay them at the same time. This is what makes
	// a latency-bound mount different from a bandwidth-bound one.
	RequestBytes int64
	Latency      time.Duration
	// FullBytes is one complete snapshot. A weights-only checkpoint is a smaller FullBytes, not a
	// second field: nothing in the contract can ask for one (the decision carries a kind, not a
	// content).
	FullBytes int64
	// DeltaBytesPerSecond is how many bytes a delta carries per second of training it covers.
	DeltaBytesPerSecond float64
}

// streams is how many parallel streams this writer uses. Zero means one.
func (m WriteModel) streams() int {
	if m.Streams > 0 {
		return m.Streams
	}
	return 1
}

// Cost prices one write. The aggregate cap is what the volume can absorb, which is why adding
// streams stops helping at some point.
func (m WriteModel) Cost(kind policy.WriteKind, coverage time.Duration) time.Duration {
	bytes := m.FullBytes
	if kind == policy.WriteDelta {
		bytes = int64(m.DeltaBytesPerSecond * coverage.Seconds())
	}
	return m.cost(bytes, m.streams())
}

func (m WriteModel) cost(bytes int64, streams int) time.Duration {
	bw := m.Bandwidth * float64(streams)
	if m.Aggregate > 0 && bw > m.Aggregate {
		bw = m.Aggregate
	}
	transfer := time.Duration(0)
	if bw > 0 {
		transfer = time.Duration(float64(bytes) / bw * float64(time.Second))
	}
	// Round trips: each request costs its latency, and parallel streams pay them together.
	roundTrips := time.Duration(0)
	if m.RequestBytes > 0 && m.Latency > 0 {
		requests := bytes / m.RequestBytes
		if bytes%m.RequestBytes != 0 {
			requests++
		}
		roundTrips = time.Duration(int64(m.Latency) * requests / int64(streams))
	}
	return m.Overhead + roundTrips + transfer
}

// FullCost is the cost of one full snapshot on this storage.
func (m WriteModel) FullCost() time.Duration {
	return m.cost(m.FullBytes, m.streams())
}

// DeltaShareAt is the marginal cost of a delta per second of coverage: the alpha of
// the retired interval arithmetic (0014). It is what a policy measures, and it does not include the per-write
// overhead, which is why the overhead has its own field.
func (m WriteModel) DeltaShareAt() float64 {
	bw := m.Bandwidth * float64(m.streams())
	if m.Aggregate > 0 && bw > m.Aggregate {
		bw = m.Aggregate
	}
	if bw <= 0 {
		return -1
	}
	return m.DeltaBytesPerSecond / bw
}

// SmallTeamModel is the target user of V1: a 300 MB checkpoint with optimizer state on a mount that
// moves about 10 MB/s and charges one second per write. It is the model the V2 ladder is priced
// against in policy §7–8.
func SmallTeamModel() WriteModel {
	return WriteModel{
		Overhead:            time.Second,
		Bandwidth:           10 * 1000 * 1000,
		Streams:             1,
		FullBytes:           300 * 1000 * 1000,
		DeltaBytesPerSecond: 50 * 1000,
	}
}

// LatencyModel is a mount whose cost is dominated by round trips rather than bandwidth: 100 MB/s,
// 4 MB per request, 100 ms per request. With one stream a 300 MB snapshot pays 75 round trips; with
// four streams it pays them four at a time. The delta rate is set at the size of a dense delta (about
// its weights), so the delta axis stays closed here and the shard axis is what the replay prices.
func LatencyModel(streams int) WriteModel {
	return WriteModel{
		Overhead:            200 * time.Millisecond,
		Bandwidth:           100 * 1000 * 1000,
		Aggregate:           100 * 1000 * 1000,
		Streams:             streams,
		RequestBytes:        4 * 1000 * 1000,
		Latency:             100 * time.Millisecond,
		FullBytes:           300 * 1000 * 1000,
		DeltaBytesPerSecond: 6 * 1000 * 1000,
	}
}
