#!/bin/sh
# 功能：验证纯平台镜像所需的外部规则挂载，再进入原 Web 启动流程。
# 实现：检查 mountinfo 中三个独立挂载点及非空 YAML；缺失时退出，不使用镜像旧规则。
# 输入输出：固定 /app/data 三个目录；输出诊断和 Web 日志。
# 依赖及用法：镜像自带 Python；由 v1.1.1 镜像 CMD 调用。
set -eu
python - <<'PY'
from pathlib import Path
mounts = {line.split()[4] for line in Path('/proc/self/mountinfo').read_text().splitlines()}
for scope in ('fingerprints', 'vuln', 'vuln_en'):
    path = Path('/app/data') / scope
    if str(path) not in mounts or not path.is_dir() or not any(path.rglob('*.yaml')):
        raise SystemExit('外部规则挂载缺失或为空，拒绝启动扫描服务: ' + str(path))
PY
exec /app/start.sh
