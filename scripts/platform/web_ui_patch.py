#!/usr/bin/env python3
"""功能：只替换已安装 v1.1.1 的 Web UI 镜像，提供预检、冷备份和原地回退。
实现：严格核验实际 Compose/容器/挂载，复用原双锁和维护入口；不替换业务数据。
输入：固定摘要入口传入的离线包，--check 或 --rollback --backup。
输出：Web 容器重建、配置和部署身份证、0600阶段记录及独立备份；规则/Agent不变。
依赖：Linux amd64 root、Python3.8+、Docker Compose v2、cp、iptables/ip6tables。
"""
import argparse
import copy
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import signal
import sys
import tarfile
import time
import types
import uuid

# 固定入口在内存中预装同一库；源码运行也避免生成__pycache__破坏只读预检。
if 'platform_upgrade' not in sys.modules:
    library = types.ModuleType('platform_upgrade')
    library.__file__ = str(Path(__file__).with_name('platform_upgrade.py'))
    exec(compile(Path(library.__file__).read_bytes(), library.__file__, 'exec'), library.__dict__)
    sys.modules['platform_upgrade'] = library
import platform_upgrade as p

VERSION = 'v1.1.1-ui1'
TARGET_TAG = 'ai-infra-guard-webserver:' + VERSION
SOURCE_WEB = 'sha256:adeef221d46780cae1fab812a6c65ad8e41ce873e2b256570a0cb341052efac6'
SOURCE_AGENT = 'sha256:4243bdc214fba86533fa2036b2de851d9697c0bcd5aeb73660341b2e25e4db4f'
BASE = Path('/opt/aig')
ROOT = BASE / 'v1.1.1'
PROJECT = 'aig-v111'
WEB_NAME = 'ai-infra-guard-webserver-v111'
AGENT_NAME = 'ai-infra-guard-agent-v111'
CONVERTER = 'tools/rules-compatible/prepare_rules_v111.py'
TOOL_TARGET = Path('platform-tools/rules-compatible/prepare_rules_v111.py')
REQUIRED = {'payload/images/web.tar', 'tools/web_ui_patch.py', 'tools/platform_upgrade.py',
            'LICENSE', 'NOTICE', 'OPERATIONS.md', 'RELEASE-NOTES.md'}
require = p.require


def validate_bundle(path, extract=None):
    """归档全量读取校验，预检不展开；仅允许已定义的Web和管理工具。"""
    path = Path(path)
    require(path.is_file() and not path.is_symlink(), '配套包缺失或为链接')
    with tarfile.open(path, 'r:*') as archive:
        members = {}
        for item in archive:
            require(item.isfile() and item.name not in members and item.name in REQUIRED | {CONVERTER, 'manifest.json', 'SHA256SUMS.txt'},
                    '包包含未知/重复/链接路径: ' + item.name)
            members[item.name] = item
        require(REQUIRED | {'manifest.json', 'SHA256SUMS.txt'} <= set(members), '包缺少必要文件')
        require(members['manifest.json'].size < 1024 * 1024, '清单过大')
        manifest = json.load(archive.extractfile(members['manifest.json']))
        require(manifest['format'] == 'aig-web-ui-patch-v1' and manifest['version'] == VERSION, '补丁类型/版本不符')
        require(manifest['source_web'] == SOURCE_WEB and manifest['source_agent'] == SOURCE_AGENT, '固定源镜像不符')
        require(manifest['architecture'] == 'linux/amd64' and manifest['contains_infrastructure_rules'] is False
                and manifest['migration'] == {'required': False}, '架构/规则/迁移声明不符')
        for field in ('source_commit', 'image_source_commit'):
            require(re.fullmatch('[0-9a-f]{40}', manifest[field]), '必须记录完整源码提交: ' + field)
        image = manifest['web_image']
        require(image['tag'] == TARGET_TAG and image['archive'] == 'payload/images/web.tar'
                and re.fullmatch('sha256:[0-9a-f]{64}', image['id']) and image['id'] != SOURCE_WEB, '目标Web镜像身份不符')
        entries = manifest['files']
        names = [e['path'] for e in entries]
        require(len(names) == len(set(names)) and set(names) == set(members) - {'manifest.json', 'SHA256SUMS.txt'}, '清单文件集合不符')
        require(manifest.get('tools', {}) == ({'rules_converter': CONVERTER} if CONVERTER in members else {}), '兼容工具声明不符')
        for entry in entries:
            member = members[entry['path']]
            require(member.size == entry['size'], '负载大小不符')
            digest = hashlib.sha256()
            with archive.extractfile(member) as stream:
                for block in iter(lambda: stream.read(4 * 1024 * 1024), b''):
                    digest.update(block)
            require(digest.hexdigest() == entry['sha256'], '负载摘要不符: ' + entry['path'])
        sums = ''.join(e['sha256'] + '  ' + e['path'] + '\n' for e in entries).encode()
        require(archive.extractfile(members['SHA256SUMS.txt']).read() == sums, '摘要清单不符')
        if extract is not None:
            destination = Path(extract)
            require(not destination.exists(), '负载展开目录已存在')
            destination.mkdir(mode=0o700)
            for name, member in members.items():
                target = destination / name
                target.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
                with archive.extractfile(member) as source, target.open('xb') as output:
                    shutil.copyfileobj(source, output)
                target.chmod(0o600)
    return manifest


def preflight():
    require(sys.platform.startswith('linux') and os.geteuid() == 0, '需要Linux root')
    require(os.uname().machine in ('x86_64', 'amd64'), '仅支持Linux amd64')
    for command in ('docker', 'cp', 'iptables', 'ip6tables'):
        require(shutil.which(command), '缺少离线依赖: ' + command)
    info = p.docker_json('info', '--format', '{{json .}}')
    require(info['OSType'] == 'linux' and not any(('rootless' in x or 'userns' in x) for x in info.get('SecurityOptions', [])), '不支持rootless/userns Docker')
    p.run(['docker', 'compose', 'version'])
    for path in (Path('/opt'), BASE, ROOT):
        p.real_path(path)
    p.run(['iptables', '-w', '-S', 'DOCKER-USER'])
    p.run(['ip6tables', '-w', '-S', 'INPUT'])


def agent_identity(agent):
    return {'id': agent['Id'], 'image': agent['Image'], 'mounts': p.mounts(agent),
            'networks': agent['NetworkSettings']['Networks']}


def assert_web_runtime(original, actual):
    require(original['HostConfig']['PortBindings'] == actual['HostConfig']['PortBindings'], 'Web实际端口改变')
    for field in ('Env', 'Cmd', 'Entrypoint', 'User', 'WorkingDir'):
        left, right = original['Config'].get(field), actual['Config'].get(field)
        require((sorted(left or []) == sorted(right or [])) if field == 'Env' else left == right, 'Web实际配置改变: ' + field)
    require(p.mounts(original) == p.mounts(actual), 'Web实际挂载改变')
    networks = lambda value: {name: data['NetworkID'] for name, data in value['NetworkSettings']['Networks'].items()}
    require(networks(original) == networks(actual), 'Web实际网络改变')


def configured_mounts(effective, role):
    result = {}
    for item in effective['services'][role].get('volumes', []):
        require(isinstance(item, dict) and item['target'] not in result, 'Compose挂载重复/未规范化')
        source = item.get('source')
        if item['type'] == 'volume':
            source = effective.get('volumes', {}).get(source, {}).get('name') or PROJECT + '_' + source
        result[item['target']] = (item['type'], source, not item.get('read_only', False))
    return result


def verify_startup(service, actual, image):
    """Compose null继承镜像；显式空列表/字符串清空，不能误当成继承。"""
    defaults = image.get('Config') or {}
    for field, actual_field in (('command', 'Cmd'), ('entrypoint', 'Entrypoint'), ('user', 'User'), ('working_dir', 'WorkingDir')):
        expected = service.get(field)
        if expected is None:
            expected = defaults.get(actual_field)
            if field == 'command' and service.get('entrypoint') is not None:
                expected = None
        observed = actual['Config'].get(actual_field)
        if field in ('command', 'entrypoint'):
            require((expected or []) == (observed or []), '实际启动配置漂移: ' + field)
        else:
            require((expected or '') == (observed or ''), '实际启动配置漂移: ' + field)


def discover(expected_web=None):
    """只接受已安装的三份JSON Compose，实际标签、身份和挂载必须相符。"""
    expected_web = expected_web or SOURCE_WEB
    p.real_path(ROOT)
    files = [str(ROOT / name) for name in p.COMPOSE_NAMES]
    documents = {}
    for path in files:
        p.real_path(path, False)
        require(Path(path).stat().st_size < 1024 * 1024, 'Compose过大')
        documents[Path(path).name] = json.loads(Path(path).read_text(encoding='utf-8'))
    for name in p.COMPOSE_NAMES[1:]:
        require(all('image' not in s for s in documents[name].get('services', {}).values()), '叠加Compose禁止覆写image')
    web, agent = p.inspect(WEB_NAME), p.inspect(AGENT_NAME)
    actual_ids = p.run(['docker', 'ps', '-aq', '--filter', 'label=' + p.LABEL + 'project=' + PROJECT]).split()
    require(set(actual_ids) in ({web['Id'], agent['Id']}, {web['Id'][:12], agent['Id'][:12]}), '项目必须精确为Web/Agent两个容器')
    effective = json.loads(p.run(p.compose_command(PROJECT, files) + ['config', '--format', 'json']))
    require(set(effective['services']) == {'webserver', 'agent'}, '有效Compose服务不符')
    for role, actual, image in (('webserver', web, expected_web), ('agent', agent, SOURCE_AGENT)):
        labels = actual['Config'].get('Labels') or {}
        require(labels.get(p.LABEL + 'project') == PROJECT and labels.get(p.LABEL + 'service') == role
                and labels.get(p.LABEL + 'project.working_dir') == str(ROOT)
                and labels.get(p.LABEL + 'project.config_files', '').split(',') == files, '容器Compose标签与固定三文件不符')
        require(actual['Name'] == '/' + (WEB_NAME if role == 'webserver' else AGENT_NAME)
                and actual['State']['Running'] and actual['Image'] == image, '容器名称/状态/固定镜像不符: ' + role)
        service = effective['services'][role]
        require(service['image'] == actual['Config']['Image'], '有效Compose镜像引用漂移')
        image_info = p.docker_json('image', 'inspect', service['image'])[0]
        require(image_info['Id'] == image, '镜像标签已漂移')
        require(image_info['Os'] == 'linux' and image_info['Architecture'] == 'amd64', '镜像实际架构不符')
        require(not any(k in service for k in ('build', 'network_mode', 'devices', 'configs', 'secrets')), '未知Compose部署布局')
        env = dict(item.split('=', 1) for item in actual['Config'].get('Env', []) if '=' in item)
        require(all(str(env.get(k, '')) == str(v if v is not None else '') for k, v in service.get('environment', {}).items()), '环境配置漂移')
        verify_startup(service, actual, image_info)
        expected_networks = {effective.get('networks', {}).get(name, {}).get('name') or PROJECT + '_' + name for name in service.get('networks', {'default': {}})}
        require(expected_networks == set(actual['NetworkSettings']['Networks']), '实际网络与Compose不同')
        mounts = {t: (m['Type'], m.get('Name') if m['Type'] == 'volume' else m['Source'], m['RW']) for t, m in p.mounts(actual).items()}
        require(mounts == configured_mounts(effective, role), '实际挂载与Compose不一致')
        configured_ports = {(str(x.get('target')) + '/' + x.get('protocol', 'tcp'), str(x.get('published')), x.get('host_ip') or '0.0.0.0') for x in service.get('ports', [])}
        actual_ports = {(target, x['HostPort'], x.get('HostIp') or '0.0.0.0') for target, values in (actual['HostConfig'].get('PortBindings') or {}).items() for x in values}
        require(configured_ports == actual_ports, '实际端口与Compose不一致')
    wm, am = p.mounts(web), p.mounts(agent)
    require(set(wm) == {'/app/' + x for x in ('data', 'db', 'logs', 'uploads')} | {'/app/data/' + s for s in p.SCOPES}, 'Web挂载目标不符')
    for name in ('data', 'db', 'logs', 'uploads'):
        m = wm['/app/' + name]
        require(m['Type'] == 'bind' and m['Source'] == str(ROOT / name) and m['RW'], '业务bind路径不符')
        p.real_path(ROOT / name)
    for scope in p.SCOPES:
        m = wm['/app/data/' + scope]
        require(m['Type'] == 'bind' and m['Source'] == str(p.RULES / scope), '共享规则精确挂载不符')
        p.real_path(p.RULES / scope)
        require(any((p.RULES / scope).iterdir()), '规则目录不能为空')
    require(set(am) == {'/api-checker-data'} and am['/api-checker-data']['Type'] == 'volume', 'Checker卷布局不符')
    volume = p.docker_json('volume', 'inspect', am['/api-checker-data']['Name'])[0]
    require(volume['Driver'] == 'local' and not volume.get('Options'), 'Checker必须为本地卷')
    networks = set(web['NetworkSettings']['Networks'])
    require(len(networks) == 1 and networks == set(agent['NetworkSettings']['Networks']), '必须唯一共享网络')
    network = p.docker_json('network', 'inspect', next(iter(networks)))[0]
    require(network['Driver'] == 'bridge' and not network.get('EnableIPv6') and set(network.get('Containers', {})) == {web['Id'], agent['Id']}, '网络布局不支持维护入口')
    require(set(web['HostConfig'].get('PortBindings') or {}) == {'8088/tcp'} and not agent['HostConfig'].get('PortBindings'), 'Web/Agent发布端口布局不符')
    marker_path = ROOT / 'PLATFORM-DEPLOYED.json'
    p.real_path(marker_path, False)
    marker = json.loads(marker_path.read_text(encoding='utf-8'))
    require(marker.get('new_web', {}).get('Id') == web['Id'] and marker.get('new_web', {}).get('Image') == expected_web
            and marker.get('new_agent', {}).get('Id') == agent['Id'] and marker.get('new_agent', {}).get('Image') == SOURCE_AGENT, 'PLATFORM-DEPLOYED身份证与实际容器不符')
    return {'web': web, 'agent': agent, 'files': files, 'documents': documents, 'compose': effective,
            'network': network, 'checker_volume': volume, 'marker': marker,
            'config_sha256': {f: p.sha256(f) for f in files}, 'agent_identity': agent_identity(agent)}


def schema(snapshot):
    return {name: value['schema'] for name, value in snapshot.items()}


def check_site(layout, manifest):
    snapshot = p.database_snapshot(ROOT / 'db/tasks.db', idle=True)
    p.current_rules()
    p.knowledge(layout['web']['Id'])
    p.fetch_agent_status(layout['web']['Id'])
    required = sum(e['size'] for e in manifest['files']) + sum(x.stat().st_size for d in ('data', 'db', 'logs', 'uploads') for x in (ROOT / d).rglob('*') if x.is_file())
    checker = p.real_path(layout['checker_volume']['Mountpoint'])
    required += sum(x.stat().st_size for x in checker.rglob('*') if x.is_file())
    require(shutil.disk_usage(BASE).free > required * 2 + 512 * 1024 ** 2, '备份/导入空间不足')
    docker_root = p.docker_json('info', '--format', '{{json .}}')['DockerRootDir']
    image_size = next(e['size'] for e in manifest['files'] if e['path'] == manifest['web_image']['archive'])
    require(shutil.disk_usage(docker_root).free > image_size * 3 + 512 * 1024 ** 2, 'Docker镜像存储空间不足')
    return snapshot


def write_web_image(path, image):
    path = Path(path)
    document = json.loads(path.read_text(encoding='utf-8'))
    document['services']['webserver']['image'] = image
    p.save(path, document)


def enter_maintenance(gate):
    try:
        gate.begin()
    except BaseException:
        # begin可在中途失败；此时尚未停止Web，撤销已建立的维护规则。
        gate.end()
        raise


def cold_backup(work, layout):
    config = work / 'configuration'
    config.mkdir(mode=0o700)
    names = [*p.COMPOSE_NAMES, 'PLATFORM-DEPLOYED.json']
    if (ROOT / '.env').exists():
        p.real_path(ROOT / '.env', False)
        names.append('.env')
    for name in names:
        shutil.copy2(ROOT / name, config / name)
        (config / name).chmod(0o600)
    if (ROOT / TOOL_TARGET).exists():
        p.real_path(ROOT / TOOL_TARGET, False)
        shutil.copy2(ROOT / TOOL_TARGET, config / 'rules-converter.py')
        (config / 'rules-converter.py').chmod(0o600)
    business = work / 'business'
    business.mkdir(mode=0o700)
    for name in ('data', 'db', 'logs', 'uploads'):
        destination = business / name
        destination.mkdir(mode=0o700)
        for child in (ROOT / name).iterdir():
            if name != 'data' or child.name not in p.SCOPES:
                p.run(['cp', '-a', '--', child, destination / child.name], timeout=3600)
        expected = p.business_data_inventory(ROOT / name) if name == 'data' else p.inventory(ROOT / name)
        require(p.inventory(destination) == expected, '业务冷备份摘要不符: ' + name)
    checker = work / 'checker'
    checker.mkdir(mode=0o700)
    p.run(['docker', 'run', '--rm', '--pull=never', '--network=none', '--user=0', '--entrypoint=/bin/sh',
           '--mount', 'type=volume,src=' + layout['checker_volume']['Name'] + ',dst=/source,readonly',
           '--mount', 'type=bind,src=' + str(checker) + ',dst=/backup', SOURCE_AGENT,
           '-ec', 'cp -a /source/. /backup/'], timeout=3600)
    require(checker_inventory(checker) == p.volume_hash(SOURCE_AGENT, layout['checker_volume']['Name']), 'Checker备份摘要不符')


def checker_inventory(root):
    """对齐原库卷摘要格式，包含卷根属性；拒绝链接及特殊文件。"""
    import stat
    root = Path(root)
    result = p.inventory(root)
    for value in result.values():
        value.pop('kind', None)
        value.pop('size', None)
    info = root.stat()
    result['.'] = {'mode': stat.S_IMODE(info.st_mode), 'uid': info.st_uid, 'gid': info.st_gid}
    return result


def restore_configuration(work):
    """仅恢复配置；业务目录与Checker卷从不被旧快照覆盖。"""
    for name in (*p.COMPOSE_NAMES, 'PLATFORM-DEPLOYED.json', '.env'):
        source = Path(work) / 'configuration' / name
        if source.exists():
            shutil.copy2(source, ROOT / name)
    converter = Path(work) / 'configuration/rules-converter.py'
    if converter.exists():
        shutil.copy2(converter, ROOT / TOOL_TARGET)


def up_web(layout):
    p.run(p.compose_command(PROJECT, layout['files']) + ['up', '-d', '--no-deps', '--pull', 'never', 'webserver'], timeout=300)
    return p.inspect(WEB_NAME)


def assert_unchanged(layout):
    require(agent_identity(p.inspect(AGENT_NAME)) == layout['agent_identity'], 'Agent容器/镜像/Checker卷/网络身份已改变')
    require(p.docker_json('volume', 'inspect', layout['checker_volume']['Name'])[0] == layout['checker_volume'], 'Checker卷实际身份已改变')


def converter_target(create=False):
    destination = ROOT / TOOL_TARGET
    for parent in (ROOT / 'platform-tools', destination.parent):
        if parent.exists() or parent.is_symlink():
            p.real_path(parent)
        elif create:
            parent.mkdir(mode=0o700)
    if destination.exists() or destination.is_symlink():
        p.real_path(destination, False)
    return destination


def curl(web, endpoint):
    return p.run(['docker', 'exec', web, 'curl', '--noproxy', '*', '--fail', '--silent', '--show-error', '--max-time', '15', 'http://127.0.0.1:8088' + endpoint])


def accept(layout, image, expected_knowledge, expect_ui, timeout=180):
    deadline, error = time.monotonic() + timeout, None
    while time.monotonic() < deadline:
        try:
            web = p.inspect(WEB_NAME)
            require(web['State']['Running'] and web['Image'] == image and web['State'].get('Health', {}).get('Status') == 'healthy', 'Web尚未健康/镜像不符')
            assert_web_runtime(layout['web'], web)
            assert_unchanged(layout)
            agent = p.inspect(AGENT_NAME)
            require(agent['State'].get('Health', {}).get('Status') == 'healthy', 'Agent尚未健康')
            p.run(['docker', 'exec', AGENT_NAME, '/app/api-checker-venv/bin/python', '-c', "import urllib.request; urllib.request.build_opener(urllib.request.ProxyHandler({})).open('http://127.0.0.1:8000/healthz',timeout=3).read()"], timeout=10)
            p.fetch_agent_status(web['Id'])
            require(p.normalize_knowledge(p.knowledge(web['Id'])) == p.normalize_knowledge(expected_knowledge), '知识库内容改变')
            if expect_ui:
                html = curl(web['Id'], '/')
                scripts = re.findall(r'<script\b[^>]*\bsrc=["\']([^"\']+\.js(?:\?[^"\']*)?)["\']', html)
                require(scripts and all(s.startswith('/') and not s.startswith('//') and '..' not in s for s in scripts), 'UI index没有合法本地JS')
                javascript = '\n'.join(curl(web['Id'], s) for s in scripts)
                require(all(token in javascript for token in ('scan-port-mode', 'full_tcp', 'fixed_ai', 'port_mode')), '真实UI JS缺少端口模式选项/参数')
            return web
        except Exception as caught:
            error = caught
            time.sleep(2)
    raise RuntimeError('Web UI补丁验收失败: ' + str(error))


def update_marker(layout, web, state, rolled_back=False):
    marker = copy.deepcopy(layout['marker'])
    marker['new_web'] = web
    marker['web_ui_patch'] = {'version': VERSION, 'backup': state['backup'], 'bundle_sha256': state['bundle_sha256'], 'rolled_back': rolled_back}
    p.save(ROOT / 'PLATFORM-DEPLOYED.json', marker)


def resume_unchanged_web(layout, image, expected_knowledge, expect_ui):
    """stop响应丢失时以实际容器状态为准；确认原身份并恢复健康后才能开入口。"""
    actual = p.inspect(WEB_NAME)
    require(actual['Id'] == layout['web']['Id'] and actual['Image'] == image, '停止失败后Web身份不明；保留维护入口')
    if not actual['State']['Running']:
        p.run(['docker', 'start', actual['Id']], timeout=120)
    return accept(layout, image, expected_knowledge, expect_ui)


def refresh_patch_receipt(backup, state, web, reason):
    """只同步本补丁的当前Web身份证；保留前一ID审计，不修改历史平台备份。"""
    history = state.setdefault('web_identity_history', [])
    history.append({'previous_web_id': state['new_web']['Id'], 'current_web_id': web['Id'],
                    'reason': reason, 'recorded_at': datetime.datetime.now(datetime.timezone.utc).isoformat()})
    state['new_web'] = web
    state['config_after'] = {f: p.sha256(f) for f in state['layout']['files']}
    update_marker(state['layout'], web, state)
    p.save(Path(backup) / 'STATE.json', state)
    p.save(ROOT / 'WEB-UI-PATCH.json', state)


def upgrade(bundle, manifest):
    layout = discover()
    before = check_site(layout, manifest)
    locks = p.lock_all()
    work = BASE / 'backups' / ('web-ui-' + VERSION + '-' + time.strftime('%Y%m%d-%H%M%S') + '-' + uuid.uuid4().hex[:8])
    if work.parent.exists() or work.parent.is_symlink():
        p.real_path(work.parent)
    work.parent.mkdir(exist_ok=True, mode=0o700)
    work.mkdir(mode=0o700)
    state = {'version': VERSION, 'backup': str(work), 'bundle_sha256': p.sha256(bundle), 'manifest': manifest,
             'layout': layout, 'schema': schema(before), 'access_may_be_public': False, 'phase': 'created'}
    p.save(work / 'STATE.json', state)
    gate, changed, maintenance, stop_attempted = p.Gate(layout, work), False, False, False
    try:
        require(discover()['config_sha256'] == layout['config_sha256'], '加锁后配置已变化')
        rules = p.current_rules()
        rule_records = p.rule_version_records()
        knowledge = p.knowledge(layout['web']['Id'])
        state.update(knowledge_before=knowledge, rules_before=rules, rule_records_before=rule_records)
        p.save(work / 'STATE.json', state)
        enter_maintenance(gate); maintenance = True
        p.database_snapshot(ROOT / 'db/tasks.db', idle=True)
        state.update(phase='stopping-web', stop_attempted=True)
        p.save(work / 'STATE.json', state)
        stop_attempted = True
        p.run(['docker', 'stop', layout['web']['Id']], timeout=120)
        snapshot = p.database_snapshot(ROOT / 'db/tasks.db', idle=True)
        require(schema(snapshot) == state['schema'], '维护期间schema改变')
        cold_backup(work, layout)
        state.update(phase='cold-backup-complete', configuration_sha256={n: p.sha256(work / 'configuration' / n) for n in p.COMPOSE_NAMES})
        p.save(work / 'STATE.json', state)
        validate_bundle(bundle, work / 'payload')
        p.run(['docker', 'load', '-i', work / 'payload' / manifest['web_image']['archive']], timeout=3600)
        require(p.docker_json('image', 'inspect', TARGET_TAG)[0]['Id'] == manifest['web_image']['id'], '导入后目标镜像身份不符')
        changed = True
        write_web_image(ROOT / p.COMPOSE_NAMES[0], TARGET_TAG)
        if manifest.get('tools'):
            destination = converter_target(create=True)
            shutil.copy2(work / 'payload' / CONVERTER, destination)
            destination.chmod(0o600)
        up_web(layout)
        web = accept(layout, manifest['web_image']['id'], knowledge, True)
        require(p.database_snapshot(ROOT / 'db/tasks.db', idle=True) == snapshot, 'Web补丁改变业务数据库')
        require(p.current_rules() == rules, '共享规则改变')
        require(p.rule_version_records() == rule_records, '独立规则版本记录改变')
        state.update(phase='validated', new_web=web, config_after={f: p.sha256(f) for f in layout['files']})
        update_marker(layout, web, state)
        p.save(work / 'STATE.json', state)
        p.begin_access_opening(gate, state, web['Id'], work / 'STATE.json')
        maintenance = False
        state.update(phase='complete', completed_at=datetime.datetime.now(datetime.timezone.utc).isoformat())
        p.save(work / 'STATE.json', state)
        p.save(ROOT / 'WEB-UI-PATCH.json', state)
        print('Web UI补丁完成；备份: ' + str(work))
        return work
    except BaseException as error:
        state.update(error=str(error), phase='failed')
        p.best_effort_save(work / 'STATE.json', state)
        if state['access_may_be_public']:
            state['recovery'] = 'current-web-and-business-preserved'
        else:
            try:
                if changed:
                    p.run(['docker', 'stop', WEB_NAME], timeout=120, check=False)
                    restore_configuration(work)
                    up_web(layout)
                    restored = accept(layout, SOURCE_WEB, state.get('knowledge_before', {}), False)
                elif stop_attempted:
                    restored = resume_unchanged_web(layout, SOURCE_WEB, state.get('knowledge_before', {}), False)
                else:
                    restored = layout['web']
                if stop_attempted:
                    update_marker(layout, restored, state, rolled_back=True)
                if maintenance:
                    p.begin_access_opening(gate, state, restored['Id'], work / 'STATE.json')
                    state['phase'] = 'failed'
                state['recovery'] = 'source-web-restored-business-preserved'
            except BaseException as recovery_error:
                state.update(recovery='manual-required-maintenance-preserved', recovery_error=str(recovery_error))
        p.best_effort_save(work / 'STATE.json', state)
        raise
    finally:
        for lock in locks:
            lock.close()


def rollback(backup, manifest, bundle):
    backup = p.real_path(backup)
    require(backup.parent == BASE / 'backups', '备份必须位于本平台备份目录')
    p.real_path(backup / 'STATE.json', False)
    state = json.loads((backup / 'STATE.json').read_text(encoding='utf-8'))
    require(state['version'] == VERSION and state['bundle_sha256'] == p.sha256(bundle) and state.get('new_web')
            and state['manifest'] == manifest and state.get('phase') in ('complete', 'failed'), '补丁备份身份/阶段不符')
    layout = discover(manifest['web_image']['id'])
    require(layout['web']['Id'] == state['new_web']['Id'] and layout['config_sha256'] == state['config_after'], '当前Web/配置与receipt不同')
    require(layout['marker'].get('web_ui_patch', {}).get('backup') == str(backup), '部署标记与补丁备份不符')
    require(layout['agent_identity'] == state['layout']['agent_identity'], 'Agent/Checker卷与升级前不同')
    require(schema(p.database_snapshot(ROOT / 'db/tasks.db', idle=True)) == state['schema'], 'schema已改变，不能原地回退Web')
    for name, digest in state['configuration_sha256'].items():
        require(p.sha256(backup / 'configuration' / name) == digest, '备份配置摘要不符')
    require(p.docker_json('image', 'inspect', state['layout']['web']['Config']['Image'])[0]['Id'] == SOURCE_WEB, '原Web镜像标签已漂移')
    locks = p.lock_all()
    work = backup / ('rollback-' + uuid.uuid4().hex[:8])
    work.mkdir(mode=0o700)
    gate = p.Gate(layout, work)
    recovery = {'backup': str(backup), 'access_may_be_public': False, 'phase': 'rollback-started'}
    maintenance, stop_attempted, changed = False, False, False
    try:
        require(discover(manifest['web_image']['id'])['config_sha256'] == state['config_after'], '加锁后配置漂移')
        enter_maintenance(gate)
        maintenance = True
        rules = p.current_rules()
        rule_records = p.rule_version_records()
        knowledge = p.knowledge(layout['web']['Id'])
        p.database_snapshot(ROOT / 'db/tasks.db', idle=True)
        recovery.update(phase='stopping-web', stop_attempted=True)
        p.save(work / 'STATE.json', recovery)
        stop_attempted = True
        p.run(['docker', 'stop', WEB_NAME], timeout=120)
        snapshot = p.database_snapshot(ROOT / 'db/tasks.db', idle=True)
        require(schema(snapshot) == state['schema'], '维护后schema改变')
        cold_backup(work, layout)
        # 主动回退只换Web image；当前规则、converter和业务写入全部保留。
        changed = True
        write_web_image(ROOT / p.COMPOSE_NAMES[0], state['layout']['web']['Config']['Image'])
        up_web(layout)
        web = accept(layout, SOURCE_WEB, knowledge, False)
        require(p.database_snapshot(ROOT / 'db/tasks.db', idle=True) == snapshot, '回退改变业务数据')
        require(p.current_rules() == rules and p.rule_version_records() == rule_records, '回退期间共享规则/版本记录改变')
        update_marker(state['layout'], web, state, rolled_back=True)
        p.begin_access_opening(gate, recovery, web['Id'], work / 'STATE.json')
        maintenance = False
        recovery['phase'] = 'rollback-complete'
        p.save(work / 'STATE.json', recovery)
        p.save(ROOT / 'WEB-UI-PATCH.json', recovery)
        print('Web已回退，保留全部当前业务数据；备份: ' + str(work))
    except BaseException as error:
        recovery['error'] = str(error)
        p.best_effort_save(work / 'STATE.json', recovery)
        if stop_attempted and not recovery['access_may_be_public']:
            try:
                if changed:
                    p.run(['docker', 'stop', WEB_NAME], timeout=120, check=False)
                    restore_configuration(work)
                    up_web(layout)
                    web = accept(layout, manifest['web_image']['id'], knowledge, True)
                else:
                    web = resume_unchanged_web(layout, manifest['web_image']['id'], knowledge, True)
                refresh_patch_receipt(backup, state, web, 'failed-rollback-recovered-patched-web')
                p.begin_access_opening(gate, recovery, web['Id'], work / 'STATE.json')
                recovery['phase'] = 'rollback-failed-recovered'
                recovery['recovery'] = 'patched-web-restored-business-preserved'
            except BaseException as recovery_error:
                recovery.update(recovery='manual-required-maintenance-preserved', recovery_error=str(recovery_error))
        elif maintenance and not recovery['access_may_be_public']:
            try:
                web = accept(layout, manifest['web_image']['id'], p.knowledge(layout['web']['Id']), True)
                p.begin_access_opening(gate, recovery, web['Id'], work / 'STATE.json')
                recovery['phase'] = 'rollback-failed-recovered'
                recovery['recovery'] = 'patched-web-unchanged'
            except BaseException as recovery_error:
                recovery.update(recovery='manual-required-maintenance-preserved', recovery_error=str(recovery_error))
        p.best_effort_save(work / 'STATE.json', recovery)
        raise
    finally:
        for lock in locks:
            lock.close()


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--bundle', required=True)
    modes = parser.add_mutually_exclusive_group()
    modes.add_argument('--check', action='store_true')
    modes.add_argument('--rollback', action='store_true')
    parser.add_argument('--backup')
    args = parser.parse_args(argv)
    require(bool(args.backup) == args.rollback, '--backup必须与--rollback配套')
    manifest = validate_bundle(args.bundle)
    preflight()
    if args.rollback:
        return rollback(args.backup, manifest, args.bundle)
    if args.check:
        check_site(discover(), manifest)
        print('只读预检通过；没有创建目录/锁或导入镜像')
        return
    return upgrade(args.bundle, manifest)


if __name__ == '__main__':
    try:
        def interrupted(signum, frame):
            raise RuntimeError('收到终止信号，进入有记录的Web恢复流程: ' + str(signum))
        signal.signal(signal.SIGTERM, interrupted)
        main()
    except Exception as error:
        print('Web UI补丁失败: ' + str(error), file=sys.stderr)
        sys.exit(1)
