package agent

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/gorilla/websocket"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFullSendQueueUnblocksWhenAgentCancelled(t *testing.T) {
	a := NewAgent(AgentConfig{})
	for i := 0; i < cap(a.sendChan); i++ {
		a.sendChan <- i
	}
	done := make(chan error, 1)
	go func() { done <- a.SendsToolUsedLog("task", "action", "tool", "step", "log") }()
	a.Stop()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled agent left task blocked on full send queue")
	}
}

type cancellationProbeTask struct{ started, stopped chan struct{} }

func (p *cancellationProbeTask) GetName() string { return "probe" }
func (p *cancellationProbeTask) Execute(ctx context.Context, _ TaskRequest, _ TaskCallbacks) error {
	close(p.started)
	<-ctx.Done()
	close(p.stopped)
	return ctx.Err()
}

func TestReceiveDisconnectCancelsAndJoinsActiveTask(t *testing.T) {
	disconnect := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		<-disconnect
	}))
	defer server.Close()
	a := NewAgent(AgentConfig{ServerURL: "ws" + strings.TrimPrefix(server.URL, "http")})
	probe := &cancellationProbeTask{started: make(chan struct{}), stopped: make(chan struct{})}
	a.RegisterTaskFunc(probe)
	done := make(chan error, 1)
	go func() { done <- a.Start() }()
	payload, _ := json.Marshal(RequestData{Type: ServerMsgTypeTaskAssign, Content: TaskRequest{SessionId: "task", TaskType: "probe"}})
	if err := a.processMessage(payload); err != nil {
		t.Fatal(err)
	}
	<-probe.started
	close(disconnect)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("agent receiver did not stop")
	}
	select {
	case <-probe.stopped:
	default:
		t.Fatal("agent returned before active task stopped")
	}
	if a.GetTaskBySessionId("task") != nil {
		t.Fatal("completed cancellation task retained")
	}
}

func TestTaskCancellationUnblocksCallbackWithoutStoppingAgent(t *testing.T) {
	a := NewAgent(AgentConfig{})
	defer a.Stop()
	taskCtx, cancel := context.WithCancel(a.ctx)
	a.Tasks = append(a.Tasks, &TaskContext{Request: TaskRequest{SessionId: "task"}, Context: taskCtx, Cancel: cancel})
	for i := 0; i < cap(a.sendChan); i++ {
		a.sendChan <- i
	}
	done := make(chan error, 1)
	go func() { done <- a.SendsToolUsedLog("task", "action", "tool", "step", "log") }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled task left callback blocked")
	}
	if a.ctx.Err() != nil {
		t.Fatal("task cancellation stopped agent")
	}
}

func TestQueueCongestionStopsAgentAndCancelsTasks(t *testing.T) {
	a := NewAgent(AgentConfig{})
	a.enqueueTimeout = 20 * time.Millisecond
	taskCtx, cancel := context.WithCancel(a.ctx)
	defer cancel()
	a.Tasks = append(a.Tasks, &TaskContext{Request: TaskRequest{SessionId: "task"}, Context: taskCtx, Cancel: cancel})
	for i := 0; i < cap(a.sendChan); i++ {
		a.sendChan <- i
	}
	err := a.SendsToolUsedLog("task", "action", "tool", "step", "log")
	if err == nil || a.ctx.Err() == nil || taskCtx.Err() == nil {
		t.Fatalf("congestion silently dropped event or left task running: %v", err)
	}
}

func TestCancelledTaskCanStillQueueItsCoverageReport(t *testing.T) {
	a := NewAgent(AgentConfig{})
	defer a.Stop()
	ctx, cancel := context.WithCancel(a.ctx)
	cancel()
	a.Tasks = append(a.Tasks, &TaskContext{Request: TaskRequest{SessionId: "task"}, Context: ctx})
	if err := a.SendTaskResult("task", map[string]interface{}{"completion_status": "cancelled"}); err != nil {
		t.Fatalf("lost partial coverage report: %v", err)
	}
	if len(a.sendChan) != 1 {
		t.Fatal("coverage report missing")
	}
}
