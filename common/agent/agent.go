// Copyright (c) 2024-2026 Tencent Zhuque Lab. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// Requirement: Any integration or derivative work must explicitly attribute
// Tencent Zhuque Lab (https://github.com/Tencent/AI-Infra-Guard) in its
// documentation or user interface, as detailed in the NOTICE file.

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/Tencent/AI-Infra-Guard/internal/gologger"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// Agent 客户端结构
type Agent struct {
	// 基本信息
	info      AgentInfo
	serverURL string
	conn      *websocket.Conn

	// 任务管理
	Tasks    []*TaskContext
	taskFunc []TaskInterface

	// 通信管理
	sendChan chan interface{}
	ctx      context.Context
	cancel   context.CancelFunc

	// 配置
	mutex          sync.RWMutex
	writeMu        sync.Mutex
	enqueueTimeout time.Duration
	writeTimeout   time.Duration
	taskWG         sync.WaitGroup
}

// TaskContext 任务上下文
type TaskContext struct {
	Context   context.Context
	Request   TaskRequest
	Status    string
	Progress  int
	StartTime time.Time
	Cancel    context.CancelFunc
	Result    interface{}
	Error     error
}

// AgentConfig Agent配置
type AgentConfig struct {
	ServerURL string
	Info      AgentInfo
}

// NewAgent 创建新的Agent实例
func NewAgent(config AgentConfig) *Agent {
	ctx, cancel := context.WithCancel(context.Background())
	agent := &Agent{
		info:           config.Info,
		serverURL:      config.ServerURL,
		conn:           nil,
		Tasks:          make([]*TaskContext, 0),
		sendChan:       make(chan interface{}, 100),
		ctx:            ctx,
		cancel:         cancel,
		mutex:          sync.RWMutex{},
		taskFunc:       make([]TaskInterface, 0),
		enqueueTimeout: 30 * time.Second, writeTimeout: 30 * time.Second,
	}
	return agent
}

func (a *Agent) RegisterTaskFunc(taskFunc TaskInterface) {
	a.mutex.Lock()
	defer a.mutex.Unlock()
	a.taskFunc = append(a.taskFunc, taskFunc)
	a.info.Capabilities = append(a.info.Capabilities, taskFunc.GetName())
}

// Start 启动Agent
func (a *Agent) Start() error {
	// 尝试连接到服务器
	if err := a.connect(); err != nil {
		return fmt.Errorf("failed to connect to server: %v", err)
	}
	// 启动各种协程
	sendDone := make(chan struct{})
	go func() { defer close(sendDone); a.handleSend() }()
	defer func() { a.Stop(); a.taskWG.Wait(); <-sendDone }()
	a.handleReceive()
	return nil
}

// Stop 停止Agent
func (a *Agent) Stop() {
	a.mutex.Lock()
	for _, task := range a.Tasks {
		if task.Cancel != nil {
			task.Cancel()
		}
	}
	a.cancel()
	conn := a.conn
	a.mutex.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

// enqueue 取消可打断有界队列等待；拥塞超时关闭连接，明确标记报告未完整回传。
func (a *Agent) enqueue(ctx context.Context, msg interface{}) error {
	if err := a.ctx.Err(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	timeout := a.enqueueTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-a.ctx.Done():
		return a.ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	case a.sendChan <- msg:
		return nil
	case <-timer.C:
		err := fmt.Errorf("agent send queue congested for %s; connection stopped to preserve incomplete coverage", timeout)
		gologger.WithError(err).Errorln("Agent event delivery failed")
		a.Stop()
		return err
	}
}

func (a *Agent) enqueueForTask(sessionId string, msg interface{}) error {
	ctx := a.ctx
	a.mutex.RLock()
	for _, task := range a.Tasks {
		if task.Request.SessionId == sessionId && task.Context != nil {
			ctx = task.Context
			break
		}
	}
	a.mutex.RUnlock()
	return a.enqueue(ctx, msg)
}

// connect 连接到服务器
func (a *Agent) connect() error {
	u, err := url.Parse(a.serverURL)
	if err != nil {
		return err
	}
	dialer := websocket.DefaultDialer
	conn, _, err := dialer.DialContext(a.ctx, u.String(), nil)
	if err != nil {
		return err
	}
	conn.SetReadLimit(1024 * 1024 * 5)
	a.mutex.Lock()
	a.conn = conn
	a.mutex.Unlock()

	// 设置ping处理器：收到ping消息后自动回复pong
	conn.SetPingHandler(func(appData string) error {
		gologger.Debugln("Received ping message, sending pong response", appData)
		a.writeMu.Lock()
		defer a.writeMu.Unlock()
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(5*time.Second))
	})

	// 设置pong处理器：收到pong消息时的处理逻辑
	conn.SetPongHandler(func(appData string) error {
		gologger.Debugln("Received pong message")
		// 更新读取超时时间，保持连接活跃
		return conn.SetReadDeadline(time.Now().Add(120 * time.Second))
	})

	// 发送注册消息
	return a.register()
}

// register 向服务器注册
func (a *Agent) register() error {
	registerMsg := RequestData{
		Type:    AgentMsgTypeRegister,
		Content: a.info,
	}
	return a.enqueue(a.ctx, registerMsg)
}

// Disconnect 断开连接
func (a *Agent) Disconnect(reason string) error {
	msg := Disconnect{
		AgentID: a.info.ID,
		Reason:  reason,
	}
	return a.sendMessage(msg)
}

// sendMessage 发送消息
func (a *Agent) sendMessage(msg interface{}) error {
	if err := a.ctx.Err(); err != nil {
		return err
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	a.mutex.RLock()
	conn := a.conn
	a.mutex.RUnlock()
	if conn == nil {
		return fmt.Errorf("connection is nil")
	}
	timeout := a.writeTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if err = conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, data)
}

// handleSend 处理发送队列
func (a *Agent) handleSend() {
	for {
		select {
		case <-a.ctx.Done():
			return
		case msg := <-a.sendChan:
			if err := a.sendMessage(msg); err != nil {
				gologger.WithError(err).Errorln("Failed to send message")
				a.Stop()
				return
			}
		}
	}
}

// handleReceive 处理接收消息
func (a *Agent) handleReceive() {
	defer a.Stop()
	a.mutex.RLock()
	conn := a.conn
	a.mutex.RUnlock()
	if conn == nil {
		return
	}
	for {
		if a.ctx.Err() != nil {
			return
		}
		_, message, err := conn.ReadMessage()
		if err != nil {
			gologger.WithError(err).Errorln("Failed to read message")
			return
		}
		if err = a.processMessage(message); err != nil {
			gologger.WithError(err).Errorln("Failed to process message")
		}
	}
}

// processMessage 处理接收到的消息
func (a *Agent) processMessage(data []byte) error {
	var baseMsg ResponseData

	if err := json.Unmarshal(data, &baseMsg); err != nil {
		return err
	}

	switch baseMsg.Type {
	case ServerMsgTypeRegisterResp:
	case ServerMsgTypeTaskAssign:
		var task TaskRequest
		if err := json.Unmarshal(baseMsg.Content, &task); err != nil {
			return err
		}
		taskType := task.TaskType
		taskCtx, cancel := context.WithCancel(a.ctx)
		// 创建任务上下文
		if task.Timeout > 0 {
			//taskCtx, cancel = context.WithTimeout(taskCtx, time.Duration(task.Timeout)*time.Second)
		}
		// 加入task 上下文
		taskContext := &TaskContext{
			Context:   taskCtx,
			Request:   task,
			Status:    TaskStatusPending,
			Progress:  0,
			StartTime: time.Now(),
			Cancel:    cancel,
		}
		a.mutex.Lock()
		a.Tasks = append(a.Tasks, taskContext)
		functions := append([]TaskInterface(nil), a.taskFunc...)
		a.mutex.Unlock()
		matched := false
		for _, taskFunc := range functions {
			if taskType == taskFunc.GetName() {
				gologger.Debugln("执行任务", taskFunc.GetName())
				matched = true
				a.setTaskStatus(taskContext, TaskStatusRunning)
				// 创建回调函数集合
				callbacks := TaskCallbacks{
					ResultCallback: func(result map[string]interface{}) {
						gologger.Debugln("ResultCallback", result)
						a.SendTaskResult(task.SessionId, result)
						gologger.Debugln("ResultCallback end")
					},
					ToolUseLogCallback: func(actionId, tool, planStepId, actionLog string) {
						a.SendsToolUsedLog(task.SessionId, actionId, tool, planStepId, actionLog)
						gologger.Debugln("ToolUseLogCallback", actionId, tool, planStepId, actionLog)
					},
					ToolUsedCallback: func(planStepId, statusId, description string, tools []Tool) {
						a.SendToolUsed(task.SessionId, planStepId, statusId, description, tools)
						gologger.Debugln("ToolUsedCallback", planStepId, statusId, description, tools)
					},
					NewPlanStepCallback: func(stepId, title string) {
						a.SendNewPlanStep(task.SessionId, stepId, title)
						gologger.Debugln("NewPlanStepCallback", stepId, title)
					},
					StepStatusUpdateCallback: func(planStepId, statusId, agentStatus, brief, description string) {
						a.SendStepStatusUpdate(task.SessionId, planStepId, statusId, agentStatus, brief, description)
						gologger.Debugln("StepStatusUpdateCallback", planStepId, statusId, agentStatus, brief, description)
					},
					PlanUpdateCallback: func(tasks []SubTask) {
						a.SendPlanUpdate(task.SessionId, tasks)
						gologger.Debugln("PlanUpdateCallback", tasks)
					},
					ErrorCallback: func(error string) {
						a.SendError(task.SessionId, error)
						gologger.Debugln("ErrorCallback", error)
					},
				}
				a.taskWG.Add(1)
				go func(taskFunc TaskInterface) {
					defer a.taskWG.Done()
					defer cancel()
					err := taskCtx.Err()
					if err == nil {
						err = taskFunc.Execute(taskCtx, task, callbacks)
					}
					if err != nil {
						if errors.Is(err, context.Canceled) {
							gologger.Infof("任务已取消: sessionId=%s", task.SessionId)
							a.setTaskStatus(taskContext, TaskStatusFailed)
							a.removeTask(task.SessionId)
							return
						}
						a.setTaskStatus(taskContext, TaskStatusFailed)
						a.SendTaskError(task.SessionId, err)
						a.removeTask(task.SessionId)
						return
					}
					a.setTaskStatus(taskContext, TaskStatusComplete)
					a.removeTask(task.SessionId)
				}(taskFunc)
				break
			}
		}
		if !matched {
			cancel()
			a.removeTask(task.SessionId)
			return a.SendError(task.SessionId, "unsupported task type")
		}
	case ServerMsgTypeTerminate:
		var terminateReq TerminateTaskRequest
		if err := json.Unmarshal(baseMsg.Content, &terminateReq); err != nil {
			return err
		}
		if terminateReq.SessionID == "" {
			return fmt.Errorf("terminate message missing session_id")
		}
		if !a.cancelTask(terminateReq.SessionID) {
			gologger.Warningf("未找到可终止的任务: sessionId=%s", terminateReq.SessionID)
		}
	default:
		return nil
	}
	return nil
}

// SendTaskResult 发送任务最终结果
func (a *Agent) SendTaskResult(sessionId string, result map[string]interface{}) error {
	timestamp := time.Now().Unix()
	msgId := uuid.New().String()

	// 构建事件数据
	event := Event{
		ID:        msgId,
		Type:      "resultUpdate",
		Timestamp: timestamp,
		Result:    result,
	}

	// 构建结果更新消息
	resultUpdate := ResultUpdate{
		ID:        msgId,
		Type:      "event",
		SessionId: sessionId,
		Timestamp: timestamp,
		Event:     event,
	}

	// 构建发送给服务器的消息
	resultUpdateContent := RequestData{
		Type:    AgentMsgTypeResultUpdate,
		Content: resultUpdate,
	}

	// 通过发送通道发送消息
	// 最终覆盖报告允许在单任务取消后回传；Agent 断连仍会中断。
	return a.enqueue(a.ctx, resultUpdateContent)
}

// GetTaskBySessionId 根据SessionId获取任务上下文
func (a *Agent) GetTaskBySessionId(sessionId string) *TaskContext {
	a.mutex.RLock()
	defer a.mutex.RUnlock()

	for _, task := range a.Tasks {
		if task.Request.SessionId == sessionId {
			snapshot := *task
			return &snapshot
		}
	}
	return nil
}

func (a *Agent) setTaskStatus(task *TaskContext, status string) {
	a.mutex.Lock()
	task.Status = status
	a.mutex.Unlock()
}

func (a *Agent) cancelTask(sessionId string) bool {
	a.mutex.RLock()
	defer a.mutex.RUnlock()

	for _, task := range a.Tasks {
		if task.Request.SessionId != sessionId {
			continue
		}
		if task.Cancel != nil {
			task.Cancel()
		}
		return true
	}
	return false
}

func (a *Agent) removeTask(sessionId string) {
	a.mutex.Lock()
	defer a.mutex.Unlock()

	for idx, task := range a.Tasks {
		if task.Request.SessionId != sessionId {
			continue
		}
		a.Tasks = append(a.Tasks[:idx], a.Tasks[idx+1:]...)
		return
	}
}

func (a *Agent) SendsToolUsedLog(sessionId, actionId, tool, planStepId, actionLog string) error {
	timestamp := time.Now().Unix()
	msgId := uuid.New().String()

	// 构建插件日志事件数据
	event := ActionLogEvent{
		ID:         msgId,
		Type:       "actionLog",
		Timestamp:  timestamp,
		ActionId:   actionId,
		Tool:       tool,
		PlanStepId: planStepId,
		ActionLog:  actionLog,
	}

	// 构建插件日志更新消息
	actionLogUpdate := ActionLogUpdate{
		ID:        msgId,
		Type:      "event",
		SessionId: sessionId,
		Timestamp: timestamp,
		Event:     event,
	}

	// 构建发送给服务器的消息
	actionLogContent := ActionLogContent{
		Type:    AgentMsgTypeActionLog,
		Content: actionLogUpdate,
	}

	// 通过发送通道发送消息
	return a.enqueueForTask(sessionId, actionLogContent)
}

func (a *Agent) SendToolUsed(sessionId, planStepId, statusId, description string, tools []Tool) error {
	timestamp := time.Now().Unix()
	msgId := uuid.New().String()

	// 构建插件工作状态事件数据
	event := ToolUsedEvent{
		ID:          msgId,
		Type:        "toolUsed",
		Timestamp:   timestamp,
		Description: description,
		PlanStepId:  planStepId,
		StatusId:    statusId,
		Tools:       tools,
	}

	// 构建插件工作状态更新消息
	toolUsedUpdate := ToolUsedUpdate{
		ID:        msgId,
		Type:      "event",
		SessionId: sessionId,
		Timestamp: timestamp,
		Event:     event,
	}

	// 构建发送给服务器的消息
	toolUsedContent := ToolUsedContent{
		Type:    AgentMsgTypeToolUsed,
		Content: toolUsedUpdate,
	}

	// 通过发送通道发送消息
	return a.enqueueForTask(sessionId, toolUsedContent)
}

// CreateTool 创建工具使用信息
func CreateTool(toolId, tool string, status statusString, brief, action, param, result string) Tool {
	return Tool{
		ToolId: toolId,
		Tool:   tool,
		Status: status,
		Brief:  brief,
		Message: ToolMessage{
			Action: action,
			Param:  param,
		},
		Result: result,
	}
}

// SendNewPlanStep 新建执行步骤
func (a *Agent) SendNewPlanStep(sessionId, stepId, title string) error {
	timestamp := time.Now().Unix()
	msgId := uuid.New().String()

	// 构建新建执行步骤事件数据
	event := NewPlanStepEvent{
		ID:        msgId,
		Type:      AgentMsgTypeNewPlanStep,
		Timestamp: timestamp,
		StepId:    stepId,
		Title:     title,
	}

	// 构建新建执行步骤更新消息
	newPlanStepUpdate := NewPlanStepUpdate{
		ID:        msgId,
		Type:      "event",
		SessionId: sessionId,
		Timestamp: timestamp,
		Event:     event,
	}

	// 构建发送给服务器的消息
	newPlanStepContent := NewPlanStepContent{
		Type:    AgentMsgTypeNewPlanStep,
		Content: newPlanStepUpdate,
	}

	// 通过发送通道发送消息
	return a.enqueueForTask(sessionId, newPlanStepContent)
}

// SendStepStatusUpdate 发送更新步骤状态
func (a *Agent) SendStepStatusUpdate(sessionId, planStepId, statusId, agentStatus, brief, description string) error {
	timestamp := time.Now().Unix()

	// 构建更新步骤状态事件数据
	event := StatusUpdateEvent{
		ID:          statusId,
		Type:        AgentMsgTypeStatusUpdate,
		Timestamp:   timestamp,
		AgentStatus: agentStatus,
		Brief:       brief,
		Description: description,
		NoRender:    false,
		PlanStepId:  planStepId,
	}

	// 构建更新步骤状态更新消息
	statusUpdateUpdate := StatusUpdateUpdate{
		ID:        statusId,
		Type:      "event",
		SessionId: sessionId,
		Timestamp: timestamp,
		Event:     event,
	}

	// 构建发送给服务器的消息
	statusUpdateContent := StatusUpdateContent{
		Type:    AgentMsgTypeStatusUpdate,
		Content: statusUpdateUpdate,
	}

	// 通过发送通道发送消息
	return a.enqueueForTask(sessionId, statusUpdateContent)
}

// SendPlanUpdate 整体计划更新
func (a *Agent) SendPlanUpdate(sessionId string, tasks []SubTask) error {
	timestamp := time.Now().Unix()
	msgId := uuid.New().String()

	// 构建更新任务计划事件数据
	event := PlanUpdateEvent{
		ID:        msgId,
		Type:      "planUpdate",
		Timestamp: timestamp,
		Tasks:     append([]SubTask(nil), tasks...),
	}

	// 构建更新任务计划更新消息
	planUpdateUpdate := PlanUpdateUpdate{
		ID:        msgId,
		Type:      "event",
		SessionId: sessionId,
		Timestamp: timestamp,
		Event:     event,
	}

	// 构建发送给服务器的消息
	planUpdateContent := PlanUpdateContent{
		Type:    AgentMsgTypePlanUpdate,
		Content: planUpdateUpdate,
	}

	// 通过发送通道发送消息
	return a.enqueueForTask(sessionId, planUpdateContent)
}

// CreateSubTask 创建子任务的便捷方法
func CreateSubTask(status statusString, title string, startedAt int64, stepId string) SubTask {
	return SubTask{
		Status:    status,
		Title:     title,
		StartedAt: startedAt,
		StepId:    stepId,
	}
}

// SendError 发送错误
func (a *Agent) SendError(sessionId, msg string) error {
	return a.sendErrorEvent(sessionId, msg, nil)
}

// SendTaskError 在目标解析失败时附带逐行错误，其余任务错误保持原事件格式。
func (a *Agent) SendTaskError(sessionId string, err error) error {
	var parseErr *targetParseError
	if errors.As(err, &parseErr) {
		return a.sendErrorEvent(sessionId, err.Error(), parseErr.targetIssues())
	}
	return a.SendError(sessionId, err.Error())
}

func (a *Agent) sendErrorEvent(sessionId, msg string, issues []TargetIssue) error {
	timestamp := time.Now().Unix()
	msgId := uuid.New().String()

	// 构建更新任务计划事件数据
	event := ErrorEvent{
		Id:           msgId,
		Type:         "error",
		Timestamp:    timestamp,
		Message:      msg,
		TargetIssues: issues,
	}

	// 构建更新任务计划更新消息
	planUpdateUpdate := ErrorUpdate{
		ID:        msgId,
		Type:      "event",
		SessionID: sessionId,
		Timestamp: timestamp,
		Event:     event,
	}

	// 构建发送给服务器的消息
	planUpdateContent := ErrorUpdateContent{
		Type:    AgentMsgTypeError,
		Content: planUpdateUpdate,
	}

	// 通过发送通道发送消息
	return a.enqueueForTask(sessionId, planUpdateContent)

}
