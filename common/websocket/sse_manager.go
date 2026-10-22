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
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"trpc.group/trpc-go/trpc-go/log"
)

// SSEConnection 表示一个SSE连接
type SSEConnection struct {
	SessionID string
	Username  string
	Writer    http.ResponseWriter
	Flusher   http.Flusher
	CloseChan chan bool
	LastPing  time.Time
	writeMu   sync.Mutex
	closeOnce sync.Once
}

// SSEManager 管理SSE连接和事件推送
type SSEManager struct {
	connections  map[string]*SSEConnection // sessionId -> connection
	mutex        sync.RWMutex
	writeTimeout time.Duration
}

// NewSSEManager 创建新的SSE管理器
func NewSSEManager() *SSEManager {
	timeout := 5 * time.Second
	if value := os.Getenv("AIG_SSE_WRITE_TIMEOUT"); value != "" {
		if duration, err := time.ParseDuration(value); err == nil && duration > 0 {
			timeout = duration
		} else {
			log.Warn("AIG_SSE_WRITE_TIMEOUT无效，使用5s默认值")
		}
	}
	return &SSEManager{
		connections:  make(map[string]*SSEConnection),
		writeTimeout: timeout,
	}
}

// AddConnection 保留旧接口；HTTP 处理器使用连接句柄隔离同会话重连。
func (sm *SSEManager) AddConnection(sessionID, username string, w http.ResponseWriter) error {
	_, err := sm.addConnection(sessionID, username, w)
	return err
}

func (sm *SSEManager) addConnection(sessionID, username string, w http.ResponseWriter) (*SSEConnection, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("streaming unsupported")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Cache-Control")
	conn := &SSEConnection{SessionID: sessionID, Username: username, Writer: w, Flusher: flusher, CloseChan: make(chan bool), LastPing: time.Now()}
	// 连接成功帧发送完成后再发布，确保业务事件不会抢在 connected 之前。
	// 此处不持有管理器锁，慢客户端不影响其他任务的连接管理。
	if err := sm.sendEventToConnection(conn, "connected", "connected", map[string]interface{}{"message": "SSE连接已建立", "sessionId": sessionID}); err != nil {
		return nil, err
	}
	sm.mutex.Lock()
	previous := sm.connections[sessionID]
	sm.connections[sessionID] = conn
	sm.mutex.Unlock()
	if previous != nil {
		previous.close()
	}
	go sm.keepConnectionAlive(conn)
	return conn, nil
}

func (conn *SSEConnection) close() { conn.closeOnce.Do(func() { close(conn.CloseChan) }) }

// waitForWrites 必须在 HTTP 处理器返回前调用，防止 ResponseWriter 被回收时仍有写操作。
func (conn *SSEConnection) waitForWrites() { conn.writeMu.Lock(); conn.writeMu.Unlock() }

func (sm *SSEManager) keepConnectionAlive(conn *SSEConnection) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-conn.CloseChan:
			return
		case <-ticker.C:
			heartbeat := TaskEventMessage{ID: fmt.Sprintf("heartbeat_%d", time.Now().Unix()), Type: "liveStatus", SessionID: conn.SessionID, Timestamp: time.Now().Unix(), Event: LiveStatusEvent{ID: fmt.Sprintf("heartbeat_%d", time.Now().Unix()), Type: "liveStatus", Timestamp: time.Now().UnixMilli(), Text: "思考中..."}}
			data, err := json.Marshal(heartbeat)
			if err != nil {
				continue
			}
			if err := sm.writeFrame(conn, fmt.Sprintf("data: %s\n\n", data)); err != nil {
				return
			}
		}
	}
}

// removeConnection 按连接指针移除，旧订阅的延迟退出不能删除新订阅。
func (sm *SSEManager) removeConnection(conn *SSEConnection) {
	sm.mutex.Lock()
	if sm.connections[conn.SessionID] == conn {
		delete(sm.connections, conn.SessionID)
	}
	sm.mutex.Unlock()
	conn.close()
}

func (sm *SSEManager) RemoveConnection(sessionID string) {
	sm.mutex.RLock()
	conn := sm.connections[sessionID]
	sm.mutex.RUnlock()
	if conn != nil {
		sm.removeConnection(conn)
	}
}

// SendEvent 向指定会话发送事件
func (sm *SSEManager) SendEvent(id string, sessionID string, eventType string, event interface{}) error {
	sm.mutex.RLock()
	conn, exists := sm.connections[sessionID]
	sm.mutex.RUnlock()

	if !exists {
		log.Warnf("SSE连接不存在，跳过事件推送: sessionId=%s, eventType=%s", sessionID, eventType)
		return fmt.Errorf("连接不存在: sessionId=%s", sessionID)
	}

	log.Debugf("SSE事件推送: sessionId=%s, eventType=%s, eventId=%s", sessionID, eventType, id)
	return sm.sendEventToConnection(conn, id, eventType, event)
}

// sendEventToConnection 向单个连接发送事件
func (sm *SSEManager) sendEventToConnection(conn *SSEConnection, id string, eventType string, event interface{}) error {
	// 创建事件消息
	eventMessage := TaskEventMessage{
		ID:        id,
		Type:      eventType,
		SessionID: conn.SessionID,
		Timestamp: time.Now().Unix(),
		Event:     event,
	}

	// 序列化事件
	eventData, err := json.Marshal(eventMessage)
	if err != nil {
		log.Errorf("SSE事件序列化失败: sessionId=%s, eventType=%s, error=%v", conn.SessionID, eventType, err)
		return fmt.Errorf("序列化事件失败: %v", err)
	}

	return sm.writeFrame(conn, fmt.Sprintf("id: %s\nevent: %s\ndata: %s\n\n", id, eventType, eventData))
}

// writeFrame 在底层 HTTP 连接上设置真实写截止并覆盖 Flush，全程不创建可遗留的写协程。
func (sm *SSEManager) writeFrame(conn *SSEConnection, frame string) error {
	conn.writeMu.Lock()
	defer conn.writeMu.Unlock()
	select {
	case <-conn.CloseChan:
		return fmt.Errorf("SSE connection closed")
	default:
	}
	// Gin 的 Flush() 不返回底层错误；解包后使用原生 FlushError 才能观察截止失败。
	responseWriter := conn.Writer
	for {
		wrapper, ok := responseWriter.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			break
		}
		responseWriter = wrapper.Unwrap()
	}
	controller := http.NewResponseController(responseWriter)
	timeout := sm.writeTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	if err := controller.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		sm.removeConnection(conn)
		return fmt.Errorf("SSE writer cannot enforce deadline: %w", err)
	}
	if _, err := fmt.Fprint(conn.Writer, frame); err != nil {
		sm.removeConnection(conn)
		return fmt.Errorf("SSE write failed: %w", err)
	}
	if err := controller.Flush(); err != nil {
		sm.removeConnection(conn)
		return fmt.Errorf("SSE flush failed: %w", err)
	}
	if err := controller.SetWriteDeadline(time.Time{}); err != nil {
		sm.removeConnection(conn)
		return fmt.Errorf("SSE deadline reset failed: %w", err)
	}
	conn.LastPing = time.Now()
	return nil
}

// GetConnectionCount 获取当前连接数
func (sm *SSEManager) GetConnectionCount() int {
	sm.mutex.RLock()
	defer sm.mutex.RUnlock()
	count := len(sm.connections)
	log.Debugf("SSE连接数统计: count=%d", count)
	return count
}

// GetConnectionsByUser 获取指定用户的连接
func (sm *SSEManager) GetConnectionsByUser(username string) []string {
	sm.mutex.RLock()
	defer sm.mutex.RUnlock()

	var sessionIDs []string
	for sessionID, conn := range sm.connections {
		if conn.Username == username {
			sessionIDs = append(sessionIDs, sessionID)
		}
	}

	log.Debugf("用户SSE连接查询: username=%s, connectionCount=%d", username, len(sessionIDs))
	return sessionIDs
}

// HasConnection 检查指定sessionId的连接是否存在
func (sm *SSEManager) HasConnection(sessionID string) bool {
	sm.mutex.RLock()
	defer sm.mutex.RUnlock()
	_, exists := sm.connections[sessionID]
	return exists
}
