package budget

import "sync/atomic"

// Data-movement counters (PAR-EVAL-04). They are process-wide, monotonic since start,
// and cost one atomic add per event. They count what THIS process did: the children it
// spawned, the bytes it hashed, and the tool results it captured. Work done inside a
// child is not visible here (a Rust core call's own git spawns and file hashing, for
// example), and neither are agent runs, LSP servers or terminals, which are external
// processes reported on their own lines.

// SpawnKind classifies a child process this process started.
type SpawnKind int

const (
	// SpawnCore is a per-call xmustard-core bridge child.
	SpawnCore SpawnKind = iota
	// SpawnGit is a git child started directly by Go.
	SpawnGit
	// SpawnHelper is another tracked short-lived helper (ast-grep, agent CLI probes).
	SpawnHelper
	spawnKinds
)

var spawnKindNames = [spawnKinds]string{"core", "git", "helper"}

func (k SpawnKind) String() string {
	if k < 0 || k >= spawnKinds {
		return "unknown"
	}
	return spawnKindNames[k]
}

type dataCounters struct {
	spawns       [spawnKinds]atomic.Int64
	bytesHashed  atomic.Int64
	captures     atomic.Int64
	captureBytes atomic.Int64
}

var counters dataCounters

// NoteSpawn records one started child of kind k. Call it after Start succeeds.
func NoteSpawn(k SpawnKind) {
	if k >= 0 && k < spawnKinds {
		counters.spawns[k].Add(1)
	}
}

// NoteHashed records n bytes fed through a content hash.
func NoteHashed(n int64) {
	if n > 0 {
		counters.bytesHashed.Add(n)
	}
}

// NoteCapture records one captured tool result of rawBytes original bytes.
func NoteCapture(rawBytes int64) {
	counters.captures.Add(1)
	if rawBytes > 0 {
		counters.captureBytes.Add(rawBytes)
	}
}

// CounterSnapshot is the health view of the data-movement counters.
type CounterSnapshot struct {
	Spawns       map[string]int64 `json:"spawns"`
	SpawnsTotal  int64            `json:"spawns_total"`
	BytesHashed  int64            `json:"bytes_hashed"`
	Captures     int64            `json:"captures"`
	CaptureBytes int64            `json:"capture_bytes"`
	Scope        string           `json:"scope"`
}

const counterScope = "this process since start; work inside children (Rust core git spawns and hashing) and external processes (agent runs, LSP servers, terminals) are not counted here"

// Counters returns the current counter values.
func Counters() CounterSnapshot {
	s := CounterSnapshot{Spawns: make(map[string]int64, spawnKinds), Scope: counterScope}
	for k := SpawnKind(0); k < spawnKinds; k++ {
		n := counters.spawns[k].Load()
		s.Spawns[k.String()] = n
		s.SpawnsTotal += n
	}
	s.BytesHashed = counters.bytesHashed.Load()
	s.Captures = counters.captures.Load()
	s.CaptureBytes = counters.captureBytes.Load()
	return s
}
