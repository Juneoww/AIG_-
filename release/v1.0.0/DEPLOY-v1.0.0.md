# AI-Infra-Guard 内网部署文档（保姆级）

| 项目 | 内容 |
|---|---|
| 版本 | **v1.0.0** |
| 发布日期 | 2026-09-15 |
| 基于上游 | Tencent/AI-Infra-Guard main 分支（2026-09-14 代码快照） |
| 部署形态 | Docker Compose 双容器（webserver + agent），单机内网部署 |
| 对外端口 | 8088（Web 控制台 / API） |

> **本档适合谁**：第一次接触 Docker / 第一次部署本系统的同学。全文每条命令都标注了"在哪台机器上执行"、命令的作用、以及执行后应该看到什么。跟着从上往下做即可。

---

## 目录

1. [部署前必读：装完之后是什么样子](#1-部署前必读)
2. [准备阶段：需要收集的信息](#2-准备阶段)
3. [在内网机上安装 Docker（已装可跳过）](#3-内网机安装-docker)
4. [部署六步走](#4-部署六步走)
5. [Web 界面操作：配置模型 + 跑第一个扫描](#5-web-界面操作)
6. [日常运维速查](#6-日常运维速查)
7. [故障排查（按症状查）](#7-故障排查)
8. [卸载与回滚](#8-卸载与回滚)
9. [本版本验证记录](#9-本版本验证记录)

---

## 1. 部署前必读

### 1.1 这套系统装完之后是什么样子

一台内网服务器上跑两个 Docker 容器，你用浏览器访问它发起扫描：

```
┌────────────────────── 你的内网服务器 ──────────────────────┐
│                                                            │
│  ┌─────────────────────┐      ┌─────────────────────────┐  │
│  │  webserver 容器      │      │  agent 容器              │  │
│  │  (对外端口 8088)     │◄─────│  · 接收任务并执行        │  │
│  │  · Web 操作界面      │ 任务 │  · nmap 全端口扫描(1-65535)│
│  │  · 任务管理/数据库   │ 下发 │  · 指纹识别/漏洞验证     │  │
│  │  · 指纹+漏洞知识库   │─────►│  · API 漏洞验证(api_checker)│
│  └─────────────────────┘ 结果 └───────────┬─────────────┘  │
│                                           │ 扫描流量        │
└───────────────────────────────────────────┼────────────────┘
                                            ▼
                              内网中被扫描的目标机器（任意端口）
```

**你只需要记住**：装好后浏览器打开 `http://服务器IP:8088`，选"AI 基础设施扫描"，填一个内网 IP，系统会自动对该 IP 的全部 65535 个 TCP 端口做扫描并生成安全报告。

### 1.2 本版本相比官方原版的差异

1. **深度扫描改为全端口扫描**：官方只扫 `11434,1337,7000-9000,18789` 十几个端口，本版扫全部 1-65535 个 TCP 端口，且界面显示与实际行为一致。
2. nmap 加了 `--open`（只输出开放端口，避免全端口扫描时输出爆炸）。
3. agent 容器里的 nmap 授予了特殊权限，扫描速度比默认快数倍。
4. 放开了官方默认"拒绝扫描内网网段"的两项限制（内网部署必须放开，否则扫不动）。
5. 构建期走了国内源，产物功能与官方一致。

### 1.3 运行时需要连外网吗？

**不需要**。指纹库、漏洞库都随发布包离线提供（`data-v1.0.0.tar.gz`），系统内部自闭环。唯一需要的是：**内网里要有一台大模型（LLM）服务**，用来做 AI 分析和报告生成。如果你的团队还没有，先找运维/算法同事确认内网 LLM 的地址，没有它扫描任务无法完成 AI 报告环节。

---

## 2. 准备阶段

### 2.1 硬件与软件要求（内网部署机）

| 项目 | 最低要求 | 说明 |
|---|---|---|
| 操作系统 | Linux x86_64（Ubuntu 20.04+ / CentOS 8+ / 统信 UOS / 麒麟等） | 需要 root 或 sudo 权限 |
| 内存 | 8GB 以上 | agent 容器要预留 2GB 共享内存 |
| 磁盘 | 30GB 空闲 | 镜像解压后约 3.3GB + 数据/日志增长 |
| Docker | 24.0+ 与 Compose v2 | 没装的话看第 3 节离线安装 |
| 网络 | 能连通：被扫描网段（任意 TCP 端口出方向）、内网 LLM 服务 | 见 2.3 |

### 2.2 你需要拿到的东西（发布包文件）

> 本发布包镜像架构为 **linux/amd64**（x86_64 服务器）。在服务器上执行 `uname -m` 应输出 `x86_64`；如果输出 `aarch64`，此包不适用（需另行构建 arm64 版本）。

```
release/v1.0.0/
├── aig-images-v1.0.0-amd64.tar.gz ← 两个 Docker 镜像（webserver + agent，amd64 架构）
├── data-v1.0.0.tar.gz             ← 指纹+漏洞知识库（2MB）
├── docker-compose.intranet.yml    ← 部署编排文件
├── DEPLOY-v1.0.0.md               ← 本文档
└── SHA256SUMS.txt                 ← 完整性校验文件
```

**把这 5 个文件拷到内网机**，常用方式任选：
- **U 盘拷贝**（最简单，注意内网机 USB 策略是否允许）；
- **scp 传输**（有跳板机/堡垒机时）：
  ```bash
  # 在你能同时访问发布包和内网机的机器上执行（路径按实际改）
  scp release/v1.0.0/* user@内网机IP:/opt/aig/
  ```
- 企业内部网盘/文件共享中转。

### 2.3 部署前必须确认的三件事

在内网机上逐条执行，**三件都通过再往下走**：

```bash
# ① 确认 Docker 可用（没装看第 3 节）
docker --version && docker compose version
# 预期输出类似：Docker version 24.0.9  /  Docker Compose version v2.x.x

# ② 确认内网 LLM 服务可达（地址问运维/算法同事，下面换成实际地址）
curl http://LLM服务器IP:端口/v1/models
# 预期：返回一段 JSON（里面有模型列表）；返回 Connection refused/timeout 就是网络不通，先解决网络

# ③ 确认能连通被扫描网段（换成你要扫的一台测试机的 IP 和它的某个开放端口）
timeout 3 bash -c "echo > /dev/tcp/被扫测试机IP/端口" && echo "连通 OK"
```

---

## 3. 内网机安装 Docker

> **如果 `docker --version` 能正常输出版本号，直接跳到第 4 节。**
> 内网机没法 `apt/yum install docker`（要连外网源），下面用**静态二进制包离线安装**，适用于任何 x86_64 Linux。

### 3.1 在有外网的机器上下载两个文件

```bash
# Docker 引擎静态包（约 70MB，中科大镜像源）
curl -LO https://mirrors.ustc.edu.cn/docker-ce/linux/static/stable/x86_64/docker-27.3.1.tgz

# Docker Compose v2 独立二进制（约 60MB；GitHub 直连不通就加 gh-proxy 前缀）
curl -LO https://gh-proxy.com/https://github.com/docker/compose/releases/download/v2.29.7/docker-compose-linux-x86_64
```

### 3.2 拷到内网机并安装（以下命令都在内网机上，用 root 执行）

```bash
# ① 解压 Docker 引擎到 /usr/bin
tar -xzf docker-27.3.1.tgz
cp docker/* /usr/bin/

# ② 安装 compose 插件（放这个目录后 "docker compose" 命令可用）
mkdir -p /usr/local/lib/docker/cli-plugins
cp docker-compose-linux-x86_64 /usr/local/lib/docker/cli-plugins/docker-compose
chmod +x /usr/local/lib/docker/cli-plugins/docker-compose

# ③ 配置 systemd 开机自启
cat > /etc/systemd/system/docker.service <<'EOF'
[Unit]
Description=Docker Engine
After=network.target

[Service]
ExecStart=/usr/bin/dockerd
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

# ④ 启动并验证
systemctl daemon-reload
systemctl enable --now docker
docker --version && docker compose version
# 两条都有版本号输出 = 安装成功
```

---

## 4. 部署六步走

> 以下所有命令都在**内网部署机**上执行。假设发布包已放在 `/opt/aig/`（按你的实际路径替换）。

### 第 1 步：校验发布包完整性（可选但建议）

```bash
cd /opt/aig
sha256sum -c SHA256SUMS.txt
```

**预期输出**：5 行，每行 `文件名: OK`。如果出现 `FAILED`，说明文件在拷贝过程中损坏，重新拷贝该文件。

### 第 2 步：导入镜像

```bash
cd /opt/aig
docker load -i aig-images-v1.0.0-amd64.tar.gz
```

**预期输出**（约 1-3 分钟）：

```
Loaded image: ai-infra-guard-webserver:v1.0.0
Loaded image: ai-infra-guard-agent:v1.0.0
```

验证一下（顺便确认架构对了）：

```bash
docker images | grep ai-infra-guard
docker inspect ai-infra-guard-agent:v1.0.0 --format '{{.Os}}/{{.Architecture}}'
# 应输出：linux/amd64。若不是，说明导入的包不对
```

**预期**：能看到 2 行（webserver 和 agent 的 `v1.0.0` 标签各一条）。compose 编排文件引用的就是 `v1.0.0` 标签，且已配置 `pull_policy: never`（镜像缺失时直接报错，绝不会去连外网拉取）。

> **报错 `No space left on device`**：磁盘满了，`df -h` 看哪里满了，清理后再试。

### 第 3 步：准备部署目录

```bash
mkdir -p /opt/aig-app && cd /opt/aig-app
cp /opt/aig/docker-compose.intranet.yml .
tar -xzf /opt/aig/data-v1.0.0.tar.gz
```

**完成后目录结构必须是**（`data/` 和 compose 文件必须在同一层，这是最常见的出错点）：

```
/opt/aig-app/
├── docker-compose.intranet.yml
└── data/                  ← 刚解压出来的
    ├── fingerprints/      ← 指纹库
    └── vuln/ vuln_en/ 等
```

验证：`ls /opt/aig-app/data | head` 能看到若干文件/子目录即可。

### 第 4 步：启动服务

```bash
cd /opt/aig-app
docker compose -f docker-compose.intranet.yml up -d
```

**预期输出**：

```
[+] Running 3/3
 ✔ Network ai-infra-guard-network  Created
 ✔ Container ai-infra-guard-agent  Healthy
 ✔ Container ai-infra-guard-webserver Started
```

> 注意：agent 显示 Healthy 可能需要等 10-40 秒（有健康检查间隔）。如果 webserver 卡在 Waiting，稍等再执行下一步查看状态。

### 第 5 步：确认两个容器都健康

```bash
docker compose -f docker-compose.intranet.yml ps
```

**预期输出**：两行，STATUS 都带 `(healthy)`：

```
NAME                        STATUS
ai-infra-guard-agent        Up 1 minute (healthy)
ai-infra-guard-webserver    Up 1 minute (healthy)
```

**如果 STATUS 是 `(health: starting)`**：刚启动还没检查完，等 30 秒再执行一次。
**如果一直 `(unhealthy)` 或 `Restarting`**：直接跳到第 7 节按症状排查。

再看一眼 agent 是否注册成功（这一步能确认两个容器内部通信正常）：

```bash
docker logs ai-infra-guard-agent 2>&1 | grep -E "注册成功|connect server" | tail -3
```

**预期输出**包含：

```
connect server: webserver:8088
recv {"type":"register_ack",..."message":"注册成功"...}
```

> 开头若有一条 `lookup webserver: no such host` 报错可忽略——那是启动瞬间 webserver 还没就绪，程序会自动重连。

### 第 6 步：放行防火墙并打开页面

```bash
# 按部署机的防火墙二选一执行（都可以先执行看看，报 command not found 就换另一个）
firewall-cmd --add-port=8088/tcp --permanent && firewall-cmd --reload   # firewalld（CentOS/麒麟）
ufw allow 8088/tcp                                                      # ufw（Ubuntu）
```

然后在你自己的电脑浏览器打开：**http://内网服务器IP:8088**

**预期**：出现一个对话式的操作界面（底部有输入框），无登录页（开源版不需要登录）。

**打不开页面**：先在部署机本机验证服务是否正常：

```bash
curl -I http://localhost:8088/
# 预期第一行：HTTP/1.1 200 OK
```

- 本机 curl 是 200、外部浏览器打不开 → 是网络/防火墙问题（回看上面防火墙命令，或检查是否有安全组/网络隔离策略）；
- 本机 curl 也失败 → 回到第 5 步查容器状态。

---

## 5. Web 界面操作

### 5.1 配置内网大模型（必须做一次）

没有模型配置时，提交扫描任务会报错 `model.token is required when no default model is configured`，所以先配模型：

1. 打开 `http://内网服务器IP:8088`，看页面**底部输入框**区域。
2. 找到输入框上方的**模型选择器**（默认显示"默认模型/不使用模型"字样），点开它，选择**模型管理**（或"新增模型"）。
3. 在弹窗里点**新增模型**，按下表填写：

| 字段 | 怎么填 | 示例 |
|---|---|---|
| 模型名称 | 填 LLM 服务上真实存在的模型名 | `qwen2.5-72b-instruct` |
| API Key | 服务端要求什么就填什么；无鉴权就随便填 | `EMPTY` |
| Base URL | LLM 服务的 OpenAI 兼容地址，**以 /v1 结尾** | 见下表 |
| 并发配置 | 填 10 即可 | `10` |

**常见内网 LLM 的 Base URL 填法**：

| LLM 服务 | Base URL | 模型名称在哪查 |
|---|---|---|
| vLLM 部署 | `http://LLM机IP:8000/v1` | `curl http://LLM机IP:8000/v1/models` |
| Ollama | `http://LLM机IP:11434/v1` | `ollama list` |
| 公司统一网关 | 问平台组，一般形如 `http://gateway:9000/v1` | 网关文档 |

4. 保存后回到主界面。

> 拿不准模型名？在部署机执行 `curl http://LLM地址/v1/models`，返回 JSON 里 `"id":"xxx"` 的 xxx 就是模型名。

### 5.2 跑第一个扫描任务（建议先拿一台测试机试）

1. 在底部输入框**先输入一个 `@` 字符**——会弹出智能体（任务类型）选择列表。
2. 选择 **AI基础设施扫描**。
3. 在输入框里填扫描目标：**裸 IP**（如 `192.168.1.10`）。多个目标换行分隔，或上传 TXT（一行一个）。
   - 提示：目标写**裸 IP** 才会触发全端口扫描；写 `http://IP:PORT` 这种带协议的会跳过端口扫描直接扫该地址。
4. 回车发送。界面上会出现执行计划，依次是三个阶段：
   - **准备扫描环境**：里面能看到一个 `nmap` 工具卡片，命令显示 **`-T4 -p 1-65535`**（这就是全端口扫描在工作），每发现一个开放端口会实时列出"发现端口: IP:端口"；
   - **执行深度扫描**：对发现的每个 `IP:端口` 做指纹识别和漏洞匹配；
   - **智能分析与报告生成**：AI 汇总生成报告。
5. 全部完成后可查看/导出报告。

**第一次跑的时长预期**：单台 IP 全端口扫描约 1-3 分钟（目标防火墙全丢包时更久），整个任务 5-15 分钟属正常。**不是卡死**，看 nmap 卡片状态是"运行中"就是在干活。

---

## 6. 日常运维速查

所有命令都在部署机的 `/opt/aig-app/` 目录下执行：

```bash
cd /opt/aig-app

# 启动 / 停止 / 重启整套服务
docker compose -f docker-compose.intranet.yml up -d
docker compose -f docker-compose.intranet.yml down
docker compose -f docker-compose.intranet.yml restart

# 只重启某一个容器
docker compose -f docker-compose.intranet.yml restart webserver
docker compose -f docker-compose.intranet.yml restart agent

# 看状态 / 看日志（Ctrl+C 退出日志，不影响运行）
docker compose -f docker-compose.intranet.yml ps
docker logs --tail 100 ai-infra-guard-webserver
docker logs --tail 100 ai-infra-guard-agent
docker logs -f ai-infra-guard-agent

# 更新指纹/漏洞库：用新包里的 data 解压覆盖本目录 data/ 后重启 webserver
tar -xzf data-v1.0.1.tar.gz && docker compose -f docker-compose.intranet.yml restart webserver

# 备份（任务记录、模型配置、上传文件都在这三个目录里）
tar -czf aig-backup-$(date +%F).tar.gz db uploads
```

> 数据都存在部署目录里（`db/`、`uploads/`、`logs/` 是启动后自动生成的），删除容器不会丢数据；`down` 也不会；只有手动删目录才会丢。

---

## 7. 故障排查

> 按症状对号入座。所有 `docker` 命令在部署机执行。

### 症状 1：`docker: command not found`
没装 Docker，回第 3 节。

### 症状 2：启动时报 `port is already allocated` / `address already in use`
8088 端口被别的程序占了。查占用：`ss -ltnp | grep 8088`。
处理：要么停掉占端口的程序，要么把 `docker-compose.intranet.yml` 里 ports 改成 `"9088:8088"`（用 9088 访问）。

### 症状 3：容器一直 `Restarting` 或 `(unhealthy)`
```bash
docker logs --tail 50 ai-infra-guard-webserver   # 看哪个容器、报什么错
```
- 日志有 `data` / `fingerprints` / `no such file` 相关报错 → `data/` 目录没和 compose 放同一层，回第 4 节第 3 步。
- 日志有 `permission denied` → 用 root 执行，或 `sudo chown -R 755 /opt/aig-app`。

### 症状 4：浏览器打不开页面
1. 部署机本机 `curl -I http://localhost:8088/`：
   - 200 → 服务正常，是外部访问被拦：执行第 4 节第 6 步的防火墙命令；云环境还要检查安全组；
   - 不通 → 回症状 3 查容器。

### 症状 5：提交扫描任务报 `model.token is required...`
没配模型。回 5.1 节配置。

### 症状 6：扫描卡在报告生成 / AI 分析报错
模型连不通。在部署机验证：
```bash
docker exec ai-infra-guard-agent curl -s --max-time 5 http://LLM地址/v1/models
```
不通就是部署机到 LLM 的网络问题，或 Base URL/模型名填错。

### 症状 7：任务长时间停在"端口扫描"（超过 10 分钟一个端口都没报）
大概率是网络不通而不是程序问题。验证部署机到目标机：
```bash
timeout 3 bash -c "echo > /dev/tcp/目标IP/80" && echo OK || echo "不通"
```
不通 → 找网络组放通部署机 → 目标网段的**出方向任意 TCP 端口**（全端口扫描的特性，普通只放行 80/443 的策略不够）。
另外：目标防火墙如果对扫到的端口全部"静默丢包"（不拒绝只丢弃），nmap 要等超时，本来就慢，耐心等或缩小目标范围。

### 症状 8：能发现漏洞但"API 验证"环节报目标不允许
内网编排已默认放开限制（`AIG_API_CHECKER_ALLOW_PRIVATE_TARGETS=1`、`AIG_API_CHECKER_ALLOW_HTTP=1`）。若你自己改过 compose 把它们改回了 0，会触发此问题，改回 1 并 `down` 后重新 `up -d`。

### 症状 9：启动时报 `Error response from daemon: Get "https://registry-1.docker.io/v2/"` 连接超时
compose 在试图从 Docker Hub（外网）拉取镜像——说明镜像**没有成功导入本地**，或导入的标签与 compose 引用的不一致。
1. 检查镜像是否在本地：`docker images | grep ai-infra-guard`
   - 没有输出 → 先执行第 4 节第 2 步的 `docker load`；
   - 有输出但只有 `v1.0.0` 标签 → 确认 compose 文件是本发布包配套的版本（引用 `v1.0.0` 标签且带 `pull_policy: never`），不要用引用 `:latest` 的旧版编排文件。
2. 重新执行 `up -d`。

### 症状 10：磁盘满了
```bash
docker system df            # 看 Docker 占用
docker builder prune -af    # 清构建缓存（不影响已运行容器）
docker image prune -f       # 清悬空镜像
```
日志在部署目录 `logs/` 下，可定期归档清理。

---

## 8. 卸载与回滚

```bash
cd /opt/aig-app
docker compose -f docker-compose.intranet.yml down   # 停止并移除容器（数据保留）
docker rmi ai-infra-guard-webserver:v1.0.0 ai-infra-guard-agent:v1.0.0   # 不想留镜像就删
# 完全清除：rm -rf /opt/aig-app
```

**升级到新版本**：`down` → 导入新镜像 tar → 替换 compose 和 data → `up -d`。`db/`、`uploads/` 保留即数据不丢。

---

## 9. 本版本验证记录（2026-09-15，构建机实测）

- 镜像构建：webserver `v1.0.0`（402MB）、agent `v1.0.0`（2.85GB），amd64 与 arm64 双架构均完成构建与验证；**发布包为 amd64（适配 x86_64 服务器）**。
- 按本档 compose 启动：两容器 healthy，agent 注册成功（`register_ack: 注册成功`）——amd64 与 arm64 均实测通过（arm64 在模拟环境验证）。
- 全端口扫描链路实测：API 提交 `ai_infra_scan` 任务（目标 `127.0.0.1`）：
  - 端口扫描命令显示 `nmap -T4 -p 1-65535`；
  - 发现开放端口 `127.0.0.1:8000`；
  - 自动完成指纹识别（AI-Agent-Config）、漏洞检出（1 个高危）与报告生成。
- nmap 文件能力：`getcap` 确认 `cap_net_admin,cap_net_raw=eip`，非 root 用户可用 SYN 扫描（两个架构均确认）。
