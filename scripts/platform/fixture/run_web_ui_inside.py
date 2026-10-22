#!/usr/bin/env python3
"""功能：仅在专用嵌套 Docker 引擎运行 v1.1.1 Web UI 补丁事务验收。
实现：建立固定三 Compose 的 v111 现场；仅测试目录副本替换源 seed 身份，
  生成真实包与 Bash 入口，运行只读预检、拒绝、失败恢复、升级和保留写入回退。
输入：/tools 只读源码、seed 镜像、/adapters 可选工具；输出：/evidence 报告。
依赖：Python 标准库、Docker/Compose、iptables；所有 HTTP 均在隔离测试网络。
"""

import argparse
import fcntl
import hashlib
import importlib.util
import json
from pathlib import Path
import shutil
import sqlite3
import subprocess
import sys
import time
from unittest.mock import patch

BASE = Path("/opt/aig")
ROOT = BASE / "v1.1.1"
EVIDENCE = Path("/evidence")
FIXTURE = Path("/fixture-source")
SEED = "aig-platform-upgrade-fixture:seed"
REPORT = {}


def command(args, timeout=300):
    result = subprocess.run([str(x) for x in args], stdout=subprocess.PIPE,
                            stderr=subprocess.STDOUT, timeout=timeout)
    body = result.stdout.decode("utf-8", errors="replace")
    if result.returncode:
        raise RuntimeError(" ".join(map(str, args[:6])) + "\n" + body)
    return body


def record(key, value=True):
    REPORT[key] = value
    p.save(EVIDENCE / "report.json", REPORT)
    print("PASS " + key, flush=True)


def load_fixture_tools():
    source_id = json.loads(command(["docker", "image", "inspect", SEED]))[0]["Id"]
    destination = FIXTURE / "scripts/platform"
    destination.mkdir(parents=True)
    original_bytes = Path("/tools/web_ui_patch.py").read_bytes()
    original = original_bytes.decode("utf-8")
    helper = original
    for field in ("SOURCE_WEB", "SOURCE_AGENT"):
        lines = [line for line in helper.splitlines() if line.startswith(field + " = ")]
        assert len(lines) == 1
        helper = helper.replace(lines[0], field + " = " + repr(source_id))
    (destination / "web_ui_patch.py").write_text(helper, encoding="utf-8")
    for name in ("platform_upgrade.py", "package_web_ui_patch.py"):
        shutil.copy2(Path("/tools") / name, destination / name)
    for name in ("LICENSE", "NOTICE", "OPERATIONS.md", "RELEASE-NOTES.md"):
        (FIXTURE / name).write_text("Isolated transaction fixture only. Tencent Zhuque Lab attribution preserved.\n")
    sys.path.insert(0, str(destination))
    import web_ui_patch
    import package_web_ui_patch
    assert web_ui_patch.SOURCE_WEB == source_id == web_ui_patch.SOURCE_AGENT
    return web_ui_patch, package_web_ui_patch, hashlib.sha256(original_bytes).hexdigest()


def setup_site():
    for name in ("data/mcp", "db", "uploads", "logs"):
        (ROOT / name).mkdir(parents=True, exist_ok=True)
    for scope in p.SCOPES:
        directory = BASE / "rule-data" / scope
        directory.mkdir(parents=True)
        (directory / "fixture.yaml").write_text("info:\n  name: harmless-fixture\n")
    version = BASE / "rule-backups/fixture-v004"
    version.mkdir(parents=True)
    (version / "DEPLOYED.txt").write_text("version=v004-fixture\n")
    (ROOT / "data/mcp/custom.json").write_text('{"custom":"must-survive"}')
    (ROOT / "uploads/history.txt").write_text("existing-upload")
    (ROOT / "logs/history.log").write_text("existing-log")
    (ROOT / ".env").write_text("MODEL_KEY=fixture-preserved\n")
    converter = ROOT / w.TOOL_TARGET
    converter.parent.mkdir(parents=True)
    converter.write_text("# fixture converter before patch\n")
    with sqlite3.connect(ROOT / "db/tasks.db") as db:
        db.executescript("create table sessions(id text primary key,status text,content text);"
                         "insert into sessions values('historic','done','keep');"
                         "create table models(id text,key text);insert into models values('custom','preserved');")
    program = {"services": {}, "networks": {"internal": {"name": "aig-web-ui-fixture-net", "driver": "bridge"}}}
    upgrade = {"services": {"webserver": {"volumes": []}, "agent": {"volumes": [
        {"type": "volume", "source": "platform-checker", "target": "/api-checker-data"}]}},
        "volumes": {"platform-checker": {"name": "aig-web-ui-fixture-checker"}}}
    rules = {"services": {"webserver": {"volumes": []}}}
    for service, role in (("webserver", "web"), ("agent", "agent")):
        image = "ai-infra-guard-" + service + ":v1.1.1"
        command(["docker", "tag", SEED, image])
        item = {"image": image, "pull_policy": "never", "container_name": "ai-infra-guard-" + service + "-v111",
                "environment": {"ROLE": role, "SITE_VALUE": "retained-env"}, "networks": {"internal": None},
                "healthcheck": {"test": ["CMD", "/usr/local/bin/python3", "-c",
                    "import urllib.request;urllib.request.urlopen('http://127.0.0.1:%d/healthz',timeout=2).read()" % (8088 if role == "web" else 8000)],
                    "interval": "1s", "timeout": "3s", "retries": 20}}
        if role == "web":
            item["ports"] = ["127.0.0.1:18888:8088"]
        program["services"][service] = item
    upgrade["services"]["webserver"]["volumes"] = [
        {"type": "bind", "source": str(ROOT / name), "target": "/app/" + name, "bind": {"create_host_path": False}}
        for name in ("data", "db", "uploads", "logs")]
    rules["services"]["webserver"]["volumes"] = [
        {"type": "bind", "source": str(BASE / "rule-data" / scope), "target": "/app/data/" + scope,
         "bind": {"create_host_path": False}} for scope in p.SCOPES]
    files = [ROOT / name for name in p.COMPOSE_NAMES]
    for file, document in zip(files, (program, upgrade, rules)):
        p.save(file, document)
    command(p.compose_command(w.PROJECT, files) + ["up", "-d", "--pull", "never"])
    for _ in range(60):
        if all(p.inspect(name)["State"].get("Health", {}).get("Status") == "healthy" for name in (w.WEB_NAME, w.AGENT_NAME)):
            break
        time.sleep(1)
    else:
        raise RuntimeError("fixture source containers not healthy")
    marker = {"version": "v1.1.1", "candidate_compose": [str(x) for x in files],
              "new_web": p.inspect(w.WEB_NAME), "new_agent": p.inspect(w.AGENT_NAME)}
    p.save(ROOT / "PLATFORM-DEPLOYED.json", marker)
    assert not (BASE / "v1.1.0").exists()
    w.preflight()
    p.save(EVIDENCE / "initial-web.json", p.inspect(w.WEB_NAME))
    p.save(EVIDENCE / "initial-agent.json", p.inspect(w.AGENT_NAME))
    p.save(EVIDENCE / "initial-compose.json", json.loads(command(p.compose_command(w.PROJECT, files) + ["config", "--format", "json"])))
    return w.discover()


def build_delivery():
    build = Path("/fixture-image")
    build.mkdir()
    shutil.copy2("/tools/fixture/web_ui_service.py", build / "fake_service.py")
    (build / "Dockerfile").write_text("FROM " + SEED + "\nCOPY fake_service.py /app/fake_service.py\n")
    command(["docker", "build", "--network=none", "--pull=false", "-t", w.TARGET_TAG, build])
    target = p.docker_json("image", "inspect", w.TARGET_TAG)[0]["Id"]
    archive = Path("/fixture-web.tar")
    command(["docker", "save", "-o", archive, w.TARGET_TAG])
    args = pack.parser().parse_args(["--web-image", str(archive), "--web-id", target,
        "--source-commit", "a" * 40, "--image-source-commit", "b" * 40,
        "--operations", str(FIXTURE / "OPERATIONS.md"), "--release-notes", str(FIXTURE / "RELEASE-NOTES.md"),
        "--rules-converter", "/tools/prepare_rules_v111.py", "--output-dir", "/delivery"])
    bundle, entry = pack.build(args)
    command(["docker", "image", "rm", w.TARGET_TAG])
    return bundle, entry, target


def invoke(entry, args=(), success=True, label="entry"):
    result = subprocess.run(["bash", str(entry), *map(str, args)], stdout=subprocess.PIPE,
                            stderr=subprocess.STDOUT, timeout=600)
    body = result.stdout.decode("utf-8", errors="replace")
    (EVIDENCE / (label + ".log")).write_text(body)
    print(body, flush=True)
    assert success == (result.returncode == 0), "unexpected entry exit: " + str(result.returncode)
    return body


def business():
    return {name: p.business_data_inventory(ROOT / name) if name == "data" else p.inventory(ROOT / name)
            for name in ("data", "db", "uploads", "logs")}


def agent_snapshot():
    agent = p.inspect(w.AGENT_NAME)
    return {"identity": w.agent_identity(agent), "started_at": agent["State"]["StartedAt"],
            "restarts": agent["RestartCount"], "config": agent["Config"], "host_config": agent["HostConfig"]}


def assert_preserved(snapshot, volume, rules, versions, expected_business=None):
    assert agent_snapshot() == snapshot, "Agent identity/start/config changed"
    name = p.mounts(p.inspect(w.AGENT_NAME))["/api-checker-data"]["Name"]
    assert p.volume_hash(w.SOURCE_AGENT, name) == volume, "Checker volume bytes changed"
    assert p.current_rules() == rules, "shared rules changed"
    assert p.rule_version_records() == versions, "independent rule version records changed"
    if expected_business is not None:
        assert business() == expected_business, "business inventory changed"


def latest_backup(phase):
    return next(path.parent for path in sorted((BASE / "backups").glob("*/STATE.json"), reverse=True)
                if json.loads(path.read_text()).get("phase") == phase)


def main():
    global w, pack, p
    EVIDENCE.mkdir()
    w, pack, original_helper_hash = load_fixture_tools()
    p = w.p
    record("fixture_source_provenance", {"production_helper_sha256": original_helper_hash,
        "production_library_sha256": p.sha256("/tools/platform_upgrade.py"),
        "source_seed_id": w.SOURCE_WEB, "test_only_identity_substitution": True})
    layout = setup_site()
    record("v111_only_three_compose_layout")
    bundle, entry, target = build_delivery()
    manifest = w.validate_bundle(bundle)
    # 原生产源码仍固定真实源镜像；只有 /fixture-source 的测试副本可以接受 seed。
    spec = importlib.util.spec_from_file_location("production_web_ui_guard", "/tools/web_ui_patch.py")
    production = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(production)
    try:
        production.validate_bundle(bundle)
        raise AssertionError("production guard accepted fixture source IDs")
    except ValueError as error:
        assert "固定源镜像" in str(error)
    assert p.sha256("/tools/web_ui_patch.py") == original_helper_hash
    record("production_fixed_source_ids_unchanged_and_reject_fixture")
    agents = agent_snapshot()
    checker_name = layout["checker_volume"]["Name"]
    checker = p.volume_hash(w.SOURCE_AGENT, checker_name)
    volume_identity = p.docker_json("volume", "inspect", checker_name)
    rules, versions, initial_business = p.current_rules(), p.rule_version_records(), business()
    containers = command(["docker", "ps", "-aq"])
    images = command(["docker", "image", "ls", "-q", "--no-trunc"])
    site_before = p.inventory(BASE)
    invoke(entry, ["--check"], label="readonly-check")
    assert command(["docker", "ps", "-aq"]) == containers
    assert command(["docker", "image", "ls", "-q", "--no-trunc"]) == images
    assert p.inventory(BASE) == site_before
    assert not (BASE / "backups").exists() and not (BASE / ".aig-rules.lock").exists()
    record("check_readonly_no_image_import_lock_backup_or_layout_write")
    original_bundle = bundle.read_bytes()
    with bundle.open("ab") as stream:
        stream.write(b"fixture corruption")
    invoke(entry, ["--check"], success=False, label="corrupt-package")
    bundle.write_bytes(original_bundle)
    record("corrupt_package_rejected_before_transaction")
    rules_file = ROOT / p.COMPOSE_NAMES[2]
    original_rules_file = rules_file.read_bytes()
    p.save(rules_file, {"services": {}})
    invoke(entry, ["--check"], success=False, label="missing-rule-mount")
    rules_file.write_bytes(original_rules_file)
    record("missing_precise_rule_mount_rejected_without_stopping_web")
    with sqlite3.connect(ROOT / "db/tasks.db") as db:
        db.execute("update sessions set status='doing' where id='historic'")
    invoke(entry, ["--check"], success=False, label="busy-task")
    with sqlite3.connect(ROOT / "db/tasks.db") as db:
        assert db.execute("select status from sessions where id='historic'").fetchone()[0] == "doing"
        db.execute("update sessions set status='done' where id='historic'")
    # SQLite 执行过真实写入，重新保存期望文件摘要，但行内容保持原样。
    initial_business = business()
    record("busy_task_rejected_without_changing_status")
    lock = (BASE / ".aig-rules.lock").open("w")
    fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    try:
        invoke(entry, success=False, label="shared-rule-lock")
    finally:
        lock.close()
    assert not (BASE / "backups").exists()
    assert_preserved(agents, checker, rules, versions, initial_business)
    record("rule_update_shared_lock_prevents_ui_upgrade")
    original_run = p.run
    stop_failed_once = False
    def stopped_then_command_failed(args, **kwargs):
        nonlocal stop_failed_once
        result = original_run(args, **kwargs)
        if not stop_failed_once and [str(x) for x in args[:2]] == ["docker", "stop"]:
            stop_failed_once = True
            raise RuntimeError("fixture-stop-replied-error-after-actually-stopped")
        return result
    with patch.object(p, "run", side_effect=stopped_then_command_failed):
        try:
            w.main(["--bundle", str(bundle)])
            raise AssertionError("uncertain stop failure was not injected")
        except RuntimeError as error:
            assert "fixture-stop-replied-error" in str(error)
    assert p.inspect(w.WEB_NAME)["State"]["Running"]
    w.discover()
    assert_preserved(agents, checker, rules, versions, initial_business)
    record("stop_command_error_after_actual_stop_restores_source")
    original_up, original_save = w.up_web, p.save
    failed_once = False
    diagnostic_failures = []
    def fail_candidate_once(value):
        nonlocal failed_once
        result = original_up(value)
        if not failed_once:
            failed_once = True
            raise RuntimeError("fixture-injected-after-real-target-start")
        return result
    def disk_full_diagnostic(path, value):
        if isinstance(value, dict) and value.get("phase") == "failed":
            diagnostic_failures.append(value.copy())
            raise OSError("fixture-injected-disk-full-diagnostic")
        return original_save(path, value)
    with patch.object(w, "up_web", side_effect=fail_candidate_once), patch.object(p, "save", side_effect=disk_full_diagnostic):
        try:
            w.main(["--bundle", str(bundle)])
            raise AssertionError("candidate failure was not injected")
        except RuntimeError as error:
            assert "fixture-injected-after-real-target-start" in str(error)
    assert diagnostic_failures, "failure diagnostic write was not injected"
    recovered_states = [json.loads(path.read_text()) for path in (BASE / "backups").glob("*/STATE.json")]
    assert any(state.get("error") == "fixture-injected-after-real-target-start"
               and state.get("recovery") == "source-web-restored-business-preserved"
               for state in recovered_states)
    assert p.inspect(w.WEB_NAME)["Image"] == w.SOURCE_WEB
    w.discover()
    assert_preserved(agents, checker, rules, versions, initial_business)
    record("real_candidate_start_failure_restores_source_web")
    record("disk_full_diagnostic_does_not_block_source_recovery")
    source_config = {name: json.loads((ROOT / name).read_text()) for name in p.COMPOSE_NAMES}
    invoke(entry, label="upgrade")
    patched = w.discover(target)
    backup = latest_backup("complete")
    state = json.loads((backup / "STATE.json").read_text())
    assert state["new_web"]["Id"] == patched["web"]["Id"]
    assert p.inspect(w.WEB_NAME)["Image"] == target
    assert p.docker_json("volume", "inspect", checker_name) == volume_identity
    assert_preserved(agents, checker, rules, versions, initial_business)
    expected_config = source_config
    expected_config[p.COMPOSE_NAMES[0]]["services"]["webserver"]["image"] = w.TARGET_TAG
    assert {name: json.loads((ROOT / name).read_text()) for name in p.COMPOSE_NAMES} == expected_config
    assert (ROOT / w.TOOL_TARGET).read_bytes() == Path("/tools/prepare_rules_v111.py").read_bytes()
    record("real_wrapper_upgrade_only_changes_web_image_and_converter")
    record("agent_checker_ports_env_mounts_network_and_business_preserved")
    assert all((backup / "business" / name).is_dir() for name in ("data", "db", "uploads", "logs"))
    assert p.database_snapshot(backup / "business/db/tasks.db") == p.database_snapshot(ROOT / "db/tasks.db")
    record("cold_business_and_checker_backup_created")
    sys.path.insert(0, "/tools/fixture")
    from run_inside import verify_adapters
    marker = json.loads((ROOT / "PLATFORM-DEPLOYED.json").read_text())
    record("compatible_rule_adapter_discovery", verify_adapters(marker))
    # 补丁运行后新增写入与独立规则更新，主动回退必须全部保留。
    with sqlite3.connect(ROOT / "db/tasks.db") as db:
        db.execute("insert into sessions values('after-ui-upgrade','done','must-survive-rollback')")
    (ROOT / "uploads/new.txt").write_text("new accepted upload")
    (ROOT / "data/mcp/new.json").write_text('{"new":"accepted"}')
    (ROOT / "logs/after.log").write_text("new accepted log")
    (ROOT / ".env").write_text("MODEL_KEY=fixture-updated-after-upgrade\n")
    (BASE / "rule-data/fingerprints/fixture.yaml").write_text("info:\n  name: independently-updated-fixture\n")
    (BASE / "rule-backups/fixture-v004/DEPLOYED.txt").write_text("version=v004-fixture-updated\n")
    current_business, rules, versions = business(), p.current_rules(), p.rule_version_records()
    converter = (ROOT / w.TOOL_TARGET).read_bytes()
    rollback_failed_once = False
    def fail_rollback_source_once(value):
        nonlocal rollback_failed_once
        result = original_up(value)
        if not rollback_failed_once:
            rollback_failed_once = True
            raise RuntimeError("fixture-rollback-source-started-then-failed")
        return result
    previous_target_id = p.inspect(w.WEB_NAME)["Id"]
    with patch.object(w, "up_web", side_effect=fail_rollback_source_once):
        try:
            w.main(["--bundle", str(bundle), "--rollback", "--backup", str(backup)])
            raise AssertionError("rollback failure was not injected")
        except RuntimeError as error:
            assert "fixture-rollback-source-started" in str(error)
    current_target = w.discover(target)["web"]
    assert current_target["Id"] != previous_target_id
    assert json.loads((backup / "STATE.json").read_text())["new_web"]["Id"] == current_target["Id"]
    assert_preserved(agents, checker, rules, versions, current_business)
    record("rollback_failure_restores_patched_web_and_refreshes_retry_receipt")
    invoke(entry, ["--rollback", "--backup", backup], label="rollback-preserves-new-writes")
    w.discover()
    assert_preserved(agents, checker, rules, versions, current_business)
    assert (ROOT / ".env").read_text() == "MODEL_KEY=fixture-updated-after-upgrade\n"
    assert (ROOT / w.TOOL_TARGET).read_bytes() == converter
    record("real_wrapper_rollback_preserves_all_post_patch_business_and_rules")
    # 模拟维护链已经打开后才报错，验证不回切并覆盖刚接受的新写入。
    original_end = p.Gate.end
    def opened_then_failed(gate):
        original_end(gate)
        with sqlite3.connect(ROOT / "db/tasks.db") as db:
            db.execute("insert into sessions values('during-opening','done','must-survive-teardown-error')")
        raise RuntimeError("fixture-injected-after-access-opened")
    with patch.object(p.Gate, "end", opened_then_failed):
        try:
            w.main(["--bundle", str(bundle)])
            raise AssertionError("access teardown failure was not injected")
        except RuntimeError as error:
            assert "fixture-injected-after-access-opened" in str(error)
    assert p.inspect(w.WEB_NAME)["Image"] == target
    latest = latest_backup("failed")
    state = json.loads((latest / "STATE.json").read_text())
    assert state["access_may_be_public"] and state["recovery"] == "current-web-and-business-preserved"
    with sqlite3.connect(ROOT / "db/tasks.db") as db:
        assert db.execute("select content from sessions where id='during-opening'").fetchone()[0] == "must-survive-teardown-error"
    assert_preserved(agents, checker, rules, versions)
    record("access_teardown_failure_keeps_patched_web_and_accepted_writes")
    final_business = business()
    invoke(entry, ["--rollback", "--backup", latest], label="rollback-after-opening-error")
    w.discover()
    assert_preserved(agents, checker, rules, versions, final_business)
    assert p.docker_json("volume", "inspect", checker_name) == volume_identity
    assert p.sha256("/tools/web_ui_patch.py") == original_helper_hash
    record("failed_opening_receipt_supports_write_preserving_rollback")
    record("final_source_web_agent_checker_and_business_verified")
    print(json.dumps(REPORT, ensure_ascii=False, indent=2), flush=True)


if __name__ == "__main__":
    main()
