# v1.1.1 扫描停滞修复与内网平台升级实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development or executing-plans task by task. 本轮按独立文件范围使用dispatching-parallel-agents，并逐项进行验证与独立复审。

**Goal:** 修复v1.1.0现场后处理无限等待、事件处理退化与Agent断联状态遗留，交付Linux amd64纯程序离线升级与回滚文件。

**Architecture:** 从现场交付提交c2c18547cef9276c376f86057a5387b7e61bfa62建立独立维护worktree，不混入主工作区未完成的恢复/前后端拆分。保留旧Web/Agent双容器及接口，轻量读取任务状态；Web读循环与顺序事件消费解耦；后处理有真实context并记录完成状态，保留基础检测结果。平台交付仅含程序，三个规则目录保持现场共享挂载。

**Tech Stack:** Go 1.23、Gin/GORM/SQLite/Gorilla、Rod/Chromium、Docker Compose、Python标准库/Bash离线升级工具。

## 已接受范围
用户已基于故障记录要求修复并更新内网。任务采用v1.1.1维护版，保留full_tcp默认、一期输入格式和旧前端。初版不实现持久化断点续扫；断联任务保留已回传事件并明确失败，可由用户重新扫描，禁止静默重派或伪造成功。截图/上传失败保留规则检测结果与明确证据缺口。

## Task 1 后处理可终止且保留检测结果
Files: common/agent/tasks.go、新postprocess模块与回归测试；common/runner/ai.go；common/utils/chromium/screenshot.go；common/utils/api.go；common/runner/types.go按需增加可选状态字段。
- [ ] 先写失败测试：155结果中1截图阻塞，其他结果保留、最终报告有可见postprocess状态；取消及时结束且无goroutine泄漏。
- [ ] 截图创建/连接/导航/截图/关闭贯穿context，清理另有短截止；不以放弃仍运行后台goroutine冒充超时。
- [ ] 上传使用request context和超时；AI后处理使用派生context，操作时限由环境配置且保留保守默认。
- [ ] 保留公共旧函数包装以兼容其他调用；截图与上传失败不删除基础结果，额外字段兼容旧前端，并在现有日志显示目标脱敏身份与阶段。
- [ ] 测试先RED后GREEN；真实Linux Chromium回环慢响应/取消验证不触及现场IP。

## Task 2 Web事件与Agent状态
Files: common/websocket/agent.go、task_manager.go、新连接回归测试；pkg/database/task.go及新轻量状态/失联更新模块。
- [ ] 先写失败测试：状态检查不读取Messages；慢事件消费仍持续读取Pong；同连接事件有序；连接更替不误删新连接或错误结束新任务。
- [ ] 状态检查只查必要Session列；不改变任务详情GetSession加载契约。
- [ ] 使用有界队列与单顺序消费者隔离事件持久化和socket读取；队列溢出明确失败并保留已入库事件，不能静默丢数据；多个连接无全局长锁。
- [ ] 所有Web写经同一writer锁；心跳日志正确区分超时和正常关闭。
- [ ] 失联更新只针对该连接仍负责的doing任务；同AgentID替换须防旧cleanup干扰新连接；不自动重新派发。
- [ ] 新旧API兼容；必要的error事件存库并发SSE，Agent重启旧任务不永久doing。
- [ ] 定向与race测试、真实WebSocket批量事件回环验证。

## Task 3 端口发现可取消并受控并发
Files: common/utils/nmap.go；common/portscan新增批量调度与测试；common/agent/tasks.go只由任务1负责人集成调用；common/runner/runner.go。
- [ ] 先写失败测试：多个IP有限并发、全端口参数保留、稳定去重、单目标错误显式记录、取消停止新进程并回收运行进程。
- [ ] 引入NmapScanContext并保留NmapScan兼容；按小批次或有限worker执行，避免每IP串行阻塞整段。
- [ ] 不设置会静默删减覆盖的短host-timeout；如配置超时，任务明确失败/未完成，不当作无开放端口。
- [ ] 旧前端回传已处理/总数并节流，保留每目标错误信息；无需新增UI重设计。

## Task 4 纯平台交付与运维工具
Files: release/v1.1.1/、scripts/platform/、打包工具及隔离测试；版本常量；API中英说明及swagger按字段变化同步。
- [ ] 记录旧镜像ID和源码完整提交；从既有已验证runtime镜像导出文件系统，移除三个基础设施规则目录，使用FROM scratch建立干净单层运行时，再加入本轮新编译二进制，保留原镜像运行配置/入口/依赖。
- [ ] 验证新镜像每层及最终文件系统都没有三个目录的规则文件；缺少外部规则挂载时启动拒绝扫描，不使用镜像默认规则。不得复用包含规则的旧镜像层作为新发布镜像基础。
- [ ] 交付deploy-platform-v1.1.1.sh与AIG-platform-v1.1.1-linux-amd64.tar.gz；manifest包含精确镜像身份、文件集合/摘要/架构/源提交/版本/无迁移声明和contains_infrastructure_rules=false。
- [ ] 脚本支持--check/默认升级/--rollback --backup/--help，离线运行。发现唯一原双服务与完整Compose、挂载/卷/端口，兼容原两个锁。
- [ ] 预检拒绝未知布局、非共享规则挂载、不支持架构、坏包、空间不足、活动任务；不为旧卡死任务静默改状态。维护期停止写入、备份db/uploads/logs/配置/其他data与API Checker卷，原规则只读摘要。
- [ ] 新建/opt/aig/v1.1.1，继承数据与环境、原端口和共享规则，复制到独立API Checker卷；不覆盖旧目录/旧卷。失败恢复旧配置，显式回滚不静默丢新版数据。
- [ ] 适配独立规则管理工具对v1.1.1布局/镜像的兼容性，或交付兼容工具更新；不可要求改旧脚本绕过版本检查。
- [ ] 使用真实Docker隔离部署测试升级成功、无修改预检、规则摘要不变、坏包拒绝、活动任务拒绝、失败恢复和回滚；不创建GitHub Release、不操作用户内网主机。

## Task 5 完整验证与交付
- [ ] 运行基线及新增定向Go测试；已知旧Agent测试含失效符号/外部依赖，记录基线，新增测试可显式生产文件列表运行；不得把既有失败算通过。
- [ ] 按改动运行go test ./...并报告遗留/环境失败；变更不触及规则内容。Linux amd64两程序构建与真实Chromium慢响应测试通过。
- [ ] 独立审阅计划及最终补丁和升级工具；修复实质问题后再次验证。
- [ ] 核对最终交付两文件SHA256和文件内容，提供本地绝对路径、上传位置、单条升级命令与回滚方式。

## 外部约束
现场仍为v1.1.0双容器、共享规则/opt/aig/rule-data的假设已向用户发出澄清；在回复前可以完成代码/隔离验证，现场升级工具预检必须实际识别并拒绝不匹配布局。
平台与规则契约来源：主工作区docs/AI-Infra-Scan_Plus/platform-upgrade-contract.md、infrastructure-rules-upgrade-contract.md。实现人员必须完整阅读。

## 计划审阅补充（已采纳）
- 报告必须聚合成功、失败、未完成和后处理失败；155成功/21失败、全部失败和取消均不得显示全量覆盖成功，旧前端可见日志同步说明。
- 维护入口先阻断提交/规则编辑，再查空闲、停止写入、复查和备份；验收必须实际 Agent 注册、API Checker、规则接口和历史数据，不能只看容器健康。
- 用户已确认仍使用 `/opt/aig/v1.1.0`、`/opt/aig/rule-data` 和两个现有容器；旧卡死任务需用户通过终止入口明确结束后再升级。
