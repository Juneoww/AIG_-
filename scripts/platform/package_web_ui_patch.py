#!/usr/bin/env python3
"""功能：生成独立Web UI补丁包和固定包SHA256的一键入口。
实现：只接受一个Web镜像归档和白名单文档/工具，完整枚举清单；拒绝覆盖交付。
输入：--web-image、--web-id、两个完整源码提交、--output-dir；可选兼容转换器和文档。
输出：deploy-web-ui-v1.1.1-ui1.sh、AIG-web-ui-v1.1.1-ui1-linux-amd64.tar.gz。
依赖：Python标准库；用法见 --help；本工具不构建/导入镜像、不访问生产环境。
"""
import argparse
import base64
import datetime
import hashlib
import io
import json
from pathlib import Path
import re
import tarfile

import web_ui_patch as w

PACKAGE = 'AIG-web-ui-v1.1.1-ui1-linux-amd64.tar.gz'
ENTRY = 'deploy-web-ui-v1.1.1-ui1.sh'


def wrapper(digest, helper, library):
    return '''#!/usr/bin/env bash
# 功能：固定SHA256核验后执行v1.1.1 Web UI补丁、只读预检或回退。
# 实现：Python标准库工具在内存运行；--check不解压/导入镜像/创建锁。
# 输入：同目录配套资源包；--check / --rollback --backup 绝对路径。
# 输出：Web重建及独立备份；业务数据、Agent与共享规则保留。
# 依赖：Linux amd64 root、Python3.8+、Docker Compose v2、cp、iptables/ip6tables。
set -euo pipefail
if [[ ${1:-} == --help ]]; then
  cat <<'HELP'
用法：sudo bash deploy-web-ui-v1.1.1-ui1.sh [--check | --rollback --backup /opt/aig/backups/本次备份]
仅适用已部署/opt/aig/v1.1.1、aig-v111项目的固定Web/Agent镜像。
无参数：维护入口、空闲检查、冷备份，然后仅重建Web；保持全部实际端口和配置。
--check：只读核验，不创建目录/锁，不导入镜像；配套包缺失或损坏立即拒绝。
--rollback：仅换回原Web，保留补丁运行后全部业务写入、Checker卷和当前规则。
须预先终止卡死任务、停止人工编辑并结束所有API Checker检查；Agent容器不重建。
断电/SIGKILL后依据备份STATE.json及maintenance-restore.json恢复；保留所有历史交付。
HELP
  exit 0
fi
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
command -v python3 >/dev/null || { echo '缺少现有Python3，禁止联网安装' >&2; exit 1; }
exec python3 - "$SCRIPT_DIR/AIG-web-ui-v1.1.1-ui1-linux-amd64.tar.gz" "$@" <<'PY'
import base64, hashlib, pathlib, sys, types
package = pathlib.Path(sys.argv[1])
if package.is_symlink() or not package.is_file():
    raise SystemExit('配套包缺失或为链接')
digest = hashlib.sha256()
with package.open('rb') as stream:
    for block in iter(lambda: stream.read(4*1024*1024), b''):
        digest.update(block)
if digest.hexdigest() != 'PACKAGE_DIGEST':
    raise SystemExit('配套包SHA256不符；未执行部署操作')
platform_upgrade = types.ModuleType('platform_upgrade')
platform_upgrade.__file__ = '<bundled-platform_upgrade.py>'
exec(compile(base64.b64decode('LIBRARY_BASE64'), platform_upgrade.__file__, 'exec'), platform_upgrade.__dict__)
sys.modules['platform_upgrade'] = platform_upgrade
sys.argv = ['web_ui_patch.py', '--bundle', str(package), *sys.argv[2:]]
exec(compile(base64.b64decode('HELPER_BASE64'), '<bundled-web_ui_patch.py>', 'exec'), {'__name__': '__main__', '__file__': '<bundled-web_ui_patch.py>'})
PY
'''.replace('PACKAGE_DIGEST', digest).replace('LIBRARY_BASE64', base64.b64encode(library).decode()).replace('HELPER_BASE64', base64.b64encode(helper).decode())


def build(args):
    for value in (args.source_commit, args.image_source_commit):
        w.require(re.fullmatch('[0-9a-f]{40}', value), '源码提交必须为完整40位hash')
    w.require(re.fullmatch('sha256:[0-9a-f]{64}', args.web_id) and args.web_id != w.SOURCE_WEB, '目标镜像ID无效')
    output = Path(args.output_dir).resolve()
    w.require(not output.is_symlink(), '输出目录不能为链接')
    package, entry = output / PACKAGE, output / ENTRY
    w.require(not package.exists() and not entry.exists(), '拒绝覆盖历史交付')
    here = Path(__file__).resolve().parent
    repo = here.parent.parent
    default_doc = repo / 'docs/AI-Infra-Scan_Plus/v1.1.1-ui1-operations.md'
    if not default_doc.is_file():
        default_doc = here / 'README.md'
    paths = {'payload/images/web.tar': Path(args.web_image),
             'tools/web_ui_patch.py': here / 'web_ui_patch.py', 'tools/platform_upgrade.py': here / 'platform_upgrade.py',
             'LICENSE': repo / 'LICENSE', 'NOTICE': repo / 'NOTICE',
             'OPERATIONS.md': Path(args.operations) if args.operations else default_doc,
             'RELEASE-NOTES.md': Path(args.release_notes) if args.release_notes else repo / 'docs/AI-Infra-Scan_Plus/v1.1.1-ui1-release-notes.md'}
    if args.rules_converter:
        paths[w.CONVERTER] = Path(args.rules_converter)
    for path in paths.values():
        w.require(path.is_file() and not path.is_symlink(), '负载缺失/为链接: ' + str(path))
    entries = [{'path': name, 'size': path.stat().st_size, 'sha256': w.p.sha256(path)} for name, path in sorted(paths.items())]
    manifest = {'format': 'aig-web-ui-patch-v1', 'version': w.VERSION,
                'source_web': w.SOURCE_WEB, 'source_agent': w.SOURCE_AGENT,
                'source_commit': args.source_commit, 'image_source_commit': args.image_source_commit,
                'built_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
                'architecture': 'linux/amd64', 'contains_infrastructure_rules': False,
                'migration': {'required': False}, 'ports': 'inherit-all', 'environment': 'inherit-all',
                'agent': 'preserve-exact-container-image-and-volume', 'business_data': 'in-place-preserve-all-writes',
                'web_image': {'tag': w.TARGET_TAG, 'id': args.web_id, 'archive': 'payload/images/web.tar'},
                'tools': {'rules_converter': w.CONVERTER} if args.rules_converter else {}, 'files': entries}
    output.mkdir(parents=True, exist_ok=True)
    try:
        with tarfile.open(package, 'x:gz', compresslevel=1) as archive:
            for name, path in sorted(paths.items()):
                archive.add(path, arcname=name, recursive=False)
            for name, body in {'manifest.json': (json.dumps(manifest, ensure_ascii=False, indent=2) + '\n').encode(),
                               'SHA256SUMS.txt': ''.join(e['sha256'] + '  ' + e['path'] + '\n' for e in entries).encode()}.items():
                info = tarfile.TarInfo(name)
                info.size, info.mode = len(body), 0o644
                archive.addfile(info, io.BytesIO(body))
        w.validate_bundle(package)
        with entry.open('x', encoding='utf-8', newline='\n') as target:
            target.write(wrapper(w.p.sha256(package), paths['tools/web_ui_patch.py'].read_bytes(), paths['tools/platform_upgrade.py'].read_bytes()))
        entry.chmod(0o755)
    except BaseException:
        # 未发布的本次新文件可删除；绝不触碰既有历史交付。
        if package.exists():
            package.unlink()
        raise
    for path in (entry, package):
        print(w.p.sha256(path) + '  ' + str(path))
    return package, entry


def parser():
    value = argparse.ArgumentParser(description=__doc__)
    value.add_argument('--web-image', required=True)
    value.add_argument('--web-id', required=True)
    value.add_argument('--source-commit', required=True)
    value.add_argument('--image-source-commit', required=True)
    value.add_argument('--output-dir', '--output', dest='output_dir', required=True)
    value.add_argument('--rules-converter')
    value.add_argument('--operations')
    value.add_argument('--release-notes')
    return value


if __name__ == '__main__':
    build(parser().parse_args())
