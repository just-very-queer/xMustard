package budget

import "sync/atomic"

// Data-movement counters (PAR-EVAL-04). They are process-wide, monotonic since start,
// and cost one atomic add per event. They count what THIS process did: the children it
// spawned, the content it hashed (request bodies, tool results and file contents; short
// fingerprints over ids and metadata are not counted), and the tool results it
// captured. Work done inside a child is not in those counts; the Rust core reports its
// own per call (git spawns, listings, stats, hashing, loaded rows), and core_passes sums
// those reports. Agent runs, LSP servers and terminals are external processes reported
// on their own lines.

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
	corePasses   [corePassFields]atomic.Int64
}

// CorePasses sums the pass counters the Rust core reported per call (WS-RT1): what
// core calls listed, statted, hashed, spawned and loaded. Calls counts every core result
// Go read; Counted the ones that carried counters (an array result carries none).
type CorePasses struct {
	Calls        int64 `json:"calls"`
	Counted      int64 `json:"counted"`
	FilesListed  int64 `json:"files_listed"`
	FilesStatted int64 `json:"files_statted"`
	FilesHashed  int64 `json:"files_hashed"`
	BytesHashed  int64 `json:"bytes_hashed"`
	GitSpawns    int64 `json:"git_spawns"`
	RowsLoaded   int64 `json:"rows_loaded"`
}

const corePassFields = 8

func (p *CorePasses) fields() [corePassFields]*int64 {
	return [corePassFields]*int64{&p.Calls, &p.Counted, &p.FilesListed, &p.FilesStatted,
		&p.FilesHashed, &p.BytesHashed, &p.GitSpawns, &p.RowsLoaded}
}

// Minus is p less an earlier snapshot.
func (p CorePasses) Minus(earlier CorePasses) CorePasses {
	out := p
	of, ef := out.fields(), earlier.fields()
	for i := range of {
		*of[i] -= *ef[i]
	}
	return out
}

// NoteCorePasses adds one core call's reported counters (zero when it reported none;
// set Counted when it did).
func NoteCorePasses(p CorePasses) {
	p.Calls = 1
	for i, f := range p.fields() {
		counters.corePasses[i].Add(*f)
	}
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
	CorePasses   CorePasses       `json:"core_passes"`
	Scope        string           `json:"scope"`
}

const counterScope = "this process since start; bytes_hashed covers content Go hashes (request bodies, tool results, file contents), not short fingerprints over ids or metadata; work inside children is not counted in spawns or bytes_hashed: core_passes sums what each Rust core call reported of its own git spawns, listings, stats, hashing and loaded rows; external processes (agent runs, LSP servers, terminals) are not counted here"

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
	for i, f := range s.CorePasses.fields() {
		*f = counters.corePasses[i].Load()
	}
	return s
}
