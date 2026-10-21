package websocket

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tencent/AI-Infra-Guard/pkg/database"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"gorm.io/gorm"
)

// newLifecyclePeer 使用真实回环 WebSocket，可等待消费者与清理全部结束。
func newLifecyclePeer(t *testing.T, am *AgentManager) (*websocket.Conn, *AgentConnection, <-chan struct{}) {
	t.Helper()
	accepted := make(chan *AgentConnection, 1)
	done := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		ac := NewAgentConnection(conn)
		accepted <- ac
		ac.handleConnection(am)
		close(done)
	}))
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	ac := <-accepted
	t.Cleanup(func() {
		client.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("connection cleanup blocked")
		}
		server.Close()
	})
	if err := client.WriteJSON(WSMessage{Type: "register", Content: AgentRegisterContent{AgentID: "test_id", Hostname: "loopback", IP: "127.0.0.1", Version: "test"}}); err != nil {
		t.Fatal(err)
	}
	var ack WSMessage
	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := client.ReadJSON(&ack); err != nil {
		t.Fatal(err)
	}
	client.SetReadDeadline(time.Time{})
	if ack.Type != "register_ack" {
		t.Fatalf("unexpected register response %s", ack.Type)
	}
	return client, ac, done
}

func dispatchLifecycleTask(t *testing.T, tm *TaskManager, client *websocket.Conn, id string) {
	t.Helper()
	if err := tm.AddTaskApi(&TaskCreateRequest{SessionID: id, Username: "u", Task: "scan"}); err != nil {
		t.Fatal(err)
	}
	var assigned WSMessage
	if err := client.ReadJSON(&assigned); err != nil {
		t.Fatal(err)
	}
	if assigned.Type != "task_assign" {
		t.Fatalf("unexpected task response %s", assigned.Type)
	}
}

func TestDisconnectPreservesHistoryAndFencesReplacement(t *testing.T) {
	store, _ := lifecycleStore(t)
	am := NewAgentManager()
	tm := NewTaskManager(am, store, nil, nil, nil)
	am.SetTaskManager(tm)
	oldClient, old, oldDone := newLifecyclePeer(t, am)
	dispatchLifecycleTask(t, tm, oldClient, "old-task")
	if err := store.StoreEvent("prior", "old-task", "liveStatus", map[string]string{"text": "already scanned"}, 1); err != nil {
		t.Fatal(err)
	}
	stream := newBoundedSSEWriter()
	tm.sseManager.connections["old-task"] = &SSEConnection{SessionID: "old-task", Writer: stream, Flusher: stream, CloseChan: make(chan bool)}
	newClient, newConn, _ := newLifecyclePeer(t, am)
	dispatchLifecycleTask(t, tm, newClient, "new-task")
	select {
	case <-oldDone:
	case <-time.After(2 * time.Second):
		t.Fatal("replaced connection cleanup did not finish")
	}
	old.cleanup(am) // 重复旧清理也不能移除同 ID 的新连接。
	if available := am.GetAvailableAgents(); len(available) != 1 || available[0] != newConn {
		t.Fatalf("old cleanup removed replacement: %v", available)
	}
	oldSession, err := store.GetSession("old-task")
	if err != nil {
		t.Fatal(err)
	}
	if oldSession.Status != "error" || oldSession.CompletedAt == nil {
		t.Fatalf("lost task still running: %+v", oldSession)
	}
	if len(oldSession.Messages) != 2 {
		t.Fatalf("history not retained: %+v", oldSession.Messages)
	}
	if !strings.Contains(stream.Body.String(), "event: error") || !strings.Contains(stream.Body.String(), "previous scan interrupted") {
		t.Fatalf("disconnect error not sent to SSE: %s", stream.Body.String())
	}
	newStatus, err := store.GetSessionStatus("new-task")
	if err != nil || newStatus != "doing" {
		t.Fatalf("replacement task modified: %s %v", newStatus, err)
	}
}

func TestAgentStatusReportsOnlyRegisteredConnections(t *testing.T) {
	am := NewAgentManager()
	route := gin.New()
	route.GET("/agents/status", am.StatusHandler())
	check := func(want int) {
		t.Helper()
		response := httptest.NewRecorder()
		route.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/agents/status", nil))
		var status struct {
			ConnectedCount int `json:"connected_count"`
			Agents         []struct {
				ID        string `json:"agent_id"`
				Connected bool   `json:"connected"`
			} `json:"agents"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
			t.Fatal(err)
		}
		if response.Code != 200 || status.ConnectedCount != want || len(status.Agents) != want {
			t.Fatalf("unexpected status: %s", response.Body.String())
		}
		if want > 0 && (status.Agents[0].ID != "test_id" || !status.Agents[0].Connected) {
			t.Fatalf("wrong registered agent: %s", response.Body.String())
		}
	}
	check(0)
	client, _, done := newLifecyclePeer(t, am)
	check(1)
	client.Close()
	<-done
	check(0)
}

func TestReassignedTaskWithSameAgentIDSurvivesOldCleanup(t *testing.T) {
	store, _ := lifecycleStore(t)
	am := NewAgentManager()
	tm := NewTaskManager(am, store, nil, nil, nil)
	am.SetTaskManager(tm)
	old := NewAgentConnection(nil)
	old.agentID = "test_id"
	old.ownedTasks["shared-task"] = struct{}{}
	am.connections["test_id"] = old
	if err := store.CreateSession(&database.Session{ID: "shared-task", Username: "u", TaskType: "scan", Status: "doing", AssignedAgent: "test_id"}); err != nil {
		t.Fatal(err)
	}
	client, replacement, _ := newLifecyclePeer(t, am)
	if err := replacement.assignTask(tm, "shared-task", WSMessage{Type: "task_assign"}); err != nil {
		t.Fatal(err)
	}
	var msg WSMessage
	if err := client.ReadJSON(&msg); err != nil {
		t.Fatal(err)
	}
	old.cleanup(am)
	status, err := store.GetSessionStatus("shared-task")
	if err != nil || status != "doing" {
		t.Fatalf("old connection cleanup failed newly owned task with same AgentID: status=%s error=%v", status, err)
	}
}

func TestFailedAssignmentPersistsFailureWithoutDeletingHistory(t *testing.T) {
	store, _ := lifecycleStore(t)
	am := NewAgentManager()
	tm := NewTaskManager(am, store, nil, nil, nil)
	am.SetTaskManager(tm)
	accepted := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err == nil {
			accepted <- conn
		}
	}))
	defer server.Close()
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	serverConn := <-accepted
	serverConn.Close()
	ac := NewAgentConnection(serverConn)
	ac.agentID = "test_id"
	if err := store.CreateSession(&database.Session{ID: "failed-assignment", Username: "u", TaskType: "scan", Status: "todo"}); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreEvent("prior", "failed-assignment", "liveStatus", map[string]string{"text": "retained"}, 1); err != nil {
		t.Fatal(err)
	}
	if err := ac.assignTask(tm, "failed-assignment", WSMessage{Type: "task_assign"}); err == nil {
		t.Fatal("closed connection accepted assignment")
	}
	tm.cleanupFailedTask("failed-assignment", "test")
	session, err := store.GetSession("failed-assignment")
	if err != nil || session.Status != "error" || len(session.Messages) != 2 {
		t.Fatalf("assignment failure lost task/history: %+v %v", session, err)
	}
}

func TestFailedResultPersistenceCannotReportDone(t *testing.T) {
	store, db := lifecycleStore(t)
	if err := store.CreateSession(&database.Session{ID: "result", Username: "u", TaskType: "scan", Status: "doing"}); err != nil {
		t.Fatal(err)
	}
	db.Callback().Create().Before("gorm:create").Register("reject-result", func(tx *gorm.DB) {
		if message, ok := tx.Statement.Dest.(*database.TaskMessage); ok && message.Type == "resultUpdate" {
			tx.AddError(fmt.Errorf("controlled event storage failure"))
		}
	})
	tm := NewTaskManager(NewAgentManager(), store, nil, nil, nil)
	tm.HandleAgentEvent("result", "resultUpdate", map[string]interface{}{"results": []interface{}{}})
	status, err := store.GetSessionStatus("result")
	if err != nil {
		t.Fatal(err)
	}
	if status == "done" {
		t.Fatal("failed result storage reported task success")
	}
}

func TestTerminatedTaskRetainsOwnedCancellationReport(t *testing.T) {
	store, _ := lifecycleStore(t)
	am := NewAgentManager()
	tm := NewTaskManager(am, store, nil, nil, nil)
	am.SetTaskManager(tm)
	client, _, done := newLifecyclePeer(t, am)
	dispatchLifecycleTask(t, tm, client, "cancelled")
	if err := store.UpdateSessionStatus("cancelled", TaskStatusTerminated); err != nil {
		t.Fatal(err)
	}
	report := map[string]interface{}{"cancelled": true, "results": []interface{}{map[string]string{"target": "retained-detection"}}}
	if err := client.WriteJSON(WSMessage{Type: "resultUpdate", Content: TaskEventMessage{Type: "resultUpdate", SessionID: "cancelled", Event: map[string]interface{}{"result": report}}}); err != nil {
		t.Fatal(err)
	}
	// 正常关闭后，服务端必须先消费已接受的报告再完成清理。
	client.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled result did not drain")
	}
	session, err := store.GetSession("cancelled")
	if err != nil {
		t.Fatal(err)
	}
	if session.Status != TaskStatusTerminated {
		t.Fatalf("cancelled task status changed to %s", session.Status)
	}
	if len(session.Messages) != 1 || session.Messages[0].Type != "resultUpdate" || !strings.Contains(string(session.Messages[0].EventData), "retained-detection") {
		t.Fatalf("cancelled coverage report discarded: %+v", session.Messages)
	}
}

func TestAcceptedEventsAreOrderedAndOverflowFailsExplicitly(t *testing.T) {
	store, db := lifecycleStore(t)
	am := NewAgentManager()
	tm := NewTaskManager(am, store, nil, nil, nil)
	am.SetTaskManager(tm)
	client, ac, done := newLifecyclePeer(t, am)
	dispatchLifecycleTask(t, tm, client, "bulk-task")
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	db.Callback().Query().Before("gorm:query").Register("block-consumer", func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "Session" {
			once.Do(func() { close(entered); <-release })
		}
	})
	writeEvent := func(i int) error {
		return client.WriteJSON(WSMessage{Type: "liveStatus", Content: TaskEventMessage{Type: "liveStatus", SessionID: "bulk-task", Event: map[string]interface{}{"sequence": i}}})
	}
	if err := writeEvent(0); err != nil {
		t.Fatal(err)
	}
	<-entered
	for i := 1; i < 270; i++ {
		var err error
		if i == 1 {
			err = client.WriteJSON(WSMessage{Type: "resultUpdate", Content: TaskEventMessage{Type: "resultUpdate", SessionID: "bulk-task", Event: map[string]interface{}{"sequence": i, "result": map[string]string{"text": "queued partial report"}}}})
		} else {
			err = writeEvent(i)
		}
		if err != nil {
			break
		}
	}
	select {
	case <-ac.done:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("overflow did not close socket explicitly")
	}
	close(release)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("overflow did not drain accepted events")
	}
	session, err := store.GetSession("bulk-task")
	if err != nil {
		t.Fatal(err)
	}
	if session.Status != "error" {
		t.Fatalf("overflow left status %s", session.Status)
	}
	if len(session.Messages) < 2 {
		t.Fatal("accepted events were discarded")
	}
	// 主键生成顺序不等于 SQLite 默认查询顺序，按事件时间再 ID 排序读取。
	messages, err := store.GetSessionMessages("bulk-task")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	overflow := false
	for _, m := range messages {
		if m.Type == "error" {
			overflow = strings.Contains(string(m.EventData), "overflow")
			continue
		}
		var event struct {
			Sequence int `json:"sequence"`
		}
		if err := json.Unmarshal(m.EventData, &event); err != nil {
			t.Fatal(err)
		}
		if event.Sequence != count {
			t.Fatalf("event order broken: want %d got %d", count, event.Sequence)
		}
		count++
	}
	if count != 257 {
		t.Fatalf("accepted %d events, want in-flight + 256 queued", count)
	}
	if !overflow {
		t.Fatal("overflow error not persisted")
	}
}

func lifecycleStore(t *testing.T) (*database.TaskStore, *gorm.DB) {
	t.Helper()
	db, err := database.InitDB(database.NewConfig(filepath.Join(t.TempDir(), "tasks.db")))
	if err != nil {
		t.Fatal(err)
	}
	store := database.NewTaskStore(db)
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB, _ := db.DB(); sqlDB.Close() })
	return store, db
}

func TestEventStatusDoesNotLoadHistory(t *testing.T) {
	store, db := lifecycleStore(t)
	if err := store.CreateSession(&database.Session{ID: "task", Username: "u", TaskType: "scan", Status: "doing"}); err != nil {
		t.Fatal(err)
	}
	var historyQueries int
	db.Callback().Query().Before("gorm:query").Register("reject-history", func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "Session" && (len(tx.Statement.Selects) != 1 || tx.Statement.Selects[0] != "status") {
			t.Errorf("status query selected extra columns: %v", tx.Statement.Selects)
		}
		if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "TaskMessage" {
			historyQueries++
			tx.AddError(fmt.Errorf("history lookup forbidden on event status path"))
		}
	})
	tm := NewTaskManager(NewAgentManager(), store, nil, nil, nil)
	if tm.shouldIgnoreAgentEvent("task", "liveStatus") {
		t.Errorf("running task ignored because status check queried history")
	}
	if historyQueries != 0 {
		t.Errorf("status check loaded history %d times", historyQueries)
	}
}

func TestSlowEventDoesNotBlockWebsocketControlFrames(t *testing.T) {
	store, db := lifecycleStore(t)
	store.CreateSession(&database.Session{ID: "task", Username: "u", TaskType: "scan", Status: "doing"})
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	db.Callback().Query().Before("gorm:query").Register("slow-status", func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "Session" {
			once.Do(func() { close(entered); <-release })
		}
	})
	am := NewAgentManager()
	am.SetTaskManager(NewTaskManager(am, store, nil, nil, nil))
	done := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer close(done)
		ac := NewAgentConnection(conn)
		ac.agentID = "test_id"
		ac.ownedTasks["task"] = struct{}{}
		am.taskManager.ownerFor("task").connection = ac
		am.connections["test_id"] = ac
		ac.handleConnection(am)
	}))
	defer server.Close()
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		close(release)
		client.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("connection did not stop")
		}
	}()
	if err := client.WriteJSON(WSMessage{Type: "liveStatus", Content: map[string]interface{}{"sessionId": "task", "type": "liveStatus", "event": map[string]interface{}{"text": "slow"}}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("handler never entered")
	}
	pong := make(chan struct{}, 1)
	client.SetPongHandler(func(string) error { pong <- struct{}{}; return nil })
	go func() {
		for {
			if _, _, err := client.ReadMessage(); err != nil {
				return
			}
		}
	}()
	if err := client.WriteControl(websocket.PingMessage, []byte("probe"), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-pong:
	case <-time.After(500 * time.Millisecond):
		t.Error("slow business event blocked websocket control frame processing")
	}
}
