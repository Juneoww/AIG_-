# v1.1.1 纯平台离线升级

适用现场：Linux amd64、已完成规则解耦的旧 Web/Agent 双容器 v1.1.0，运行目录 `/opt/aig/v1.1.0`，共享规则 `/opt/aig/rule-data`。仅支持已交付镜像的固定 ID；脚本以容器标签、实际挂载和完整 Compose 交叉识别源部署。其他布局会在停服前拒绝。

升级保留 full_tcp 和旧界面，不自动续扫。旧卡死任务必须先在旧平台显式终止；脚本不会为通过预检改写任务状态。断联任务的已有事件保留为历史记录。

## 接收与执行

服务器须已具备 root、Python 3.8+、本机 Docker、Compose v2、GNU cp、iptables/ip6tables 和可用 DOCKER-USER 链。脚本不联网安装依赖、不拉取镜像、不在服务器构建源码。不支持 rootless/userns、远程卷、IPv6 项目网络或未知业务挂载。

事先建立 `/opt/aig/packages/platform/v1.1.1/`，通过可信渠道上传以下配套文件，不改名、不混用：

- `deploy-platform-v1.1.1.sh`
- `AIG-platform-v1.1.1-linux-amd64.tar.gz`

可从任意工作目录执行只读检查；不会停服务、导入镜像、创建版本目录或锁：

```bash
sudo bash /opt/aig/packages/platform/v1.1.1/deploy-platform-v1.1.1.sh --check
```

维护窗口中先显式终止旧卡死扫描任务，结束所有 API Checker 检查，停止人工规则和配置编辑；确认没有 todo/doing 任务，再执行一条升级命令：

```bash
sudo bash /opt/aig/packages/platform/v1.1.1/deploy-platform-v1.1.1.sh
```

维护防火墙阻断外部 Web 访问，保留回环与项目内部探针。确认空闲后停止两个旧容器，复查 SQLite，备份并校验数据库、上传、日志、其他 data、实际配置和独立 API Checker 卷。旧目录、旧镜像、旧容器与旧卷保留。

自动空闲预检检查的是 SQLite 中的扫描任务。旧 API Checker 没有实时活动检查数量接口，脚本不能证明它已空闲；须由操作者先结束 Checker 检查。停止两个容器后相关写入才全部静止，再进行一致性备份。

新版创建 `/opt/aig/v1.1.1` 和独立 Checker 卷，完整继承实际环境及发布端口。三个共享规则 bind 始终引用原路径，规则文件、目录属性和规则部署记录不会修改。旧版本目录中的三个已废弃规则快照不复制到新版。

兼容规则管理工具与本次操作说明安装到 `/opt/aig/v1.1.1/platform-tools/`。工具只适配受信历史规则交付的程序路径/版本检查，既不携带规则快照，也不自动执行规则更新。

新版容器使用 `ai-infra-guard-webserver-v111`、`ai-infra-guard-agent-v111`，项目为 `aig-v111`。当前有效 Compose 固定使用完整三个文件：

```bash
cd /opt/aig/v1.1.1
docker compose -p aig-v111 \
  -f docker-compose.intranet.yml \
  -f docker-compose.upgrade.yml \
  -f docker-compose.rules.yml ps
```

规范化实际配置写入 intranet 文件；另外两个文件保留规则管理工具要求的完整配置接口。JSON 内容也是合法 YAML。实际环境可能含密钥，三个配置文件仅 root 可读，不作为交付资源或公开诊断上传。

必须通过 Web/Agent 容器健康、API Checker `/healthz`、Web 内存中的真实 Agent 连接状态、三个知识库读取结果、历史数据库及业务文件一致性检查，才能恢复访问并写入 `PLATFORM-DEPLOYED.json`。验收不会默认扫描真实目标，也不要求模型配置。

## 失败与回滚

脚本打印 `/opt/aig/backups/platform-v1.1.0-to-v1.1.1-时间-随机值/`。其 `STATE.json` 包含阶段、完整原配置、实际容器/镜像、业务历史摘要、规则摘要、旧/新/备份 Checker 卷及验收结果。备份目录包含现场环境配置，应按生产密钥数据保管。

升级失败时先停止候选容器，再启动未修改的原容器并验收；不会启动空库、不会删除卷。失败目录、候选卷和阶段记录保留；再次升级遇到已有目标会拒绝，须先审核失败现场再安排清理，禁止直接覆盖。

已成功升级的回滚命令：

```bash
sudo bash /opt/aig/packages/platform/v1.1.1/deploy-platform-v1.1.1.sh \
  --rollback --backup /opt/aig/backups/本次打印的备份目录
```

回滚先停止新版并保存完整候选目录与 Checker 卷。只有新版业务数据与升级前精确一致才自动切回旧容器；有新增任务、模型变更、上传、其他 data 或 Checker 业务写入时，完整保存后拒绝可能丢数据的回退，并恢复新版。此时应安排数据合并或另行明确授权恢复旧快照。脚本没有跳过该保护的参数。

共享规则在平台回滚中保持不动。升级后若规则已经变化，自动回滚拒绝；须先独立验证旧程序兼容性，不能借平台回滚恢复旧规则。

断电和 SIGKILL 不能保证自动恢复。保留现场，读取 `STATE.json` 核对源/候选容器的实际 ID、镜像、挂载和运行状态；先停止仍在运行的候选，保证没有同端口双写，再依据记录启动原 Agent/Web。`maintenance-restore.json` 记录本次维护策略的撤销命令，按倒序逐条核对执行，不清空全局防火墙。恢复与验收失败时保持维护入口并安排人工处理。禁止 `down -v`、prune、删除历史目录或原卷。

## 发布方构建

`package_platform.py` 只消费已验证 stage，不负责生成运行镜像。stage 必须包含：

```text
images/离线镜像.tar.gz
tools/rules-compatible/兼容管理工具（不含任何规则资源包）
RELEASE-NOTES.md
OPERATIONS.md
LICENSE
NOTICE
```

打包器自动加入 `tools/platform_upgrade.py`、`manifest.json` 与 `SHA256SUMS.txt`。`OPERATIONS.md` 可以使用本文。镜像层及最终文件系统无三个规则资源目录的验证由运行镜像构建工具负责，记录作为发布验收依据。兼容管理工具应能把可信历史规则交付转换为独立的 v1.1.1 兼容入口，保持内层规则逐字不变；转换不等于执行规则更新。

```bash
python scripts/platform/package_platform.py \
  --stage 已验证stage目录 --output 新交付目录 \
  --source-commit 完整源码提交 \
  --web-reference ai-infra-guard-webserver:v1.1.1 --web-id sha256:实际Web镜像ID \
  --agent-reference ai-infra-guard-agent:v1.1.1 --agent-id sha256:实际Agent镜像ID \
  --rule-tools-verified
```

打包输出两个文件的 SHA256。脚本内固定配套包 SHA256，内嵌执行工具，不依赖操作者解压；摘要用于检测损坏和混包，仍须通过可信渠道取得文件。

单元验证：`python scripts/platform/test_platform_upgrade.py`（Linux，含 cp -a）。真实事务验收使用 `fixture/Dockerfile.harness` 和 `fixture/Dockerfile.runtime` 创建测试镜像，再运行 `docker_fixture_test.py --evidence 新目录 [--adapter-root 已生成兼容交付目录]`。测试使用专属随机容器、独立内层 Docker 引擎及无扫描能力的受控服务，无宿主 Docker socket，不操作现存业务容器。测试 fixture 不进入正式包。

Based on Tencent Zhuque Lab AI-Infra-Guard；继续保留 LICENSE、NOTICE 和上游署名。
