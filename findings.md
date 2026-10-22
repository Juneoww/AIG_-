# 调查依据

- 旧 v1.1.0 实际 Web 二进制内前端 main-ddpTgx_P.js，含 scan-port-mode、portModeTitle、fullTcpDetail；v1.1.1 交付 Web 内 main-CIjG0L6_.js，缺少这三项。
- v1.1.1 源 frontend 已含完整 HttpHeaderDialog / ChatArea port_mode；checked-in common/websocket/static 仍旧。Go 通过 embed.FS 直接嵌入；维护发布漏了前端构建/复制。
- 后端 portscan.ModeDefault=full_tcp，Ports 返回 1-65535，未删除能力。当前问题是交付 UI 回归。
- 原 v1.1.1 升级器固定源为 v1.1.0，拒绝已升级现场。补丁须验证 /opt/aig/v1.1.1、aig-v111、两个 -v111 容器及实际 Compose/挂载。
- 本机已有原镜像、Go 工具链/模块缓存，主工作区 frontend/node_modules 可只读借用依赖；源码必须使用当前基线。
