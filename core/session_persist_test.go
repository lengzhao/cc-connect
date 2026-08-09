package core

import (
	"testing"
)

func TestSessionManager_SetSaveHook(t *testing.T) {
	sm := NewSessionManager("")
	called := 0
	sm.SetSaveHook(func(*SessionManager, SessionSnapshot) { called++ })
	sm.Save()
	if called != 1 {
		t.Fatalf("saveHook calls = %d, want 1", called)
	}
}

func TestSessionManager_ExportImportSnapshot(t *testing.T) {
	sm := NewSessionManager("")
	s, err := sm.NewSessionWithID("chat-api:u1", "conv_exp012345678901234567", "demo")
	if err != nil {
		t.Fatal(err)
	}
	s.AddHistory("user", "hello")

	other := NewSessionManager("")
	other.ImportSnapshot(sm.ExportSnapshot())
	got := other.FindByID("conv_exp012345678901234567")
	if got == nil || len(got.GetHistory(0)) != 1 {
		t.Fatalf("imported snapshot = %+v", got)
	}
}
