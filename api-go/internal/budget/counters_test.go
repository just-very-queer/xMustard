package budget

import "testing"

func TestCountersIncrement(t *testing.T) {
	before := Counters()
	NoteSpawn(SpawnCore)
	NoteSpawn(SpawnGit)
	NoteSpawn(SpawnGit)
	NoteSpawn(SpawnKind(99))
	NoteHashed(1000)
	NoteHashed(-5)
	NoteCapture(4096)
	after := Counters()
	if after.Spawns["core"]-before.Spawns["core"] != 1 || after.Spawns["git"]-before.Spawns["git"] != 2 ||
		after.SpawnsTotal-before.SpawnsTotal != 3 || after.BytesHashed-before.BytesHashed != 1000 ||
		after.Captures-before.Captures != 1 || after.CaptureBytes-before.CaptureBytes != 4096 {
		t.Fatalf("counter deltas: before %+v after %+v", before, after)
	}
	if after.Scope == "" {
		t.Fatal("counters must state their scope")
	}
}
