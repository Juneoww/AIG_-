# 实施前审阅依据

日期：2026-10-14；基线分支 `feat/frontend-ui-redesign`，HEAD `c2c1854`。这些是静态源码审阅结论，未做运行时复现。

| 问题 | 源码/文档依据 | 计划归属 |
| --- | --- | --- |
| 创建任务等待 SSE，最长 100 秒 | `common/websocket/task_manager.go` 的 AddTask 预存、等待 HasConnection、dispatchTask | D3 |
| 慢订阅可能阻塞终态更新 | `HandleAgentEvent` 先调用 handleEvent；handleEvent 保存后同步 SendEvent，返回后才更新 error/done | D3：有界推送与阻塞 Writer 测试 |
| 旧连接断开可能误删新连接 | `sse_manager.go` 以 sessionId 替换/删除；`task.go` 请求取消后按 sessionId 清理 | D3：连接身份校验、关闭幂等和并发测试 |
| 查到记录不证明分发成功 | AddTask 在内存登记/分发前预存；dispatchTask 在模型增强和发送前写 assigned_agent；失败调用 cleanupFailedTask 删除；详情失败统一 500 | D1 固定契约、D3 响应区分、D4 对账 |
| 基线文档归属不统一 | 旧计划将基线写入 UI_design | 本轮改为 System_Split/04_功能与接口基线.md，UI 后续引用 |
| UI 回退依赖创建时序兼容 | 旧后端等 SSE，新前端计划 POST 后订阅 | D7：兼容矩阵与分别验证 UI/完整拓扑回退 |

构建与部署现状：Go `server.go` 内嵌产品 static；Dockerfile 先构建前端再编译 Go；Compose 是 webserver/agent 两服务。前端静态服务、API-only 模式及三服务配置均为待实施产物。

工作区保留项：`.dockerignore`、`.gitignore`、旧文档删除、其他未跟踪文档、release 产物、既有内网打包与升级脚本。新增构建和离线清单实施时需核对这些改动，不能默认覆盖。
