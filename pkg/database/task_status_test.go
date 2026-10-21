package database

import (
	"testing"
)

func TestSessionStatusPreservesDetailContract(t *testing.T) {
	store, _, cleanup := newTestDB(t)
	defer cleanup()
	if err := store.CreateSession(&Session{ID: "status-only", Username: "u", TaskType: "scan", Status: "doing"}); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreEvent("history", "status-only", "liveStatus", map[string]string{"text": "retained"}, 1); err != nil {
		t.Fatal(err)
	}
	status, err := store.GetSessionStatus("status-only")
	if err != nil || status != "doing" {
		t.Fatalf("status=%s err=%v", status, err)
	}
	session, err := store.GetSession("status-only")
	if err != nil || len(session.Messages) != 1 {
		t.Fatalf("detail history contract changed: %+v %v", session, err)
	}
	if _, err := store.GetSessionStatus("missing"); err == nil {
		t.Fatal("missing session must return error")
	}
}

func TestFailOwnedSessionIsConditionalAndAtomic(t *testing.T) {
	for _, test := range []struct {
		name, status, owner string
		change              bool
	}{{"doing", "doing", "agent", true}, {"done", "done", "agent", false}, {"terminated", "terminated", "agent", false}, {"other-owner", "doing", "other", false}} {
		t.Run(test.name, func(t *testing.T) {
			store, _, cleanup := newTestDB(t)
			defer cleanup()
			if err := store.CreateSession(&Session{ID: "task", Username: "u", TaskType: "scan", Status: test.status, AssignedAgent: test.owner}); err != nil {
				t.Fatal(err)
			}
			changed, err := store.FailOwnedSession("task", "agent", "disconnected", map[string]string{"message": "disconnected"})
			if err != nil || changed != test.change {
				t.Fatalf("changed=%v error=%v", changed, err)
			}
			session, err := store.GetSession("task")
			if err != nil {
				t.Fatal(err)
			}
			if test.change && (session.Status != "error" || session.CompletedAt == nil || len(session.Messages) != 1) {
				t.Fatalf("inconsistent failure %+v", session)
			}
			if !test.change && (session.Status != test.status || len(session.Messages) != 0) {
				t.Fatalf("unowned or terminal task modified %+v", session)
			}
			changed, err = store.FailOwnedSession("task", "agent", "again", map[string]string{"message": "disconnected"})
			if changed || err != nil {
				t.Fatalf("repeat failure is not idempotent: %v %v", changed, err)
			}
		})
	}
	t.Run("event-insert-failure-rolls-back-status", func(t *testing.T) {
		store, _, cleanup := newTestDB(t)
		defer cleanup()
		store.CreateSession(&Session{ID: "task", Username: "u", TaskType: "scan", Status: "doing", AssignedAgent: "agent"})
		store.StoreEvent("duplicate", "task", "liveStatus", map[string]string{"text": "retained"}, 1)
		changed, err := store.FailOwnedSession("task", "agent", "duplicate", map[string]string{"message": "disconnected"})
		if err == nil || changed {
			t.Fatalf("duplicate event should fail transaction: %v %v", changed, err)
		}
		status, _ := store.GetSessionStatus("task")
		if status != "doing" {
			t.Fatalf("failed persistence left status=%s", status)
		}
	})
}
