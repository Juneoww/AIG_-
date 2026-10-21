#!/usr/bin/env python3
"""功能：在专用嵌套Docker引擎内验证真实升级、拒绝、恢复和回滚。
实现：使用无扫描能力的Web/Checker服务和临时SQLite历史数据；不访问外层Docker。
输入：已加载的fixture seed镜像与/tools；输出：/evidence/验收JSON及退出码。
依赖：专用harness容器内Python、Docker、Compose与iptables。
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import sqlite3
import subprocess
import sys
import time
import tarfile
import importlib.util
from unittest.mock import patch
from collections import namedtuple

sys.path.insert(0, '/tools')
import platform_upgrade as u
import package_platform as pack

BASE = Path('/opt/aig')
OLD = BASE / 'v1.1.0'
EVIDENCE = Path('/evidence')


def run(args, **kwargs):
    return u.run(args, **kwargs)


def setup():
    for name in ('data/mcp', 'db', 'uploads', 'logs'):
        (OLD / name).mkdir(parents=True, exist_ok=True)
    for scope in u.SCOPES:
        path = BASE / 'rule-data' / scope
        path.mkdir(parents=True, exist_ok=True)
        (path / 'fixture.yaml').write_text('info:\n  name: harmless-fixture\n')
    (OLD / 'data/mcp/custom.json').write_text('{"custom":"preserved"}')
    (OLD / 'uploads/history.txt').write_text('existing-upload')
    (OLD / '.env').write_text('MODEL_KEY=fixture-preserved\n')
    db = sqlite3.connect(OLD / 'db/tasks.db')
    db.executescript("create table sessions(id text primary key,status text,content text); insert into sessions values('historic','done','keep'); create table models(id text,key text); insert into models values('custom','preserved');")
    db.commit()
    db.close()
    conf = {'services': {}, 'networks': {'internal': {'name': 'aig-platform-fixture-net', 'driver': 'bridge'}}, 'volumes': {'checker': {}}}
    for service, role in (('webserver', 'web'), ('agent', 'agent')):
        image = 'ai-infra-guard-' + service + ':v1.1.0'
        run(['docker', 'tag', 'aig-platform-upgrade-fixture:seed', image])
        run(['docker', 'tag', 'aig-platform-upgrade-fixture:seed', 'ai-infra-guard-' + service + ':v1.1.1'])
        item = {'image': image, 'pull_policy': 'never', 'container_name': 'ai-infra-guard-' + service,
                'environment': {'ROLE': role}, 'networks': {'internal': None},
                'healthcheck': {'test': ['CMD', '/usr/local/bin/python3', '-c', "import urllib.request;urllib.request.urlopen('http://127.0.0.1:%d/healthz',timeout=2).read()" % (8088 if role == 'web' else 8000)], 'interval': '1s', 'timeout': '3s', 'retries': 20}}
        if role == 'web':
            item['ports'] = ['127.0.0.1:18888:8088']
            item['volumes'] = [{'type': 'bind', 'source': str(OLD / name), 'target': '/app/' + name, 'bind': {'create_host_path': False}} for name in ('data', 'db', 'uploads', 'logs')]
            item['volumes'] += [{'type': 'bind', 'source': str(BASE / 'rule-data' / s), 'target': '/app/data/' + s, 'bind': {'create_host_path': False}} for s in u.SCOPES]
        else:
            item['volumes'] = [{'type': 'volume', 'source': 'checker', 'target': '/api-checker-data'}]
        conf['services'][service] = item
    files = [OLD / n for n in ('docker-compose.intranet.yml', 'docker-compose.upgrade.yml', 'docker-compose.rules.yml')]
    u.save(files[0], conf)
    for file in files[1:]:
        u.save(file, {'services': {}})
    run(u.compose_command('aig-v110', files) + ['up', '-d', '--pull', 'never'], timeout=120)
    for _ in range(60):
        if all(u.inspect('ai-infra-guard-' + s)['State'].get('Health', {}).get('Status') == 'healthy' for s in ('webserver', 'agent')):
            return
        time.sleep(1)
    raise RuntimeError('fixture未健康')


def build():
    stage = Path('/stage')
    (stage / 'tools/rules-compatible').mkdir(parents=True)
    (stage / 'images').mkdir()
    (stage / 'tools/rules-compatible/FIXTURE.txt').write_text('Harmless adapter marker: fixture only; no production rules.')
    for name in ('LICENSE', 'NOTICE', 'RELEASE-NOTES.md', 'OPERATIONS.md'):
        (stage / name).write_text('Isolated test fixture only. Tencent Zhuque Lab attribution preserved.')
    run(['docker', 'save', '-o', str(stage / 'images/runtime.tar.gz'), 'ai-infra-guard-webserver:v1.1.1', 'ai-infra-guard-agent:v1.1.1'], timeout=300)
    image = u.docker_json('image', 'inspect', 'aig-platform-upgrade-fixture:seed')[0]['Id']
    # 仅测试进程内替换已加载模块的来源身份；正式打包CLI没有跳过旧镜像校验的选项。
    pack.SOURCE_IMAGES = {s: image for s in ('webserver', 'agent')}
    args = argparse.Namespace(stage=str(stage), output='/delivery', source_commit='a' * 40,
                              web_reference='ai-infra-guard-webserver:v1.1.1', web_id=image,
                              agent_reference='ai-infra-guard-agent:v1.1.1', agent_id=image,
                              minimum_free_gib=0.01, rule_tools_verified=True)
    return pack.build(args)


def invoke(entry, args=(), success=True):
    result = subprocess.run(['bash', str(entry), *args], stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=600)
    output = result.stdout.decode(errors='replace')
    print(output, flush=True)
    if success != (result.returncode == 0):
        raise RuntimeError('unexpected deploy result: ' + str(result.returncode) + '\n' + output)
    return output


def verify_adapters(marker):
    adapters = Path('/adapters')
    if not adapters.is_dir():
        return {'skipped': 'adapter-root未提供'}
    reports = {}
    for directory in sorted(adapters.iterdir()):
        resource = next(directory.glob('*.tar.gz'))
        work = EVIDENCE / ('adapter-' + directory.name)
        work.mkdir()
        with tarfile.open(resource, 'r:gz') as archive:
            body = archive.extractfile('layout.py').read()
        module_path = work / 'layout.py'
        module_path.write_bytes(body)
        spec = importlib.util.spec_from_file_location('adapted_layout_' + directory.name, module_path)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        u.save(work / 'web-before.json', [u.inspect(marker['new_web']['Id'])])
        u.save(work / 'agent-before.json', [u.inspect(marker['new_agent']['Id'])])
        effective = json.loads(run(u.compose_command('aig-v111', marker['candidate_compose']) + ['config', '--format', 'json']))
        u.save(work / 'compose-before.json', effective)
        layout = module.discover(work, 'update')
        assert layout['working_dir'] == '/opt/aig/v1.1.1'
        reports[directory.name] = {'passed': True, 'layout_sha256': hashlib.sha256(body).hexdigest(),
                                   'project': layout['project'], 'config_files': layout['config_files']}
    return reports


def main():
    EVIDENCE.mkdir()
    setup()
    package, entry = build()
    report = {}
    before_containers = run(['docker', 'ps', '-aq']).split()
    before_images = run(['docker', 'image', 'ls', '-q', '--no-trunc']).split()
    report['check_output'] = invoke(entry, ['--check'])
    assert run(['docker', 'ps', '-aq']).split() == before_containers
    assert run(['docker', 'image', 'ls', '-q', '--no-trunc']).split() == before_images
    assert not u.NEW.exists() and not (BASE / '.aig-rules.lock').exists()
    assert not (BASE / 'backups').exists()
    report['readonly_check'] = True
    manifest = u.validate_bundle(package)
    layout = u.discover(manifest['source_images'])
    Disk = namedtuple('Disk', 'total used free')
    with patch.object(u.shutil, 'disk_usage', return_value=Disk(1024, 1024, 0)):
        try:
            u.check_space(layout, manifest)
            raise AssertionError('space failure was not observed')
        except ValueError as error:
            assert '空间不足' in str(error)
    report['insufficient_space_refused'] = True
    actual_compose = OLD / 'docker-compose.intranet.yml'
    original_compose = actual_compose.read_bytes()
    altered = json.loads(original_compose)
    altered['services']['webserver']['volumes'] = [m for m in altered['services']['webserver']['volumes'] if m['target'] != '/app/data/fingerprints']
    u.save(actual_compose, altered)
    report['missing_mount_output'] = invoke(entry, ['--check'], success=False)
    actual_compose.write_bytes(original_compose)
    assert all(u.inspect('ai-infra-guard-' + s)['State']['Running'] for s in ('webserver', 'agent'))
    report['missing_mount_refused_without_stopping_source'] = True
    data = package.read_bytes()
    with package.open('ab') as stream:
        stream.write(b'corruption')
    report['bad_package_output'] = invoke(entry, ['--check'], success=False)
    package.write_bytes(data)
    report['corrupt_package_refused'] = True
    db = sqlite3.connect(OLD / 'db/tasks.db')
    db.execute("update sessions set status='doing'")
    db.commit()
    report['busy_output'] = invoke(entry, ['--check'], success=False)
    assert db.execute('select status from sessions').fetchone()[0] == 'doing'
    db.execute("update sessions set status='done'")
    db.commit()
    db.close()
    report['busy_refused_preserving_status'] = True
    rules_before = u.current_rules()
    # 候选启动阶段注入明确失败；其余停写、备份和旧服务恢复均运行真实Docker命令。
    original_run = u.run
    original_save = u.save
    failed_diagnostic = {}
    def failing_candidate(args, **kwargs):
        if 'compose' in args and 'aig-v111' in args and 'up' in args:
            raise RuntimeError('fixture-injected-candidate-start-failure')
        return original_run(args, **kwargs)
    def disk_full_diagnostics(path, value):
        if isinstance(value, dict) and value.get('phase') == 'failed':
            failed_diagnostic.update(value)
            raise OSError('fixture-injected-disk-full-during-recovery-record')
        return original_save(path, value)
    with patch.object(u, 'run', side_effect=failing_candidate), patch.object(u, 'save', side_effect=disk_full_diagnostics):
        try:
            u.upgrade(package, manifest)
            raise AssertionError('injected failure was not observed')
        except RuntimeError as error:
            assert 'fixture-injected' in str(error)
    failed_backup = next((BASE / 'backups').iterdir())
    failed_state = json.loads((failed_backup / 'STATE.json').read_text())
    assert failed_diagnostic['recovery'] == 'source-restored'
    assert all(u.inspect(failed_state['layout'][s]['Id'])['State']['Running'] for s in ('web', 'agent'))
    assert u.current_rules() == rules_before
    report['candidate_start_failure_restored_source'] = True
    report['disk_full_diagnostics_did_not_block_recovery'] = True
    # 仅测试目录在隔离引擎中改名保留；正式升级器从不自动清理失败候选。
    u.NEW.rename(BASE / 'failed-candidate-preserved')
    report['upgrade_output'] = invoke(entry)
    report['rules_unchanged'] = u.current_rules() == rules_before
    marker = json.loads((u.NEW / 'PLATFORM-DEPLOYED.json').read_text())
    backup = next(p for p in (BASE / 'backups').iterdir() if json.loads((p / 'STATE.json').read_text()).get('success'))
    assert u.database_snapshot(OLD / 'db/tasks.db') == u.database_snapshot(u.NEW / 'db/tasks.db')
    assert marker['new_checker_volume'] != marker['old_checker_volume']
    assert all(not u.inspect(marker['layout'][s]['Id'])['State']['Running'] for s in ('web', 'agent'))
    assert u.knowledge(marker['new_web']['Id'])
    report['independent_volume_and_history'] = True
    report['compatible_rules_layout'] = verify_adapters(marker)
    db = sqlite3.connect(u.NEW / 'db/tasks.db')
    db.execute("insert into sessions values('new-after-upgrade','done','must-not-be-lost')")
    db.commit()
    db.close()
    report['write_aware_rollback_output'] = invoke(entry, ['--rollback', '--backup', str(backup)], success=False)
    assert u.inspect(marker['new_web']['Id'])['State']['Running']
    db = sqlite3.connect(u.NEW / 'db/tasks.db')
    assert db.execute("select content from sessions where id='new-after-upgrade'").fetchone()[0] == 'must-not-be-lost'
    db.execute("delete from sessions where id='new-after-upgrade'")
    db.commit()
    db.close()
    report['rollback_refused_new_writes_and_restored_candidate'] = True
    original_end = u.Gate.end
    def source_exposed_then_failed(gate):
        original_end(gate)
        db = sqlite3.connect(OLD / 'db/tasks.db')
        db.execute("insert into sessions values('accepted-during-rollback-opening','done','must-survive-rollback-teardown')")
        db.commit()
        db.close()
        raise RuntimeError('fixture-partial-rollback-teardown-failed')
    with patch.object(u.Gate, 'end', source_exposed_then_failed):
        try:
            u.rollback(backup)
            raise AssertionError('rollback teardown failure was not observed')
        except RuntimeError as error:
            assert 'fixture-partial-rollback' in str(error)
    assert u.inspect(marker['layout']['web']['Id'])['State']['Running']
    assert not u.inspect(marker['new_web']['Id'])['State']['Running']
    db = sqlite3.connect(OLD / 'db/tasks.db')
    assert db.execute("select content from sessions where id='accepted-during-rollback-opening'").fetchone()[0] == 'must-survive-rollback-teardown'
    db.execute("delete from sessions where id='accepted-during-rollback-opening'")
    db.commit()
    db.close()
    report['rollback_teardown_failure_preserved_accepted_source_writes'] = True
    # 测试夹具显式回到候选运行态，再验证普通回滚；不调用任何正式工具的跳过保护选项。
    u.stop_ids([marker['layout']['web']['Id'], marker['layout']['agent']['Id']])
    run(['docker', 'start', marker['new_agent']['Id'], marker['new_web']['Id']])
    report['rollback_output'] = invoke(entry, ['--rollback', '--backup', str(backup)])
    assert all(u.inspect(marker['layout'][s]['Id'])['State']['Running'] for s in ('web', 'agent'))
    assert all(not u.inspect(marker['new_' + s]['Id'])['State']['Running'] for s in ('web', 'agent'))
    assert u.current_rules() == rules_before
    report['rollback_preserved_candidate'] = any(backup.glob('candidate-preserved-*'))
    # 重建一个仅限测试的候选场景；上一轮候选和卷已经有完整保留副本。
    for container in (marker['new_web']['Id'], marker['new_agent']['Id']):
        run(['docker', 'rm', container])
    u.NEW.rename(BASE / 'successful-candidate-preserved')
    def exposed_then_failed(gate):
        original_end(gate)
        db = sqlite3.connect(u.NEW / 'db/tasks.db')
        db.execute("insert into sessions values('accepted-during-opening','done','must-survive-teardown-error')")
        db.commit()
        db.close()
        raise RuntimeError('fixture-partial-maintenance-teardown-failed')
    with patch.object(u.Gate, 'end', exposed_then_failed):
        try:
            u.upgrade(package, manifest)
            raise AssertionError('maintenance teardown failure was not observed')
        except RuntimeError as error:
            assert 'fixture-partial-maintenance' in str(error)
    assert u.inspect('ai-infra-guard-webserver-v111')['State']['Running']
    assert not u.inspect(marker['layout']['web']['Id'])['State']['Running']
    db = sqlite3.connect(u.NEW / 'db/tasks.db')
    assert db.execute("select content from sessions where id='accepted-during-opening'").fetchone()[0] == 'must-survive-teardown-error'
    db.close()
    report['maintenance_teardown_failure_preserved_accepted_writes'] = True
    u.save(EVIDENCE / 'report.json', report)
    print(json.dumps(report, ensure_ascii=False, indent=2))


if __name__ == '__main__':
    main()
