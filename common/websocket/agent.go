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

package websocket

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
	"github.com/gorilla/websocket"
	"trpc.group/trpc-go/trpc-go/log"
	// "gorm.io/datatypes"
)

const (
	// WebSocket相关常量
	maxMessageSize    = 512 * 1024 * 1024 // 512MB
	pongWait          = 120 * time.Second
	pingPeriod        = (pongWait * 8) / 10
	writeWait         = 60 * time.Second
	WSMsgTypeRegister = "register"
	// WSMsgTypeTaskAssign = "task_assign" // 任务分配
	WSMsgTypeDisconnect = "disconnect" // 主动断开连接的消息类型

	// Agent 端事件类型（与前端 SSE 事件类型一致）
	WSMsgTypeLiveStatus   = "liveStatus"   // 存活状态
	WSMsgTypePlanUpdate   = "planUpdate"   // 计划更新
	WSMsgTypeNewPlanStep  = "newPlanStep"  // 新计划步骤
	WSMsgTypeStatusUpdate = "statusUpdate" // 状态更新
	WSMsgTypeToolUsed     = "toolUsed"     // 工具使用
	WSMsgTypeResultUpdate = "resultUpdate" // 结果更新
	WSMsgTypeActionLog    = "actionLog"    // 日志
	WSMsgTypeError        = "error"        // 日志
)

// Agent 端事件消息（Agent -> Server，直接使用 task.go 中的结构体）
// 注意：Agent 端返回的事件体直接使用 task.go 中定义的结构体：
// - LiveStatusEvent
// - PlanUpdateEvent
// - PlanTaskItem
// - NewPlanStepEvent
// - StatusUpdateEvent
// - ToolUsedEvent
// 这样可以确保格式完全一致，避免重复定义

// AgentConnection 管理单个agent的连接
type AgentConnection struct {
	conn    *websocket.Conn
	agentID string

	// 细粒度的锁控制
	stateMu sync.RWMutex // 保护连接状态（agentID, isActive）
	writeMu sync.Mutex   // 保护写操作（发送消息）

	isActive         bool
	lifecycleMu      sync.Mutex // 分配与失联清理互斥，不阻塞 socket 读循环。
	ownedTasks       map[string]struct{}
	stopOnce         sync.Once
	done             chan struct{}
	failureReason    string
	eventsIncomplete bool
}

// AgentManager 管理所有agent连接
type AgentManager struct {
	connections map[string]*AgentConnection
	mu          sync.RWMutex
	taskManager *TaskManager // 新增：引用 TaskManager
	// store       *database.AgentStore // 注释掉数据库字段
}

// 注册/心跳消息内容
type AgentRegisterContent struct {
	AgentID      string   `json:"agent_id" validate:"required"` // 必需字段
	Hostname     string   `json:"hostname" validate:"required"` // 必需字段
	IP           string   `json:"ip" validate:"required,ip"`    // 必需且必须是IP格式
	Version      string   `json:"version" validate:"required"`  // 必需字段
	Capabilities []string `json:"capabilities,omitempty"`       // 可选字段
	Meta         string   `json:"meta,omitempty"`               // 可选字段
}

// 断开连接消息内容
type DisconnectContent struct {
	AgentID string `json:"agent_id" validate:"required"` // 必需字段
	Reason  string `json:"reason,omitempty"`             // 可选字段
}

// 全局验证器实例
var validate *validator.Validate

// 初始化验证器
func init() {
	validate = validator.New()
}

// formatValidationErrors 格式化验证错误信息
func formatValidationErrors(err error) string {
	if validationErrors, ok := err.(validator.ValidationErrors); ok {
		var errorMessages []string
		for _, fieldError := range validationErrors {
			fieldName := fieldError.Field()
			switch fieldError.Tag() {
			case "required":
				errorMessages = append(errorMessages,
					fmt.Sprintf("缺少必需字段: %s", fieldName))
			case "ip":
				errorMessages = append(errorMessages,
					fmt.Sprintf("字段 %s 必须是有效的IP地址", fieldName))
			case "email":
				errorMessages = append(errorMessages,
					fmt.Sprintf("字段 %s 必须是有效的邮箱格式", fieldName))
			case "url":
				errorMessages = append(errorMessages,
					fmt.Sprintf("字段 %s 必须是有效的URL", fieldName))
			case "min":
				errorMessages = append(errorMessages,
					fmt.Sprintf("字段 %s 长度不能小于 %s", fieldName, fieldError.Param()))
			case "max":
				errorMessages = append(errorMessages,
					fmt.Sprintf("字段 %s 长度不能大于 %s", fieldName, fieldError.Param()))
			default:
				errorMessages = append(errorMessages,
					fmt.Sprintf("字段 %s 验证失败: %s", fieldName, fieldError.Tag()))
			}
		}
		return fmt.Sprintf("验证失败: %s", strings.Join(errorMessages, "; "))
	}
	return "验证失败"
}

// NewAgentManager 创建新的AgentManager
// func NewAgentManager(store *database.AgentStore) *AgentManager {
func NewAgentManager() *AgentManager {
	return &AgentManager{
		connections: make(map[string]*AgentConnection),
		// store:       store,
	}
}

// NewAgentConnection 创建新的AgentConnection
// func NewAgentConnection(conn *websocket.Conn, store *database.AgentStore) *AgentConnection {
func NewAgentConnection(conn *websocket.Conn) *AgentConnection {
	return &AgentConnection{
		conn: conn,
		// store:    store,
		isActive:   true,
		ownedTasks: make(map[string]struct{}),
		done:       make(chan struct{}),
	}
}

// HandleAgentWebSocket 处理agent的WebSocket连接
func (am *AgentManager) HandleAgentWebSocket() gin.HandlerFunc {

	return func(c *gin.Context) {
		conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			log.Errorf("WebSocket升级失败: error=%v", err)
			return
		}

		// ac := NewAgentConnection(conn, am.store)
		ac := NewAgentConnection(conn)
		log.Infof("新的Agent连接建立: remoteAddr=%s", conn.RemoteAddr().String())
		go ac.handleConnection(am)
	}
}

// handleConnection 持续读取控制帧，业务消息交给单消费者顺序处理。
func (ac *AgentConnection) handleConnection(am *AgentManager) {
	const queueCapacity = 256
	const queueBytesLimit = 16 * 1024 * 1024
	type pendingMessage struct{ data []byte }
	queue := make(chan pendingMessage, queueCapacity)
	var pendingBytes int64
	var pendingMu sync.Mutex
	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		for message := range queue {
			ac.processMessage(am, message.data)
			pendingMu.Lock()
			pendingBytes -= int64(len(message.data))
			pendingMu.Unlock()
		}
	}()
	defer func() {
		ac.stop("Agent connection disconnected; scan incomplete, start a new scan to retry")
		close(queue)
		<-consumerDone // 已接受事件先完成持久化，再记录连接失败，保持历史顺序。
		ac.cleanup(am)
	}()
	ac.conn.SetReadLimit(maxMessageSize)
	ac.conn.SetReadDeadline(time.Now().Add(pongWait))
	ac.conn.SetPongHandler(func(string) error { return ac.conn.SetReadDeadline(time.Now().Add(pongWait)) })
	ac.conn.SetPingHandler(func(data string) error { return ac.writeControl(websocket.PongMessage, []byte(data)) })
	ac.conn.SetCloseHandler(func(code int, text string) error {
		return ac.writeControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, ""))
	})
	go ac.writePump()
	for {
		_, message, err := ac.conn.ReadMessage()
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				log.Warnf("Agent读取心跳超时: agentId=%s, error=%v", ac.id(), err)
			} else if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Infof("Agent连接关闭: agentId=%s", ac.id())
			} else {
				log.Warnf("Agent连接中断: agentId=%s, error=%v", ac.id(), err)
			}
			return
		}
		pendingMu.Lock()
		fits := pendingBytes+int64(len(message)) <= queueBytesLimit
		if fits {
			pendingBytes += int64(len(message))
		}
		pendingMu.Unlock()
		if fits {
			select {
			case queue <- pendingMessage{message}:
				continue
			default:
			}
		}
		// 队列溢出意味着未接收的消息无法保证完整性；显式失败，禁止静默丢弃后继续成功。
		ac.stateMu.Lock()
		ac.eventsIncomplete = true
		ac.stateMu.Unlock()
		ac.stop("Agent event queue overflow; scan incomplete, persisted events retained; start a new scan to retry")
		log.Errorf("Agent事件队列溢出，关闭连接并保留已存事件: agentId=%s", ac.id())
		return
	}
}

func (ac *AgentConnection) processMessage(am *AgentManager, message []byte) {
	var wsMsg WSMessage
	if err := json.Unmarshal(message, &wsMsg); err != nil {
		ac.sendError("消息格式错误请检查JSON格式")
		return
	}
	switch wsMsg.Type {
	case WSMsgTypeRegister:
		ac.handleRegister(am, wsMsg.Content)
	case WSMsgTypeDisconnect:
		ac.handleDisconnect(am, wsMsg.Content)
	case WSMsgTypeLiveStatus, WSMsgTypePlanUpdate, WSMsgTypeNewPlanStep, WSMsgTypeStatusUpdate, WSMsgTypeToolUsed, WSMsgTypeResultUpdate, WSMsgTypeActionLog, WSMsgTypeError:
		ac.handleAgentEvent(am, wsMsg.Content, wsMsg.Type)
	default:
		ac.sendError(fmt.Sprintf("未知的消息类型: %s", wsMsg.Type))
	}
}

// handleRegister 处理注册消息
func (ac *AgentConnection) handleRegister(am *AgentManager, content interface{}) {
	contentBytes, _ := json.Marshal(content)
	var rc AgentRegisterContent
	if err := json.Unmarshal(contentBytes, &rc); err != nil {
		log.Errorf("Agent注册消息解析失败: error=%v", err)
		ac.sendError("注册消息格式错误")
		return
	}

	// 使用validator验证结构体
	if err := validate.Struct(rc); err != nil {
		errorMsg := formatValidationErrors(err)
		log.Errorf("Agent注册验证失败: agentId=%s, error=%s", rc.AgentID, errorMsg)
		ac.sendError(errorMsg)
		return
	}

	ac.lifecycleMu.Lock()
	select {
	case <-ac.done:
		ac.lifecycleMu.Unlock()
		return
	default:
	}
	ac.stateMu.Lock()
	if ac.agentID != "" && ac.agentID != rc.AgentID {
		ac.stateMu.Unlock()
		ac.lifecycleMu.Unlock()
		ac.sendError("连接不能更改Agent ID")
		return
	}
	ac.agentID = rc.AgentID
	ac.stateMu.Unlock()
	// 指针交换在同一临界区完成；旧连接 cleanup 只能移除自身。
	am.mu.Lock()
	existingConn := am.connections[rc.AgentID]
	am.connections[rc.AgentID] = ac
	am.mu.Unlock()
	ac.lifecycleMu.Unlock()
	if existingConn != nil && existingConn != ac {
		existingConn.stop("Agent connection replaced; previous scan interrupted, persisted events retained")
	}
	log.Infof("Agent注册成功: agentId=%s, hostname=%s, ip=%s, version=%s", rc.AgentID, rc.Hostname, rc.IP, rc.Version)
	// 发送注册成功响应
	response := WSMessage{
		Type: "register_ack",
		Content: Response{
			Status:  0,
			Message: "注册成功",
		},
	}
	ac.writeJSON(response)
}

// handleDisconnect 处理主动断开连接
func (ac *AgentConnection) handleDisconnect(am *AgentManager, content interface{}) {
	contentBytes, _ := json.Marshal(content)
	var dc DisconnectContent
	if err := json.Unmarshal(contentBytes, &dc); err != nil {
		ac.sendError("断开连接消息格式错误")
		return
	}

	// 使用validator验证结构体
	if err := validate.Struct(dc); err != nil {
		errorMsg := formatValidationErrors(err)
		ac.sendError(errorMsg)
		return
	}

	// 验证身份一致性
	ac.stateMu.RLock()
	agentID := ac.agentID
	ac.stateMu.RUnlock()

	if agentID == "" || agentID != dc.AgentID {
		ac.sendError("断开连接消息身份验证失败")
		return
	}

	// 发送断开确认
	response := WSMessage{
		Type: "disconnect_ack",
		Content: Response{
			Status:  0,
			Message: "断开连接成功",
		},
	}
	ac.writeJSON(response)

	ac.stop("Agent requested disconnect; scan incomplete, persisted events retained")
}

// writePump 的生命周期随连接立即终止；全部写操作共享 writeMu。
func (ac *AgentConnection) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ac.done:
			return
		case <-ticker.C:
			if err := ac.writeControl(websocket.PingMessage, nil); err != nil {
				ac.stop("Agent heartbeat write failed; scan incomplete")
				return
			}
		}
	}
}

func (ac *AgentConnection) id() string {
	ac.stateMu.RLock()
	defer ac.stateMu.RUnlock()
	return ac.agentID
}

func (ac *AgentConnection) writeJSON(message interface{}) error {
	ac.writeMu.Lock()
	defer ac.writeMu.Unlock()
	if err := ac.conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
		return err
	}
	return ac.conn.WriteJSON(message)
}

func (ac *AgentConnection) writeControl(kind int, data []byte) error {
	ac.writeMu.Lock()
	defer ac.writeMu.Unlock()
	return ac.conn.WriteControl(kind, data, time.Now().Add(writeWait))
}

// stop 先禁止新任务分配，再关闭 socket 唤醒阻塞读取；重复调用保留首次故障原因。
func (ac *AgentConnection) stop(reason string) {
	ac.lifecycleMu.Lock()
	defer ac.lifecycleMu.Unlock()
	ac.stopOnce.Do(func() {
		ac.stateMu.Lock()
		ac.isActive = false
		ac.failureReason = reason
		ac.stateMu.Unlock()
		close(ac.done)
		if ac.conn != nil {
			ac.conn.Close()
		}
	})
}

// cleanup 仅清理此连接分配的任务，不按可复用的 AgentID 扫描全库。
func (ac *AgentConnection) cleanup(am *AgentManager) {
	ac.stop("Agent connection disconnected; scan incomplete, persisted events retained")
	agentID := ac.id()
	am.mu.Lock()
	if am.connections[agentID] == ac {
		delete(am.connections, agentID)
	}
	tm := am.taskManager
	am.mu.Unlock()
	ac.lifecycleMu.Lock()
	defer ac.lifecycleMu.Unlock()
	if tm == nil {
		return
	}
	for sessionID := range ac.ownedTasks {
		tm.failDisconnectedTask(sessionID, ac)
		delete(ac.ownedTasks, sessionID)
	}
}

// sendError 发送错误响应
func (ac *AgentConnection) sendError(message string) {
	response := WSMessage{
		Type: "error",
		Content: Response{
			Status:  1,
			Message: message,
		},
	}

	if err := ac.writeJSON(response); err != nil {
		ac.stop("Agent write failed; scan incomplete")
	}
}

// 通用事件处理函数
func (ac *AgentConnection) handleAgentEvent(am *AgentManager, content interface{}, eventType string) {
	contentBytes, err := json.Marshal(content)
	if err != nil {
		log.Errorf("Agent事件序列化失败: agentId=%s, eventType=%s, error=%v", ac.agentID, eventType, err)
		ac.sendError(fmt.Sprintf("%s事件序列化失败: %v", eventType, err))
		return
	}

	var eventMessage TaskEventMessage
	if err := json.Unmarshal(contentBytes, &eventMessage); err != nil {
		log.Errorf("Agent事件格式错误: agentId=%s, eventType=%s, error=%v", ac.agentID, eventType, err)
		ac.sendError(fmt.Sprintf("%s事件格式错误: %v", eventType, err))
		return
	}

	// 使用validator验证TaskEventMessage
	if err := validate.Struct(eventMessage); err != nil {
		errorMsg := formatValidationErrors(err)
		log.Errorf("Agent事件验证失败: agentId=%s, eventType=%s, error=%s", ac.agentID, eventType, errorMsg)
		ac.sendError(fmt.Sprintf("%s事件验证失败: %s", eventType, errorMsg))
		return
	}

	// 从TaskEventMessage中提取sessionId和事件数据
	sessionId := eventMessage.SessionID
	event := eventMessage.Event

	log.Debugf("收到Agent事件: agentId=%s, sessionId=%s, eventType=%s", ac.agentID, sessionId, eventType)

	ac.lifecycleMu.Lock()
	_, owned := ac.ownedTasks[sessionId]
	ac.lifecycleMu.Unlock()
	if !owned {
		log.Warnf("忽略非该连接任务的事件: agentId=%s, sessionId=%s", ac.id(), sessionId)
		return
	}
	am.mu.RLock()
	tm := am.taskManager
	am.mu.RUnlock()
	if tm != nil {
		ownership := tm.ownerFor(sessionId)
		ownership.mu.Lock()
		if ownership.connection != ac {
			ownership.mu.Unlock()
			return
		}
		ac.stateMu.RLock()
		incomplete := ac.eventsIncomplete
		ac.stateMu.RUnlock()
		var err error
		if incomplete && eventType == WSMsgTypeResultUpdate {
			// 溢出前排队的报告仍保存为证据，但不能把不完整的事件流标为成功。
			err = tm.handleEvent(sessionId, eventType, event)
		} else {
			err = tm.HandleAgentEvent(sessionId, eventType, event)
		}
		ownership.mu.Unlock()
		if err != nil {
			log.Errorf("Agent事件处理失败: sessionId=%s, error=%v", sessionId, err)
			ac.stop("Agent event persistence failed; scan incomplete, persisted events retained")
		}
	}
}

// 添加获取可用 Agent 的方法
func (am *AgentManager) GetAvailableAgents() []*AgentConnection {
	am.mu.RLock()
	defer am.mu.RUnlock()

	var availableAgents []*AgentConnection
	for _, conn := range am.connections {
		conn.stateMu.RLock()
		if conn.isActive {
			availableAgents = append(availableAgents, conn)
		}
		conn.stateMu.RUnlock()
	}
	return availableAgents
}

// SetTaskManager 设置 TaskManager 引用
func (am *AgentManager) SetTaskManager(taskManager *TaskManager) {
	am.mu.Lock()
	defer am.mu.Unlock()
	am.taskManager = taskManager
}

// StatusHandler 提供部署验收需要的已注册连接状态，不包含任务或鉴权信息。
func (am *AgentManager) StatusHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		type agentStatus struct {
			AgentID   string `json:"agent_id"`
			Connected bool   `json:"connected"`
		}
		agents := make([]agentStatus, 0)
		for _, ac := range am.GetAvailableAgents() {
			agents = append(agents, agentStatus{ac.id(), true})
		}
		sort.Slice(agents, func(i, j int) bool { return agents[i].AgentID < agents[j].AgentID })
		c.JSON(http.StatusOK, gin.H{"connected_count": len(agents), "agents": agents})
	}
}
