package websocket

import (
	"fmt"
	"sync"
	"time"

	"trpc.group/trpc-go/trpc-go/log"
)

type taskOwnership struct {
	mu         sync.Mutex
	connection *AgentConnection
}

func (tm *TaskManager) ownerFor(sessionID string) *taskOwnership {
	owner, _ := tm.taskOwners.LoadOrStore(sessionID, &taskOwnership{})
	return owner.(*taskOwnership)
}

// assignTask 在单连接生命周期锁内保存归属并下发；失联清理不能漏掉并发分配。
func (ac *AgentConnection) assignTask(tm *TaskManager, sessionID string, message WSMessage) (assignmentErr error) {
	ac.lifecycleMu.Lock()
	defer func() {
		ac.lifecycleMu.Unlock()
		if assignmentErr != nil {
			ac.stop("Agent task assignment failed; scan incomplete, persisted events retained")
			tm.failDisconnectedTask(sessionID, ac)
		}
	}()
	ac.stateMu.RLock()
	active, agentID := ac.isActive, ac.agentID
	ac.stateMu.RUnlock()
	if !active || agentID == "" {
		return fmt.Errorf("Agent connection unavailable")
	}
	ownership := tm.ownerFor(sessionID)
	ownership.mu.Lock()
	defer ownership.mu.Unlock()
	if err := tm.taskStore.UpdateSessionAssignedAgent(sessionID, agentID); err != nil {
		return err
	}
	ownership.connection = ac
	ac.ownedTasks[sessionID] = struct{}{}
	if err := ac.writeJSON(message); err != nil {
		return fmt.Errorf("下发任务失败: %w", err)
	}
	return nil
}

func (tm *TaskManager) failDisconnectedTask(sessionID string, ac *AgentConnection) {
	ownership := tm.ownerFor(sessionID)
	ownership.mu.Lock()
	defer ownership.mu.Unlock()
	if ownership.connection != ac {
		return
	}
	agentID, reason := ac.id(), ac.failureReason
	id := generateEventID()
	event := map[string]interface{}{"id": id, "type": "error", "timestamp": time.Now().Unix(), "message": reason, "error": reason, "brief": "Agent disconnected; scan incomplete", "description": reason}
	changed, err := tm.taskStore.FailOwnedSession(sessionID, agentID, id, event)
	if err != nil {
		log.Errorf("保存Agent失联任务失败: sessionId=%s, error=%v", sessionID, err)
		return
	}
	if !changed {
		return
	}
	ownership.connection = nil
	if err := tm.sseManager.SendEvent(id, sessionID, "error", event); err != nil {
		log.Debugf("失联事件已保存，SSE不可用: sessionId=%s", sessionID)
	}
	tm.mu.Lock()
	delete(tm.tasks, sessionID)
	tm.mu.Unlock()
}
