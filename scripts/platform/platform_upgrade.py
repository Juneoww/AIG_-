#!/usr/bin/env python3
"""功能：离线核验、升级和回滚 v1.1.1 纯平台部署。
实现：只读发现实际双服务 Compose；加互斥锁、建立维护防火墙、停写备份后复制运行。
输入：配套平台包；--check / --rollback --backup；固定 /opt/aig 两服务布局。
输出：独立新版目录、API Checker 卷、备份和阶段记录；共享规则永不写入。
依赖：Linux amd64 root、Python 3.8+、Docker Compose v2、cp、iptables/ip6tables。
用法：由带固定包 SHA256 的 deploy-platform-v1.1.1.sh 调用。
"""
import argparse
import base64
import copy
import datetime
import hashlib
import ipaddress
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import signal
import sqlite3
import stat
import subprocess
import sys
import tarfile
import time
import uuid

VERSION = 'v1.1.1'
BASE = Path('/opt/aig')
OLD = BASE / 'v1.1.0'
NEW = BASE / VERSION
RULES = BASE / 'rule-data'
SCOPES = ('fingerprints', 'vuln', 'vuln_en')
LABEL = 'com.docker.compose.'
SOURCE_IMAGES = {
    'webserver': 'sha256:9cfc382ead21bc32a99d3ae43826695d59286f1cce164b2cfd53668c175d898d',
    'agent': 'sha256:c2e81a8e49d2d1bde7bdff780a0e0681a920128627789f06f46df5fd6a24a918',
}


def require(value, message):
    if not value:
        raise ValueError(message)


def sha256(path):
    digest = hashlib.sha256()
    with Path(path).open('rb') as stream:
        for chunk in iter(lambda: stream.read(4 * 1024 * 1024), b''):
            digest.update(chunk)
    return digest.hexdigest()


def safe_member(name):
    path = PurePosixPath(name)
    require(name and not name.startswith('/') and '\\' not in name and str(path) == name
            and not any(p in ('..', '.') for p in path.parts) and not any(ord(c) < 32 for c in name), '归档路径不安全: ' + name)
    require(not any(p in SCOPES for p in path.parts), '平台包不能携带规则目录: ' + name)
    require(not re.search(r'(^|/)(?:rules|data)-.*\.tar', name), '平台包不能携带规则快照: ' + name)
    require(not name.endswith(('.tar', '.tar.gz', '.tgz', '.zip')) or (name.startswith('images/') and name.count('/') == 1),
            '未知归档负载；不能改名携带规则: ' + name)
    require(not name.endswith(('.db', '.sqlite', '.sqlite3', '.log')) and path.name != '.env', '平台包含现场数据: ' + name)


def validate_bundle(path, extract=None):
    """清单外文件、链接、重复名称和路径别名均拒绝；check 模式不落盘。"""
    with tarfile.open(path, 'r:gz') as archive:
        members = {}
        for member in archive:
            name = member.name.rstrip('/') if member.isdir() else member.name
            safe_member(name)
            require(name not in members, '归档路径重复: ' + name)
            require(member.isfile() or member.isdir(), '归档禁止链接及非普通文件: ' + name)
            require(member.size >= 0 and member.size <= 32 * 1024 ** 3, '归档文件大小不合理')
            members[name] = member
        require('manifest.json' in members and members['manifest.json'].size < 4 * 1024 ** 2, '缺少有效manifest')
        manifest = json.load(archive.extractfile(members['manifest.json']))
        require(manifest.get('type') == 'platform' and manifest.get('version') == VERSION, '平台版本或交付类型不匹配')
        require(manifest.get('source_versions') == ['v1.1.0'] and manifest.get('contract_version') == '1.0', '升级源版本或契约不兼容')
        require(manifest.get('architecture') == 'linux/amd64', '不支持平台架构')
        require(re.fullmatch('[0-9a-f]{40}', manifest.get('source_commit', '')), '需要完整源码提交')
        require(manifest.get('contains_infrastructure_rules') is False, '禁止携带基础设施规则')
        require(manifest.get('migration') == {'required': False}, '此维护版仅支持无迁移部署')
        require(manifest.get('rules_compatibility', {}).get('validated') is True, '规则引擎兼容性未经验证')
        tools = manifest.get('rule_tools_compatibility', {})
        require(tools.get('validated') is True and tools.get('version') == VERSION and tools.get('layout') == 'legacy-two-service', '规则工具必须验证v1.1.1双服务布局')
        require(isinstance(manifest.get('minimum_free_bytes'), int) and manifest['minimum_free_bytes'] > 0, '需要磁盘空间估算')
        for service in ('webserver', 'agent'):
            require(re.fullmatch(r'sha256:[a-f0-9]{64}', manifest.get('source_images', {}).get(service, '')), '缺少旧版固定镜像身份')
            image = manifest.get('images', {}).get(service, {})
            require(re.fullmatch(r'sha256:[a-f0-9]{64}', image.get('id', '')), '缺少精确镜像身份: ' + service)
            require(image.get('reference', '').endswith(':' + VERSION), '镜像必须固定v1.1.1标签')
        entries = manifest.get('files')
        require(isinstance(entries, list), 'manifest.files必须为列表')
        expected = {}
        for entry in entries:
            name = entry['path']
            safe_member(name)
            require(name not in expected and name not in ('manifest.json', 'SHA256SUMS.txt'), '清单路径重复或自引用')
            require(name in members and members[name].isfile(), '清单文件不存在: ' + name)
            require(entry['size'] == members[name].size, '文件大小不符: ' + name)
            digest = hashlib.sha256()
            stream = archive.extractfile(members[name])
            for chunk in iter(lambda: stream.read(4 * 1024 * 1024), b''):
                digest.update(chunk)
            require(digest.hexdigest() == entry['sha256'], '文件摘要不符: ' + name)
            expected[name] = entry
        actual = {name for name, member in members.items() if member.isfile()}
        require(actual == set(expected) | {'manifest.json', 'SHA256SUMS.txt'}, '归档文件集合与清单不同')
        required = {'tools/platform_upgrade.py', 'LICENSE', 'NOTICE', 'RELEASE-NOTES.md', 'OPERATIONS.md'}
        require(required.issubset(expected) and any(n.startswith('images/') for n in expected), '包缺少必要负载')
        require(any(n.startswith('tools/rules-compatible/') for n in expected), '包缺少兼容规则工具负载')
        checksums = archive.extractfile(members['SHA256SUMS.txt']).read().decode('utf-8')
        require(checksums == ''.join(e['sha256'] + '  ' + e['path'] + '\n' for e in entries), 'SHA256SUMS与manifest不一致')
        if extract is not None:
            extract = Path(extract)
            extract.mkdir(mode=0o700)
            for name, member in members.items():
                destination = extract / name
                if member.isdir():
                    destination.mkdir(parents=True, exist_ok=True)
                else:
                    destination.parent.mkdir(parents=True, exist_ok=True)
                    with destination.open('xb') as stream:
                        shutil.copyfileobj(archive.extractfile(member), stream)
                    destination.chmod(member.mode & 0o755)
        return manifest


def run(args, timeout=120, input=None, check=True):
    completed = subprocess.run([str(a) for a in args], input=input, stdout=subprocess.PIPE,
                               stderr=subprocess.PIPE, timeout=timeout)
    if check and completed.returncode:
        raise RuntimeError('命令失败: ' + str(args[0]) + ' ' + ' '.join(str(a) for a in args[1:4]) + '\n' + completed.stderr.decode(errors='replace')[-4000:])
    return completed.stdout.decode('utf-8', errors='replace')


def docker_json(*args):
    return json.loads(run(['docker', *args]))


def inspect(container):
    values = docker_json('inspect', container)
    require(len(values) == 1, 'inspect目标不唯一')
    return values[0]


def real_path(path, directory=True):
    path = Path(path)
    require(path.is_absolute() and path.resolve() == path, '路径必须为真实规范绝对路径: ' + str(path))
    require(path.is_dir() if directory else path.is_file(), '路径类型或存在性不符: ' + str(path))
    for parent in [path, *path.parents]:
        require(not parent.is_symlink(), '禁止链接路径: ' + str(path))
    return path


def inventory(root, omit=()):
    """摘要包含权限和所有权；不读取或复制链接指向的外部文件。"""
    root = Path(root)
    result = {}
    def visit(path):
        key = path.relative_to(root).as_posix()
        if any(key == x or key.startswith(x + '/') for x in omit):
            return
        info = path.lstat()
        value = {'mode': stat.S_IMODE(info.st_mode), 'uid': info.st_uid, 'gid': info.st_gid}
        require(not stat.S_ISLNK(info.st_mode), '业务或规则含链接，须单独适配: ' + str(path))
        if path.is_dir():
            value['kind'] = 'directory'
            result[key] = value
            for child in sorted(path.iterdir()):
                visit(child)
        else:
            require(stat.S_ISREG(info.st_mode), '包含特殊文件: ' + str(path))
            value.update(kind='file', size=info.st_size, sha256=sha256(path))
            result[key] = value
    for child in sorted(root.iterdir()):
        visit(child)
    return result


def database_snapshot(path, idle=False):
    """SQLite只读连接且完整性检查；保存全部业务表精确行摘要，Agent在线表除外。"""
    path = Path(path)
    require(path.is_file() and not path.is_symlink(), '数据库不存在或为链接')
    connection = sqlite3.connect(path.resolve().as_uri() + '?mode=ro', uri=True)
    try:
        require(connection.execute('PRAGMA quick_check').fetchone() == ('ok',), '数据库完整性检查失败')
        connection.execute('BEGIN')
        if idle:
            busy = connection.execute("select id,status from sessions where status in ('todo','doing') limit 10").fetchall()
            require(not busy, '仍有待执行/执行中任务；须在旧平台显式终止并保留记录: ' + repr(busy))
        result = {}
        tables = connection.execute("select name,sql from sqlite_master where type='table' and name not like 'sqlite_%' order by name").fetchall()
        for name, sql in tables:
            if name == 'agents':
                continue
            rows = connection.execute('select * from "' + name.replace('"', '""') + '"').fetchall()
            encoded = []
            for row in rows:
                encoded.append(json.dumps([{'bytes': base64.b64encode(v).decode()} if isinstance(v, bytes) else v for v in row], ensure_ascii=False, separators=(',', ':')))
            result[name] = {'schema': sql, 'count': len(rows), 'rows_sha256': hashlib.sha256('\n'.join(sorted(encoded)).encode()).hexdigest()}
        return result
    finally:
        connection.close()


def business_data_inventory(root):
    # Docker建立精确子挂载时可能在父bind内创建空挂载点；这些目录不属于其他data。
    return inventory(root, omit=SCOPES)


def copy_program(source, destination):
    """创建新目录；仅排除废弃的三个版本内规则快照，保留其他业务内容。"""
    source, destination = Path(source), Path(destination)
    require(not destination.exists(), '目标目录已存在，拒绝覆盖: ' + str(destination))
    destination.mkdir(mode=0o700)
    for child in source.iterdir():
        if child.name == 'data' and child.is_dir():
            (destination / 'data').mkdir()
            shutil.copystat(child, destination / 'data')
            if hasattr(os, 'chown'):
                os.chown(destination / 'data', child.stat().st_uid, child.stat().st_gid)
            for nested in child.iterdir():
                if nested.name not in SCOPES:
                    run(['cp', '-a', '--', nested, destination / 'data' / nested.name], timeout=3600)
        else:
            run(['cp', '-a', '--', child, destination / child.name], timeout=3600)
    return destination


def mounts(container):
    result = {}
    for mount in container.get('Mounts', []):
        target = mount['Destination']
        require(target not in result, '重复挂载目标')
        result[target] = mount
    return result


def compose_command(project, files):
    result = ['docker', 'compose', '--project-name', project]
    for path in files:
        result.extend(['--file', str(path)])
    return result


def discover(source_images=None):
    """标签、有效Compose与实际挂载三方交叉核验，不猜测卷或配置文件。"""
    source_images = source_images or SOURCE_IMAGES
    web_ids = run(['docker', 'ps', '-q', '--filter', 'label=' + LABEL + 'service=webserver']).split()
    require(len(web_ids) == 1, '要求唯一运行中的Web双服务部署')
    web = inspect(web_ids[0])
    labels = web['Config'].get('Labels') or {}
    project = labels.get(LABEL + 'project', '')
    require(re.fullmatch('[a-z0-9][a-z0-9_-]*', project), '缺少有效Compose项目标签')
    require(labels.get(LABEL + 'project.working_dir') == str(OLD), '当前运行目录必须为/opt/aig/v1.1.0')
    files = labels.get(LABEL + 'project.config_files', '').split(',')
    require(files and len(files) == len(set(files)), 'Compose配置缺失或重复')
    for path in files:
        require(Path(path).parent == OLD, 'Compose配置必须来自旧运行目录')
        real_path(path, False)
    require({Path(p).name for p in files}.issuperset({'docker-compose.intranet.yml', 'docker-compose.upgrade.yml', 'docker-compose.rules.yml'}), '缺少完整Compose规则配置')
    agent_ids = run(['docker', 'ps', '-q', '--filter', 'label=' + LABEL + 'project=' + project, '--filter', 'label=' + LABEL + 'service=agent']).split()
    require(len(agent_ids) == 1, '要求同项目唯一运行Agent')
    all_ids = run(['docker', 'ps', '-q', '--filter', 'label=' + LABEL + 'project=' + project]).split()
    require(set(all_ids) == {web_ids[0], agent_ids[0]}, '仅支持旧Web/Agent双服务拓扑')
    agent = inspect(agent_ids[0])
    require(web['Name'] == '/ai-infra-guard-webserver' and agent['Name'] == '/ai-infra-guard-agent', '当前容器名称与已确认现场不符')
    effective = json.loads(run(compose_command(project, files) + ['config', '--format', 'json']))
    require(set(effective['services']) == {'webserver', 'agent'}, 'Compose包含未知服务')
    for service, actual in (('webserver', web), ('agent', agent)):
        require(actual['State']['Running'], '源容器未运行')
        require(actual['Image'] == source_images[service], '源镜像不在已交付v1.1.0固定身份中: ' + service)
        require(effective['services'][service]['image'] == actual['Config']['Image'], 'Compose镜像与运行实例不符')
        require(actual['Config']['Image'].endswith((':v1.1.0', ':1.1.0')), '仅支持v1.1.0镜像')
        image = docker_json('image', 'inspect', actual['Image'])[0]
        require(image['Os'] == 'linux' and image['Architecture'] == 'amd64', '旧镜像架构不受支持')
        require(not any(k in effective['services'][service] for k in ('build', 'network_mode', 'devices', 'configs', 'secrets')), '不支持现场自定义构建/设备/网络/secret布局')
        actual_env = dict(entry.split('=', 1) for entry in actual['Config'].get('Env', []) if '=' in entry)
        configured_env = effective['services'][service].get('environment', {})
        require(all(str(actual_env.get(k, '')) == str(v or '') for k, v in configured_env.items()), '有效环境配置与运行容器漂移')
        configured_mounts = {}
        for item in effective['services'][service].get('volumes', []):
            require(isinstance(item, dict), 'Compose必须支持规范JSON挂载')
            source = item.get('source')
            if item['type'] == 'volume':
                source = effective.get('volumes', {}).get(source, {}).get('name') or project + '_' + source
            require(item['target'] not in configured_mounts, 'Compose重复挂载')
            configured_mounts[item['target']] = (item['type'], source, not item.get('read_only', False))
        actual_mounts = {target: (m['Type'], m.get('Name') if m['Type'] == 'volume' else m['Source'], m['RW']) for target, m in mounts(actual).items()}
        require(configured_mounts == actual_mounts, 'Compose挂载与实际运行不同: ' + service)
    wm, am = mounts(web), mounts(agent)
    expected_web = {'/app/data', '/app/db', '/app/uploads', '/app/logs'} | {'/app/data/' + s for s in SCOPES}
    require(set(wm) == expected_web and set(am) == {'/api-checker-data'}, '存在未知业务挂载，须先适配')
    for target in ('data', 'db', 'uploads', 'logs'):
        m = wm['/app/' + target]
        require(m['Type'] == 'bind' and m['Source'] == str(OLD / target) and m['RW'], '业务bind来源必须为旧运行目录的原路径')
        real_path(m['Source'])
    real_path(RULES)
    for scope in SCOPES:
        m = wm['/app/data/' + scope]
        require(m['Type'] == 'bind' and m['Source'] == str(RULES / scope), '必须使用共享规则精确子挂载')
        real_path(RULES / scope)
        require(any((RULES / scope).iterdir()), '共享规则目录不能为空')
    require(am['/api-checker-data']['Type'] == 'volume' and am['/api-checker-data'].get('Name'), 'API Checker必须为明确命名卷')
    volume = docker_json('volume', 'inspect', am['/api-checker-data']['Name'])[0]
    require(volume.get('Driver') == 'local' and not volume.get('Options'), '不支持远程或特殊卷驱动')
    networks = set(web['NetworkSettings']['Networks']) & set(agent['NetworkSettings']['Networks'])
    require(len(networks) == 1 and len(web['NetworkSettings']['Networks']) == 1 and len(agent['NetworkSettings']['Networks']) == 1, '要求唯一共享网络')
    network = docker_json('network', 'inspect', next(iter(networks)))[0]
    require(network.get('Driver') == 'bridge' and not network.get('EnableIPv6'), '维护入口仅支持IPv4 bridge网络')
    require(set(network.get('Containers', {})) == {web['Id'], agent['Id']}, '项目网络存在其他运行容器，维护入口须先单独适配')
    require(web['HostConfig']['PortBindings'] and set(web['HostConfig']['PortBindings']) == {'8088/tcp'}, '只支持Web内部8088端口；保留实际发布地址和端口')
    require(not agent['HostConfig'].get('PortBindings'), 'API Checker不能另行暴露端口')
    return {'project': project, 'files': files, 'compose': effective, 'web': web, 'agent': agent,
            'network': network, 'checker_volume': volume, 'config_sha256': {p: sha256(p) for p in files}}


def save(path, value):
    path = Path(path)
    temporary = path.with_name(path.name + '.pending')
    with temporary.open('w', encoding='utf-8') as stream:
        json.dump(value, stream, ensure_ascii=False, indent=2)
        stream.write('\n')
        stream.flush()
        os.fsync(stream.fileno())
    temporary.chmod(0o600)
    temporary.replace(path)


def best_effort_save(path, value):
    """恢复诊断不能阻止恢复服务；磁盘满等写入失败只补充警告，保留原始错误。"""
    try:
        save(path, value)
        return True
    except BaseException as error:
        try:
            print('无法写入恢复诊断，继续恢复服务: ' + str(path) + ': ' + str(error), file=sys.stderr)
        except BaseException:
            pass
        return False


def begin_access_opening(gate, state, accepted_service, record_path):
    # 撤销第一条规则便可能接收写入；在调用前进入不可自动切换旧快照的阶段。
    state.update(phase='opening-access', access_may_be_public=True, accepted_service=accepted_service)
    save(record_path, state)
    gate.end()


def recover_failed_upgrade(work, state, layout, candidate_started, source_stopped, gate, expected_knowledge):
    best_effort_save(Path(work) / 'STATE.json', state)
    try:
        if candidate_started:
            ids = candidate_ids()
            if ids:
                stop_ids(ids)
        if source_stopped:
            recovery_since = datetime.datetime.now(datetime.timezone.utc).isoformat()
            run(['docker', 'start', layout['agent']['Id'], layout['web']['Id']], timeout=120)
            wait_health(layout['web']['Id'], layout['agent']['Id'], recovery_since, expected_knowledge)
        gate.end()
        state['recovery'] = 'source-restored'
    except BaseException as recovery_error:
        state['recovery'] = 'manual-required'
        state['recovery_error'] = str(recovery_error)
        try:
            print('自动恢复失败；保留维护入口，按STATE.json与maintenance-restore.json人工恢复: ' + str(work), file=sys.stderr)
        except BaseException:
            pass
    best_effort_save(Path(work) / 'STATE.json', state)


class Gate:
    """维护链只影响目标Web；保留回环和当前项目网络的内部探针。"""
    def __init__(self, layout, work):
        self.layout, self.work = layout, Path(work)
        self.chain = 'AIGP' + uuid.uuid4().hex[:12]
        self.undo = []

    def change(self, binary, args, undo):
        self.undo.append([binary, '-w', *undo])
        save(self.work / 'maintenance-restore.json', self.undo)
        run([binary, '-w', *args])

    def begin(self):
        subnet = self.layout['network']['IPAM']['Config']
        require(subnet, '缺少网络子网')
        subnets = [str(ipaddress.IPv4Network(x['Subnet'])) for x in subnet]
        ports = {p['HostPort'] for p in self.layout['web']['HostConfig']['PortBindings']['8088/tcp']}
        for binary in ('iptables', 'ip6tables'):
            self.change(binary, ['-N', self.chain], ['-X', self.chain])
            self.change(binary, ['-A', self.chain, '-i', 'lo', '-j', 'RETURN'], ['-D', self.chain, '-i', 'lo', '-j', 'RETURN'])
            if binary == 'iptables':
                for subnet in subnets:
                    args = [self.chain, '-s', subnet, '-j', 'RETURN']
                    self.change(binary, ['-A', *args], ['-D', *args])
            self.change(binary, ['-A', self.chain, '-j', 'DROP'], ['-D', self.chain, '-j', 'DROP'])
            for port in ports:
                args = ['INPUT', '-p', 'tcp', '--dport', port, '-j', self.chain]
                self.change(binary, ['-I', 'INPUT', '1', *args[1:]], ['-D', *args])
        run(['iptables', '-w', '-S', 'DOCKER-USER'])
        for subnet in subnets:
            args = ['DOCKER-USER', '-d', subnet, '-p', 'tcp', '--dport', '8088', '-j', self.chain]
            self.change('iptables', ['-I', 'DOCKER-USER', '1', *args[1:]], ['-D', *args])

    def end(self):
        failures = []
        for command in reversed(self.undo):
            try:
                run(command)
            except Exception as error:
                failures.append(str(error))
        if failures:
            raise RuntimeError('维护入口恢复失败；按maintenance-restore.json倒序恢复: ' + '; '.join(failures))
        self.undo = []


def preflight_environment():
    require(sys.platform.startswith('linux') and os.geteuid() == 0, '需要Linux root')
    require(os.uname().machine in ('x86_64', 'amd64'), '仅支持Linux amd64')
    for command in ('docker', 'cp', 'iptables', 'ip6tables'):
        require(shutil.which(command), '缺少离线依赖: ' + command)
    info = docker_json('info', '--format', '{{json .}}')
    require(info['OSType'] == 'linux' and not any(('rootless' in x or 'userns' in x) for x in info.get('SecurityOptions', [])), '不支持rootless/userns Docker')
    run(['docker', 'compose', 'version'])
    for path in (Path('/opt'), BASE, OLD):
        real_path(path)
    run(['iptables', '-w', '-S', 'DOCKER-USER'])
    run(['ip6tables', '-w', '-S', 'INPUT'])


def lock_all():
    import fcntl
    handles = []
    for name in ('.aig-rules.lock', '.upgrade-v1.1.0.lock'):
        path = BASE / name
        if path.exists():
            require(not path.is_symlink() and path.stat().st_nlink == 1 and path.is_file(), '锁路径必须为普通单链接文件')
        fd = os.open(str(path), os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
        handle = os.fdopen(fd, 'r+')
        fcntl.flock(handle, fcntl.LOCK_EX | fcntl.LOCK_NB)
        handles.append(handle)
    return handles


def current_rules():
    result = {}
    for scope in SCOPES:
        for key, value in inventory(RULES / scope).items():
            result[scope + '/' + key] = value
        info = (RULES / scope).stat()
        result[scope] = {'mode': stat.S_IMODE(info.st_mode), 'uid': info.st_uid, 'gid': info.st_gid, 'kind': 'directory'}
    return result


def rule_version_records():
    records = []
    root = BASE / 'rule-backups'
    if root.is_dir() and not root.is_symlink():
        for path in sorted(root.glob('*/DEPLOYED.txt')):
            real_path(path, False)
            text = path.read_text(encoding='utf-8')
            version = next((line.split('=', 1)[1] for line in text.splitlines() if line.startswith('version=')), None)
            records.append({'path': str(path), 'sha256': sha256(path), 'declared_version': version})
    return {'records': records, 'active_version': None,
            'interpretation': '历史部署记录仅供追溯；不据目录名猜测当前版本，以实际逐文件规则摘要为准'}


def curl_document(web, endpoint):
    value = run(['docker', 'exec', web, 'curl', '--noproxy', '*', '--fail', '--silent', '--show-error', '--max-time', '15', 'http://127.0.0.1:8088' + endpoint])
    return json.loads(value)


def curl_json(web, endpoint):
    parsed = curl_document(web, endpoint)
    require(parsed.get('status') == 0 and isinstance(parsed.get('data'), dict), '知识库接口失败: ' + endpoint)
    return parsed['data']


def knowledge(web):
    result = {}
    for scope in SCOPES:
        value = curl_json(web, '/api/v1/knowledge/fingerprints?page=1&size=9999' if scope == 'fingerprints'
                          else '/api/v1/knowledge/vulnerabilities?page=1&size=9999&lang=' + ('en' if scope == 'vuln_en' else 'zh'))
        count = sum(1 for p in (RULES / scope).rglob('*.yaml') if p.is_file())
        require(0 < count <= 9999 and value.get('total') == count and isinstance(value.get('items'), list)
                and len(value['items']) == count, '知识库规则数与实际YAML不符或超过旧Agent上限: ' + scope)
        result[scope] = value
    return result


def normalize_knowledge(value):
    """接口记录顺序可由目录遍历改变；保留字段与值，仅按规范JSON排序列表。"""
    if isinstance(value, dict):
        return {k: normalize_knowledge(v) for k, v in value.items()}
    if isinstance(value, list):
        return sorted((normalize_knowledge(v) for v in value), key=lambda x: json.dumps(x, sort_keys=True, ensure_ascii=False))
    return value


def verify_agent_status(value):
    agents = value.get('agents')
    require(isinstance(agents, list) and value.get('connected_count', 0) > 0, '无在线Agent真实连接')
    live = [a for a in agents if a.get('agent_id') and a.get('connected') is True]
    require(len(live) == value['connected_count'], 'Agent实时连接数量不一致')


def fetch_agent_status(web):
    # 此只读内部接口直接返回状态对象；知识库接口仍要求status/data封装。
    value = curl_document(web, '/api/v1/agents/status')
    verify_agent_status(value)
    return value


def wait_health(web, agent, since, expected_knowledge, timeout=180):
    deadline = time.monotonic() + timeout
    last_error = None
    while time.monotonic() < deadline:
        try:
            for name in (web, agent):
                state = inspect(name)['State']
                require(state['Running'] and state.get('Health', {}).get('Status') == 'healthy', '容器尚未健康: ' + name)
            run(['docker', 'exec', agent, '/app/api-checker-venv/bin/python', '-c',
                 "import urllib.request; urllib.request.build_opener(urllib.request.ProxyHandler({})).open('http://127.0.0.1:8000/healthz',timeout=3).read()"], timeout=10)
            if inspect(web)['Config']['Image'].endswith(':v1.1.1'):
                fetch_agent_status(web)
            else:
                # 旧版没有实时状态接口；只能使用本次重启后Web产生的注册记录，不能采用旧日志。
                logs = run(['docker', 'logs', '--since', since, web])
                require('Agent注册成功' in logs, '旧Web重启后尚无Agent真实注册记录')
                tail = logs[logs.rfind('Agent注册成功'):]
                require(not any(token in tail for token in ('Agent连接异常断开', 'Agent正常断开', 'Agent连接已关闭', 'Agent连接超时')), '旧Agent注册后再次断联')
            actual = knowledge(web)
            require(normalize_knowledge(actual) == normalize_knowledge(expected_knowledge), '升级后知识库读取内容改变')
            return {'healthy': True, 'agent_registered': True, 'api_checker': True, 'knowledge_equal': True}
        except Exception as error:
            last_error = error
            time.sleep(2)
    raise RuntimeError('候选平台验收失败: ' + str(last_error))


def volume_copy(image, source, destination):
    """只使用已固定的本地镜像，不联网；复制时不运行原入口、不写规则。"""
    require(source != destination, '卷复制源目标必须独立')
    run(['docker', 'run', '--rm', '--pull=never', '--network=none', '--user=0', '--entrypoint=/bin/sh',
         '--mount', 'type=volume,src=' + source + ',dst=/source,readonly',
         '--mount', 'type=volume,src=' + destination + ',dst=/destination', image,
         '-ec', 'test -z "$(ls -A /destination)"; cp -a /source/. /destination/'], timeout=3600)


def volume_hash(image, volume):
    code = """import os,hashlib,json,stat
root='/volume'
def digest(path):
    h=hashlib.sha256()
    with open(path,'rb') as stream:
        for chunk in iter(lambda:stream.read(4*1024*1024),b''):h.update(chunk)
    return h.hexdigest()
s=os.lstat(root)
r={'.':{'mode':stat.S_IMODE(s.st_mode),'uid':s.st_uid,'gid':s.st_gid}}
for p,ds,fs in os.walk(root):
    for n in sorted(ds+fs):
        f=os.path.join(p,n);s=os.lstat(f);k=os.path.relpath(f,root)
        v={'mode':stat.S_IMODE(s.st_mode),'uid':s.st_uid,'gid':s.st_gid}
        if stat.S_ISLNK(s.st_mode):v['link']=os.readlink(f)
        elif stat.S_ISREG(s.st_mode):v['sha256']=digest(f)
        elif not stat.S_ISDIR(s.st_mode):raise ValueError('special volume file')
        r[k]=v
print(json.dumps(r,sort_keys=True))"""
    return json.loads(run(['docker', 'run', '--rm', '--pull=never', '--network=none', '--user=0',
                           '--entrypoint=/app/api-checker-venv/bin/python', '--mount', 'type=volume,src=' + volume + ',dst=/volume,readonly',
                           image, '-c', code], timeout=3600))


def new_compose(layout, manifest, checker_volume, work):
    configuration = copy.deepcopy(layout['compose'])
    configuration['name'] = 'aig-v111'
    for service in ('webserver', 'agent'):
        item = configuration['services'][service]
        item['image'] = manifest['images'][service]['reference']
        item['pull_policy'] = 'never'
        item['container_name'] = 'ai-infra-guard-' + service + '-v111'
        for mount in item.get('volumes', []):
            if mount['type'] == 'bind' and mount['source'].startswith(str(OLD) + '/'):
                mount['source'] = str(NEW) + mount['source'][len(str(OLD)):]
                mount.setdefault('bind', {})['create_host_path'] = False
            elif mount['type'] == 'volume':
                require(mount['target'] == '/api-checker-data', '未知命名卷')
                mount['source'] = 'platform-checker'
        item.pop('env_file', None)
    configuration['volumes'] = {'platform-checker': {'name': checker_volume, 'external': True}}
    for key, network in configuration.get('networks', {}).items():
        actual_name = network.get('name') or layout['project'] + '_' + key
        require(actual_name == layout['network']['Name'], 'Compose网络与现场不同')
        configuration['networks'][key] = {'name': actual_name, 'external': True}
    paths = [NEW / name for name in ('docker-compose.intranet.yml', 'docker-compose.upgrade.yml', 'docker-compose.rules.yml')]
    # JSON也是合法YAML；三个固定文件名继续满足独立规则管理工具的完整配置契约。
    save(paths[0], configuration)
    save(paths[1], {'services': {}})
    save(paths[2], {'services': {}})
    return paths


def stop_ids(ids):
    run(['docker', 'stop', '--time', '30', *ids], timeout=90)
    require(all(not inspect(x)['State']['Running'] for x in ids), '源容器未完全停止')


def check_space(layout, manifest):
    program_bytes = sum(v.get('size', 0) for v in inventory(OLD, omit=tuple('data/' + s for s in SCOPES)).values())
    volume_path = real_path(layout['checker_volume']['Mountpoint'])
    volume_bytes = sum(v.get('size', 0) for v in inventory(volume_path).values())
    program_required = manifest['minimum_free_bytes'] + program_bytes * 3
    volume_required = volume_bytes * 3 + 512 * 1024 ** 2
    if BASE.stat().st_dev == volume_path.stat().st_dev:
        require(shutil.disk_usage(BASE).free >= program_required + volume_required, '磁盘空间不足（程序副本及独立Checker卷）')
    else:
        require(shutil.disk_usage(BASE).free >= program_required, '程序及备份目录磁盘空间不足')
        require(shutil.disk_usage(volume_path).free >= volume_required, 'Docker卷磁盘空间不足')


def candidate_ids():
    return run(['docker', 'ps', '-aq', '--filter', 'label=' + LABEL + 'project=aig-v111']).split()


def verify_candidate(layout, manifest, volume):
    web = inspect('ai-infra-guard-webserver-v111')
    agent = inspect('ai-infra-guard-agent-v111')
    for service, old_container, candidate in (('webserver', layout['web'], web), ('agent', layout['agent'], agent)):
        require(candidate['Image'] == manifest['images'][service]['id'], '候选运行镜像身份不同')
        require(candidate['HostConfig'].get('PortBindings') == old_container['HostConfig'].get('PortBindings'), '候选端口发布发生变化')
        old_env = set(old_container['Config'].get('Env', []))
        require(old_env.issubset(set(candidate['Config'].get('Env', []))), '候选环境丢失现场变量')
    wm = mounts(web)
    for scope in SCOPES:
        require(wm['/app/data/' + scope]['Source'] == str(RULES / scope), '候选共享规则路径变化')
    for name in ('data', 'db', 'uploads', 'logs'):
        require(wm['/app/' + name]['Source'] == str(NEW / name), '候选业务路径不是新版独立目录')
    require(mounts(agent)['/api-checker-data'].get('Name') == volume, '候选Checker卷不同')
    return web, agent


def upgrade(bundle, manifest):
    layout = discover(manifest['source_images'])
    require(not NEW.exists() and not NEW.is_symlink(), '目标版本目录已存在，拒绝覆盖')
    require(not candidate_ids(), '已有目标项目容器，拒绝覆盖')
    db_before = database_snapshot(OLD / 'db/tasks.db', idle=True)
    rules_before = current_rules()
    data_before = business_data_inventory(OLD / 'data')
    uploads_before = inventory(OLD / 'uploads')
    knowledge_before = knowledge(layout['web']['Id'])
    check_space(layout, manifest)
    locks = lock_all()
    require(current_rules() == rules_before, '预检后共享规则已变化')
    backup_root = BASE / 'backups'
    if backup_root.exists():
        real_path(backup_root)
    else:
        backup_root.mkdir(mode=0o700)
    work = backup_root / ('platform-v1.1.0-to-v1.1.1-' + time.strftime('%Y%m%d-%H%M%S') + '-' + uuid.uuid4().hex[:8])
    work.mkdir(mode=0o700)
    state = {'version': VERSION, 'source_version': 'v1.1.0', 'bundle_sha256': sha256(bundle), 'layout': layout,
             'rules': rules_before, 'rule_version_records': rule_version_records(),
             'db_before': db_before, 'knowledge_before': knowledge_before,
             'phase': 'preparing', 'new_directory': str(NEW), 'success': False}
    save(work / 'STATE.json', state)
    print('备份及恢复依据: ' + str(work), flush=True)
    gate = Gate(layout, work)
    stopped = False
    candidate_started = False
    try:
        validate_bundle(bundle, work / 'resource')
        for payload in sorted((work / 'resource/images').glob('*')):
            run(['docker', 'load', '--input', payload], timeout=3600)
        for image in manifest['images'].values():
            actual = docker_json('image', 'inspect', image['reference'])[0]
            require(actual['Id'] == image['id'] and actual['Os'] == 'linux' and actual['Architecture'] == 'amd64', '导入镜像身份/架构不符')
        gate.begin()
        database_snapshot(OLD / 'db/tasks.db', idle=True)
        require({p: sha256(p) for p in layout['files']} == layout['config_sha256'], 'Compose配置已并发变化')
        state['phase'] = 'stopping-source'
        save(work / 'STATE.json', state)
        stopped = True
        stop_ids([layout['web']['Id'], layout['agent']['Id']])
        require(database_snapshot(OLD / 'db/tasks.db', idle=True) == db_before, '维护期间历史数据库已变化')
        require(current_rules() == rules_before and business_data_inventory(OLD / 'data') == data_before
                and inventory(OLD / 'uploads') == uploads_before, '维护期间业务数据/规则已变化')
        program_before = inventory(OLD, omit=tuple('data/' + s for s in SCOPES))
        copy_program(OLD, work / 'original')
        require(inventory(work / 'original') == program_before, '完整备份文件/配置/日志/属性校验失败')
        require(database_snapshot(work / 'original/db/tasks.db') == db_before, '数据库备份校验失败')
        require(business_data_inventory(work / 'original/data') == data_before and inventory(work / 'original/uploads') == uploads_before, '业务文件备份校验失败')
        old_volume = layout['checker_volume']['Name']
        source_image = layout['agent']['Image']
        checker_before = volume_hash(source_image, old_volume)
        backup_volume = 'aig-v111-backup-' + uuid.uuid4().hex[:12]
        new_volume = 'aig-v111-checker-' + uuid.uuid4().hex[:12]
        for volume in (backup_volume, new_volume):
            run(['docker', 'volume', 'create', '--label', 'aig.platform.backup=' + str(work), volume])
        volume_copy(source_image, old_volume, backup_volume)
        volume_copy(source_image, old_volume, new_volume)
        require(volume_hash(source_image, backup_volume) == checker_before and volume_hash(source_image, new_volume) == checker_before, 'API Checker卷复制校验失败')
        state.update(backup_checker_volume=backup_volume, old_checker_volume=old_volume, new_checker_volume=new_volume,
                     checker_before=checker_before, phase='backed-up')
        save(work / 'STATE.json', state)
        copy_program(work / 'original', NEW)
        compatible_tools = work / 'resource/tools/rules-compatible'
        require(compatible_tools.is_dir() and any(compatible_tools.iterdir()), '交付缺少兼容规则管理工具')
        tools_target = NEW / 'platform-tools'
        require(not tools_target.exists(), '旧目录已有platform-tools，须先适配避免覆盖')
        tools_target.mkdir(mode=0o700)
        shutil.copytree(compatible_tools, tools_target / 'rules-compatible')
        for name in ('RELEASE-NOTES.md', 'OPERATIONS.md', 'LICENSE', 'NOTICE'):
            shutil.copy2(work / 'resource' / name, tools_target / name)
        compose_paths = new_compose(layout, manifest, new_volume, work)
        # 将规范化有效配置固定到单文件，保留所有已解析环境参数；原始配置仍在备份和旧目录。
        run(compose_command('aig-v111', compose_paths) + ['config', '--quiet'])
        since = datetime.datetime.now(datetime.timezone.utc).isoformat()
        state.update(phase='starting-candidate', candidate_compose=[str(p) for p in compose_paths], candidate_since=since)
        save(work / 'STATE.json', state)
        candidate_started = True
        run(compose_command('aig-v111', compose_paths) + ['up', '--detach', '--pull', 'never', '--no-build'], timeout=240)
        web, agent = verify_candidate(layout, manifest, new_volume)
        acceptance = wait_health(web['Id'], agent['Id'], since, knowledge_before)
        require(database_snapshot(NEW / 'db/tasks.db', idle=True) == db_before, '新版历史任务/模型等业务数据不一致')
        require(business_data_inventory(NEW / 'data') == data_before and inventory(NEW / 'uploads') == uploads_before, '新版业务文件不一致')
        require(current_rules() == rules_before, '共享规则字节或属性变化，拒绝成功')
        state.update(phase='validated', acceptance=acceptance, manifest=manifest, new_web=web, new_agent=agent)
        save(work / 'STATE.json', state)
        begin_access_opening(gate, state, 'candidate', work / 'STATE.json')
        state.update(phase='deployed', success=True, completed_at=datetime.datetime.now(datetime.timezone.utc).isoformat())
        save(work / 'STATE.json', state)
        save(NEW / 'PLATFORM-DEPLOYED.json', state)
        print('平台v1.1.1升级成功；共享规则不变。备份: ' + str(work), flush=True)
    except BaseException as error:
        if state.get('access_may_be_public'):
            # 对外恢复后可能已有新数据；记录落盘失败也绝不能自动切回旧快照。
            state.update(phase='manual-required-after-access-opening', success=False, error=str(error),
                         recovery='candidate-preserved-no-snapshot-switch')
            best_effort_save(work / 'STATE.json', state)
            print('新版已验收，开放访问阶段失败；保留新版运行，禁止自动回退，请人工核对维护策略及记录: ' + str(work), file=sys.stderr)
            raise
        state.update(phase='failed', error=str(error))
        recover_failed_upgrade(work, state, layout, candidate_started, stopped, gate, knowledge_before)
        raise
    finally:
        for handle in locks:
            handle.close()


def rollback(backup):
    backup = real_path(backup)
    require(backup.parent == BASE / 'backups', '回滚备份必须为本平台升级记录的直接目录')
    state = json.loads((backup / 'STATE.json').read_text(encoding='utf-8'))
    require(state.get('success') is True and state.get('phase') == 'deployed' and state.get('version') == VERSION, '备份身份不是已成功的本次升级')
    require(json.loads((NEW / 'PLATFORM-DEPLOYED.json').read_text())['bundle_sha256'] == state['bundle_sha256'], '当前平台与备份身份不符')
    require(current_rules() == state['rules'], '升级后规则已变化；须先独立验证旧平台兼容，不能自动回退规则')
    layout = state['layout']
    for role in ('web', 'agent'):
        old = inspect(layout[role]['Id'])
        require(not old['State']['Running'] and old['Image'] == layout[role]['Image'], '旧容器状态或镜像不符')
    require({p: sha256(p) for p in layout['files']} == layout['config_sha256'], '旧Compose配置已变化')
    require(set(candidate_ids()) == {state['new_web']['Id'], state['new_agent']['Id']}
            or set(candidate_ids()) == {state['new_web']['Id'][:12], state['new_agent']['Id'][:12]}, '当前候选项目身份与本次备份不同')
    verify_candidate(layout, state['manifest'], state['new_checker_volume'])
    locks = lock_all()
    gate = Gate({'web': state['new_web'], 'network': layout['network']}, backup)
    stopped = False
    rollback_info = {'success': False}
    try:
        database_snapshot(NEW / 'db/tasks.db', idle=True)
        gate.begin()
        require(current_rules() == state['rules'], '获得锁后规则已变化')
        database_snapshot(NEW / 'db/tasks.db', idle=True)
        stopped = True
        stop_ids([state['new_web']['Id'], state['new_agent']['Id']])
        snapshot = backup / ('candidate-preserved-' + time.strftime('%Y%m%d-%H%M%S') + '-' + uuid.uuid4().hex[:8])
        copy_program(NEW, snapshot)
        candidate_volume = 'aig-v111-preserved-' + uuid.uuid4().hex[:12]
        run(['docker', 'volume', 'create', candidate_volume])
        volume_copy(state['new_agent']['Image'], state['new_checker_volume'], candidate_volume)
        rollback_info.update(preserved_directory=str(snapshot), preserved_checker_volume=candidate_volume)
        save(backup / 'ROLLBACK.json', rollback_info)
        # 无迁移发布仅在精确业务数据相等时自动切回；有新增写入则完整保存后拒绝丢数据回退。
        require(database_snapshot(NEW / 'db/tasks.db', idle=True) == state['db_before']
                and inventory(NEW / 'uploads') == inventory(backup / 'original/uploads')
                and business_data_inventory(NEW / 'data') == business_data_inventory(backup / 'original/data')
                and volume_hash(state['new_agent']['Image'], state['new_checker_volume']) == state['checker_before'],
                '新版已有业务写入，已保存快照；拒绝丢数据回滚，请安排显式数据合并/授权恢复')
        require(database_snapshot(OLD / 'db/tasks.db') == state['db_before'], '旧数据库已漂移，拒绝启动')
        require(volume_hash(layout['agent']['Image'], state['old_checker_volume']) == state['checker_before'], '旧Checker卷已漂移，拒绝启动')
        since = datetime.datetime.now(datetime.timezone.utc).isoformat()
        run(['docker', 'start', layout['agent']['Id'], layout['web']['Id']], timeout=120)
        wait_health(layout['web']['Id'], layout['agent']['Id'], since, state['knowledge_before'])
        begin_access_opening(gate, rollback_info, 'source', backup / 'ROLLBACK.json')
        rollback_info['success'] = True
        rollback_info['phase'] = 'rolled-back'
        save(backup / 'ROLLBACK.json', rollback_info)
        state['phase'] = 'rolled-back'
        save(backup / 'STATE.json', state)
        print('回滚成功；新版目录与卷、共享规则均已保留。备份: ' + str(backup))
    except BaseException as error:
        if rollback_info.get('access_may_be_public'):
            rollback_info.update(phase='manual-required-after-access-opening', success=False,
                                 error=str(error), recovery='source-preserved-no-snapshot-switch')
            best_effort_save(backup / 'ROLLBACK.json', rollback_info)
            print('旧平台已验收，开放访问阶段失败；保留旧平台运行，禁止自动切换，请人工核对维护策略及记录: ' + str(backup), file=sys.stderr)
            raise
        rollback_info['error'] = str(error)
        try:
            if stopped:
                # 失败优先恢复刚才仍可运行的新版，先确保旧版绝不同时写入。
                stop_ids([layout['web']['Id'], layout['agent']['Id']])
                recovery_since = datetime.datetime.now(datetime.timezone.utc).isoformat()
                run(['docker', 'start', state['new_agent']['Id'], state['new_web']['Id']], timeout=120)
                wait_health(state['new_web']['Id'], state['new_agent']['Id'], recovery_since, state['knowledge_before'])
                rollback_info['recovery'] = 'candidate-restored'
            gate.end()
        except BaseException as recovery_error:
            rollback_info.update(recovery='manual-required', recovery_error=str(recovery_error))
            try:
                print('回滚失败后的新版恢复未完成；保留维护入口，请按备份人工处理: ' + str(backup), file=sys.stderr)
            except BaseException:
                pass
        best_effort_save(backup / 'ROLLBACK.json', rollback_info)
        raise
    finally:
        for handle in locks:
            handle.close()


def main(argv=None):
    parser = argparse.ArgumentParser(description='v1.1.0双服务→v1.1.1纯平台离线升级；停机维护，保留原规则/历史目录/命名卷。需要root、Python3.8+、Docker Compose v2、iptables/ip6tables。先显式终止旧卡死任务。')
    parser.add_argument('--bundle', required=True, help=argparse.SUPPRESS)
    parser.add_argument('--check', action='store_true', help='只读预检：不停止、不导入镜像、不创建目录/锁')
    parser.add_argument('--rollback', action='store_true', help='回滚本次升级；新版已有写入时保存并拒绝丢数据回退')
    parser.add_argument('--backup', help='本次平台备份绝对路径，仅用于--rollback')
    args = parser.parse_args(argv)
    require(not (args.check and args.rollback) and bool(args.backup) == args.rollback, '参数组合错误')
    manifest = validate_bundle(args.bundle)
    preflight_environment()
    if args.rollback:
        rollback(args.backup)
    elif args.check:
        layout = discover(manifest['source_images'])
        require(not NEW.exists() and not NEW.is_symlink(), '目标版本目录已存在')
        require(not candidate_ids(), '已有目标项目容器')
        database_snapshot(OLD / 'db/tasks.db', idle=True)
        current_rules()
        knowledge(layout['web']['Id'])
        check_space(layout, manifest)
        print('只读预检通过；实际源项目=' + layout['project'] + '；共享规则=' + str(RULES))
    else:
        upgrade(Path(args.bundle), manifest)


if __name__ == '__main__':
    try:
        def interrupted(signum, frame):
            raise RuntimeError('收到终止信号，进入有记录的恢复流程: ' + str(signum))
        signal.signal(signal.SIGTERM, interrupted)
        main()
    except Exception as error:
        print('平台操作失败: ' + str(error), file=sys.stderr)
        sys.exit(1)
