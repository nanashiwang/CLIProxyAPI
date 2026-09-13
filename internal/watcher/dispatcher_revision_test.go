package watcher

import (
	"path/filepath"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestPersistedAuthRevisionWithoutQueueRejectsEarlierObservation(t *testing.T) {
	w := &Watcher{}
	path := filepath.Join(t.TempDir(), "auth.json")
	a := &coreauth.Auth{ID: "revision-auth", Provider: "codex", Attributes: map[string]string{"path": path}}
	first := AuthUpdate{Action: AuthUpdateActionModify, Auth: a}
	ok, rev1 := w.DispatchPersistedAuthUpdateWithRevision(&first)
	if ok || rev1 == 0 || first.Revision() != rev1 {
		t.Fatalf("missing revision without queue: %v %d %+v", ok, rev1, first)
	}
	a.Disabled = true
	second := AuthUpdate{Action: AuthUpdateActionModify, Auth: a}
	ok, rev2 := w.DispatchPersistedAuthUpdateWithRevision(&second)
	if ok || rev2 <= rev1 || second.Revision() != rev2 {
		t.Fatalf("revision did not advance: %d -> %d", rev1, rev2)
	}
	if w.currentAuths[a.ID] == a || !w.currentAuths[a.ID].Disabled {
		t.Fatal("persisted snapshot was not cloned")
	}
	queue := make(chan AuthUpdate, 2)
	w.authQueue = queue
	w.dispatchAuthUpdates([]AuthUpdate{first})
	if len(w.pendingUpdates) != 0 {
		t.Fatal("older persisted observation was re-enqueued")
	}
	if ok, rev := w.DispatchPersistedAuthUpdateWithRevision(nil); ok || rev != 0 {
		t.Fatal("nil update accepted")
	}
}
