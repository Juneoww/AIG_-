#!/usr/bin/env python3
"""功能：生成v1.1.1纯平台离线资源包和固定摘要的一键入口。
实现：从已验证stage枚举完整负载，生成manifest/摘要，安全自检后写两个外部文件。
输入：--stage、--output、源码提交及Web/Agent精确镜像身份；规则兼容验证须显式声明。
输出：AIG-platform-v1.1.1-linux-amd64.tar.gz、deploy-platform-v1.1.1.sh。
依赖：Python3标准库；不构建镜像，不下载资源，不携带现场数据。
"""
import argparse
import base64
import datetime
import hashlib
import io
import json
from pathlib import Path
import sys
import tarfile

from platform_upgrade import VERSION, SOURCE_IMAGES, require, safe_member, sha256, validate_bundle


def wrapper(digest, helper):
    encoded = base64.b64encode(helper).decode('ascii')
    return '''#!/usr/bin/env bash
# 功能：校验并执行v1.1.1纯平台离线升级/只读预检/回滚。
# 实现：固定资源包SHA256，使用内嵌Python标准库工具；不联网安装、不拉镜像。
# 输入：同目录配套包；--check / --rollback --backup 绝对路径 / --help。
# 输出：独立新版程序目录、Checker卷和平台备份；共享规则保持原样。
# 依赖：Linux amd64 root、Python3.8+、Docker Compose v2、cp、iptables/ip6tables。
set -euo pipefail
if [[ ${1:-} == --help ]]; then
    cat <<'HELP'
用法：sudo bash deploy-platform-v1.1.1.sh [--check | --rollback --backup /opt/aig/backups/本次备份]
适用：已解耦规则的v1.1.0旧Web/Agent双服务，Linux amd64。
无参数：停机备份并升级；--check只读且不导入镜像、不创建目录/锁。
--rollback：先保存新版数据；已有业务写入时拒绝丢数据回退。
依赖：root、Python3.8+、Docker Compose v2、cp、iptables/ip6tables及DOCKER-USER链。
维护前须显式终止旧卡死任务、结束所有API Checker检查，并停止人工编辑。
自动空闲检查只覆盖扫描任务，不能证明旧Checker已空闲；共享规则不会写入。
断电/SIGKILL后按备份STATE.json和maintenance-restore.json人工恢复。
HELP
    exit 0
fi
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
PACKAGE="$SCRIPT_DIR/AIG-platform-v1.1.1-linux-amd64.tar.gz"
command -v python3 >/dev/null || { echo '缺少现有Python3，禁止联网安装依赖' >&2; exit 1; }
exec python3 - "$PACKAGE" "$@" <<'PY'
import base64, hashlib, pathlib, sys
package = pathlib.Path(sys.argv[1])
if package.is_symlink() or not package.is_file():
    raise SystemExit('配套包缺失或为软链接')
digest = hashlib.sha256()
with package.open('rb') as source:
    for chunk in iter(lambda: source.read(4 * 1024 * 1024), b''):
        digest.update(chunk)
if digest.hexdigest() != 'DIGEST_PLACEHOLDER':
    raise SystemExit('配套资源包SHA256不匹配，未进行部署操作')
sys.argv = ['platform_upgrade.py', '--bundle', str(package), *sys.argv[2:]]
exec(compile(base64.b64decode('HELPER_PLACEHOLDER'), 'platform_upgrade.py', 'exec'), {'__name__':'__main__'})
PY
'''.replace('DIGEST_PLACEHOLDER', digest).replace('HELPER_PLACEHOLDER', encoded)


def build(args):
    stage, output = Path(args.stage).resolve(), Path(args.output).resolve()
    require(stage.is_dir() and stage != output and stage not in output.parents and output not in stage.parents, 'stage/output必须为独立目录')
    require(args.rule_tools_verified, '必须先验证v1.1.1双服务规则管理工具，再传--rule-tools-verified')
    require((stage / 'tools/rules-compatible').is_dir() and any((stage / 'tools/rules-compatible').iterdir()), '包必须携带兼容规则工具')
    helper = Path(__file__).with_name('platform_upgrade.py').read_bytes()
    (stage / 'tools').mkdir(exist_ok=True)
    (stage / 'tools/platform_upgrade.py').write_bytes(helper)
    require(not (stage / 'manifest.json').exists() and not (stage / 'SHA256SUMS.txt').exists(), 'stage不应预含旧manifest或SHA256SUMS')
    payload = {}
    entries = []
    for path in sorted(stage.rglob('*')):
        require(not path.is_symlink(), 'stage禁止链接: ' + str(path))
        if path.is_dir():
            continue
        require(path.is_file(), 'stage含特殊文件')
        name = path.relative_to(stage).as_posix()
        safe_member(name)
        payload[name] = path
        entries.append({'path': name, 'size': path.stat().st_size, 'sha256': sha256(path)})
    manifest = {'type': 'platform', 'version': VERSION, 'contract_version': '1.0',
                'source_versions': ['v1.1.0'], 'source_commit': args.source_commit,
                'source_images': SOURCE_IMAGES,
                'built_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
                'architecture': 'linux/amd64', 'contains_infrastructure_rules': False,
                'rule_mounts': {s: '/opt/aig/rule-data/' + s for s in ('fingerprints', 'vuln', 'vuln_en')},
                'rules_compatibility': {'engine_baseline': 'c2c18547cef9276c376f86057a5387b7e61bfa62', 'validated': True, 'format': 'unchanged-v1.1.0'},
                'rule_tools_compatibility': {'version': VERSION, 'layout': 'legacy-two-service', 'validated': True},
                'migration': {'required': False}, 'api_checker_volume': 'independent-copy-preserve-source',
                'ports': 'inherit-all-existing-published-bindings', 'environment': 'inherit-effective-config',
                'dependencies': ['root', 'python3>=3.8', 'docker-compose-v2', 'cp', 'iptables', 'ip6tables'],
                'minimum_free_bytes': int(args.minimum_free_gib * 1024 ** 3),
                'images': {'webserver': {'reference': args.web_reference, 'id': args.web_id},
                           'agent': {'reference': args.agent_reference, 'id': args.agent_id}}, 'files': entries}
    output.mkdir(parents=True, exist_ok=True)
    package = output / 'AIG-platform-v1.1.1-linux-amd64.tar.gz'
    entry = output / 'deploy-platform-v1.1.1.sh'
    require(not package.exists() and not entry.exists(), '拒绝覆盖历史交付文件')
    with tarfile.open(package, 'w:gz', compresslevel=1) as archive:
        for name, path in payload.items():
            archive.add(path, arcname=name, recursive=False)
        generated = {'manifest.json': json.dumps(manifest, ensure_ascii=False, indent=2).encode() + b'\n',
                     'SHA256SUMS.txt': ''.join(e['sha256'] + '  ' + e['path'] + '\n' for e in entries).encode()}
        for name, body in generated.items():
            info = tarfile.TarInfo(name)
            info.size, info.mode = len(body), 0o644
            archive.addfile(info, io.BytesIO(body))
    validate_bundle(package)
    entry.write_text(wrapper(sha256(package), helper), encoding='utf-8', newline='\n')
    entry.chmod(0o755)
    for path in (entry, package):
        print(sha256(path) + '  ' + str(path))
    return package, entry


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--stage', required=True)
    parser.add_argument('--output', required=True)
    parser.add_argument('--source-commit', required=True)
    parser.add_argument('--web-reference', required=True)
    parser.add_argument('--web-id', required=True)
    parser.add_argument('--agent-reference', required=True)
    parser.add_argument('--agent-id', required=True)
    parser.add_argument('--minimum-free-gib', type=float, default=8)
    parser.add_argument('--rule-tools-verified', action='store_true')
    build(parser.parse_args())


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        print('平台打包失败: ' + str(error), file=sys.stderr)
        sys.exit(1)
