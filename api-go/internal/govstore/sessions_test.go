package govstore

import (
	"context"
	"errors"
	"slices"
	"testing"
)

// The newest ledger rows of a kind are read per session or across a workspace's most
// recently seen sessions, never another workspace's; a bound trims the oldest rows of
// the kinds it names; only the session's principal ends it (WS-33).
func TestRecentSessionEventsTrimAndEnd(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, newClock())
	a := Actor{Principal: "agent-a"}
	mustUpdate(t, s, func(tx Tx) error {
		for _, ses := range []struct{ id, ws string }{{"c:s1", "ws1"}, {"c:s2", "ws1"}, {"c:s3", "ws1"}, {"c:x", "ws2"}} {
			if _, err := tx.UpsertSession(ctx, SessionInput{ID: ses.id, WorkspaceID: ses.ws}, a); err != nil {
				return err
			}
			for _, kind := range []string{"tool_call", "tool_call", "tool_call", "tool_call", "snapshot"} {
				if _, err := tx.AppendSessionEvent(ctx, SessionEventInput{SessionID: ses.id, Kind: kind}, a); err != nil {
					return err
				}
			}
		}
		return nil
	})
	sessions := func(evs []SessionEvent) (ids []string) {
		for i, ev := range evs {
			if i > 0 && ev.Seq >= evs[i-1].Seq {
				t.Fatalf("rows not newest first: %+v", evs)
			}
			ids = append(ids, ev.SessionID+"/"+ev.Kind)
		}
		return ids
	}
	for _, c := range []struct {
		name string
		f    RecentEventFilter
		want []string
	}{
		{"one session, one kind", RecentEventFilter{WorkspaceID: "ws1", SessionID: "c:s1", Kinds: []string{"tool_call"}, Limit: 2},
			[]string{"c:s1/tool_call", "c:s1/tool_call"}},
		{"recent sessions but one", RecentEventFilter{WorkspaceID: "ws1", ExcludeSession: "c:s3", Kinds: []string{"snapshot"}, Limit: 10},
			[]string{"c:s2/snapshot", "c:s1/snapshot"}},
		{"the most recent session only", RecentEventFilter{WorkspaceID: "ws1", Sessions: 1, Kinds: []string{"snapshot"}},
			[]string{"c:s3/snapshot"}},
		{"another workspace's session", RecentEventFilter{WorkspaceID: "ws1", SessionID: "c:x"}, nil},
	} {
		evs, err := s.RecentSessionEvents(ctx, c.f)
		if got := sessions(evs); err != nil || !slices.Equal(got, c.want) {
			t.Errorf("%s: %v %v, want %v", c.name, got, err, c.want)
		}
	}
	var trimmed int64
	mustUpdate(t, s, func(tx Tx) error {
		var err error
		trimmed, err = tx.TrimSessionEvents(ctx, "c:s1", []string{"tool_call"}, 1)
		return err
	})
	left, _ := s.ListSessionEvents(ctx, SessionEventFilter{SessionID: "c:s1"})
	if trimmed != 3 || len(left) != 2 || left[0].Kind != "tool_call" || left[0].Seq != 4 || left[1].Kind != "snapshot" {
		t.Fatalf("trimmed %d, left %+v", trimmed, left)
	}
	err := s.Update(ctx, func(tx Tx) error {
		_, err := tx.EndSession(ctx, "c:s1", SessionEnded, "", Actor{Principal: "mallory"})
		return err
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("another principal ended the session: %v", err)
	}
	mustUpdate(t, s, func(tx Tx) error {
		_, err := tx.EndSession(ctx, "c:s1", SessionEnded, "done", a)
		return err
	})
}
