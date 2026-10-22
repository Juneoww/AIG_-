#!/usr/bin/env python3
"""功能：在独占嵌套 Docker 引擎中验收 Web UI 补丁真实事务。
实现：随机 privileged、network=none 容器运行无扫描服务；只载入 seed 镜像，
  内层构建专用目标镜像并执行真实补丁入口；不挂载宿主 Docker socket。
输入：--evidence 新证据目录、--adapter-root 可选只读规则工具目录。
输出：日志、JSON 和阶段记录；退出时仅清理本次随机容器及匿名卷。
依赖：Python 标准库、Docker、本地 harness/seed 测试镜像，不联网安装。
用法：python scripts/platform/docker_web_ui_fixture_test.py --evidence upgrade-validation/ui1-docker-fixture
"""

import argparse
import json
from pathlib import Path
import subprocess
import tempfile
import time
import uuid


def run(args, timeout=600):
    result = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=timeout)
    output = result.stdout.decode("utf-8", errors="replace")
    if result.returncode:
        raise RuntimeError(" ".join(args[:5]) + "\n" + output)
    return output


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--evidence", required=True)
    parser.add_argument("--adapter-root")
    args = parser.parse_args()
    evidence = Path(args.evidence).resolve()
    if evidence.exists():
        raise ValueError("验收目录必须为新目录，避免覆盖历史结果")
    evidence.mkdir(parents=True)
    name = "aig-web-ui-fixture-" + uuid.uuid4().hex[:12]
    tools = Path(__file__).resolve().parent
    started = False
    try:
        command = ["docker", "run", "-d", "--privileged", "--network=none", "--name", name,
                   "--mount", "type=bind,src=" + str(tools) + ",dst=/tools,readonly"]
        if args.adapter_root:
            command += ["--mount", "type=bind,src=" + str(Path(args.adapter_root).resolve()) + ",dst=/adapters,readonly"]
        run(command + ["aig-platform-upgrade-harness:v111-test"])
        started = True
        container = json.loads(run(["docker", "inspect", name]))[0]
        isolation = {"container": name, "network_mode": container["HostConfig"]["NetworkMode"],
                     "privileged": container["HostConfig"]["Privileged"],
                     "mounts": [{key: item.get(key) for key in ("Type", "Source", "Destination", "RW")}
                                for item in container["Mounts"]]}
        assert isolation["network_mode"] == "none"
        assert not any(item["Destination"] == "/var/run/docker.sock" for item in isolation["mounts"])
        (evidence / "isolation.json").write_text(json.dumps(isolation, indent=2), encoding="utf-8")
        for _ in range(60):
            state = subprocess.run(["docker", "exec", name, "docker", "info"],
                                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            if state.returncode == 0:
                break
            time.sleep(1)
        else:
            raise RuntimeError("隔离 Docker 引擎未就绪")
        with tempfile.TemporaryDirectory(prefix="aig-web-ui-fixture-") as temporary:
            archive = Path(temporary) / "seed.tar"
            run(["docker", "save", "-o", str(archive), "aig-platform-upgrade-fixture:seed"])
            run(["docker", "cp", str(archive), name + ":/seed.tar"])
            run(["docker", "exec", name, "docker", "load", "-i", "/seed.tar"])
        try:
            output = run(["docker", "exec", name, "python3", "/tools/fixture/run_web_ui_inside.py"], timeout=1800)
            (evidence / "fixture.log").write_text(output, encoding="utf-8")
            print(output)
        except Exception as error:
            (evidence / "fixture.log").write_text(str(error), encoding="utf-8")
            raise
        finally:
            # 阶段记录也在失败时收集；仅读取本测试容器的 /opt/aig。
            run(["docker", "exec", name, "python3", "-c",
                 "from pathlib import Path;import shutil;d=Path('/evidence');d.mkdir(exist_ok=True);"
                 "[(shutil.copyfile(p,d/('state-'+str(i)+'.json'))) for i,p in enumerate(Path('/opt/aig/backups').glob('**/STATE.json'))]"])
            run(["docker", "cp", name + ":/evidence/.", str(evidence)])
    finally:
        if started:
            (evidence / "engine.log").write_text(run(["docker", "logs", name]), encoding="utf-8")
            run(["docker", "rm", "-f", "-v", name])


if __name__ == "__main__":
    main()
