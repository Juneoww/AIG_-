package database

import (
	"encoding/json"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// GetSessionStatus 仅查询状态，不加载用户、参数或历史事件；详情接口仍使用 GetSession。
func (s *TaskStore) GetSessionStatus(id string) (string, error) {
	var session Session
	err := s.db.Select("status").First(&session, "id = ?", id).Error
	return session.Status, err
}

// FailOwnedSession 原子保存失联状态与错误证据；仅修改仍由指定 Agent 执行的任务。
// 连接代际的隔离由调用方持有的任务集合保证，不能按 AgentID 批量清理。
func (s *TaskStore) FailOwnedSession(id, agentID, messageID string, event interface{}) (bool, error) {
	data, err := json.Marshal(event)
	if err != nil {
		return false, err
	}
	changed := false
	err = s.db.Transaction(func(tx *gorm.DB) error {
		now := time.Now().UnixMilli()
		result := tx.Model(&Session{}).Where("id = ? AND assigned_agent = ? AND status = ?", id, agentID, "doing").Updates(map[string]interface{}{"status": "error", "completed_at": now, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		changed = true
		return tx.Create(&TaskMessage{ID: messageID, SessionID: id, Type: "error", EventData: datatypes.JSON(data), Timestamp: now, CreatedAt: now}).Error
	})
	return changed && err == nil, err
}
