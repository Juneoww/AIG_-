#!/usr/bin/env python3
"""功能：从固定纯平台 v1.1.1 Web 镜像构建仅修复一期页面的离线镜像。
实现：先核对静态入口及 Go 二进制实际嵌入内容，再固定父镜像、复制 Web、核对运行配置。
输入：--binary Linux amd64 Web、--build-root 新构建目录、--revision 完整源码提交。
输出：v1.1.1-ui1 Web 镜像、web.tar 和不含现场信息的来源记录；Agent 不构建。
依赖：Python 标准库、本机 Linux Docker、已缓存固定源镜像；构建不联网。
"""
import argparse
import hashlib
import json
from pathlib import Path
import re
import shutil
import subprocess
import sys

BASE_IMAGE = 'ai-infra-guard-webserver:v1.1.1'
BASE_ID = 'sha256:adeef221d46780cae1fab812a6c65ad8e41ce873e2b256570a0cb341052efac6'
TARGET_IMAGE = 'ai-infra-guard-webserver:v1.1.1-ui1'


def run(args):
    return subprocess.check_output([str(x) for x in args], text=True).strip()


def inspect(reference):
    return json.loads(run(['docker', 'image', 'inspect', reference]))[0]


def sha256(path):
    result = hashlib.sha256()
    with path.open('rb') as stream:
        for block in iter(lambda: stream.read(4 * 1024 * 1024), b''):
            result.update(block)
    return result.hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=Path, required=True)
    parser.add_argument('--build-root', type=Path, required=True)
    parser.add_argument('--revision', required=True)
    args = parser.parse_args()
    if not re.fullmatch('[0-9a-f]{40}', args.revision):
        raise ValueError('需要完整源码提交')
    binary, root = args.binary.resolve(), args.build_root.resolve()
    if root.exists():
        raise ValueError('构建目录必须不存在，保留已有构建证据')
    with binary.open('rb') as stream:
        header = stream.read(20)
    if header != b'\x7fELF\x02\x01\x01\x00' + b'\x00' * 8 + b'\x02\x00\x3e\x00':
        raise ValueError('需要 Linux amd64 ELF 可执行程序')
    scripts = Path(__file__).resolve().parent
    ui = run([sys.executable, scripts / 'verify_embedded_frontend.py', '--static',
              scripts.parent / 'common/websocket/static', '--binary', binary])
    base = inspect(BASE_IMAGE)
    if base['Id'] != BASE_ID or base['Os'] != 'linux' or base['Architecture'] != 'amd64':
        raise ValueError('当前 Web 源镜像不在已交付固定身份中')
    root.mkdir(parents=True)
    shutil.copy2(binary, root / 'ai-infra-guard')
    recipe = '\n'.join([
        '# 功能：固定纯平台源镜像，仅替换已验收的 Web 程序。',
        'FROM ' + BASE_IMAGE,
        'LABEL org.opencontainers.image.version="v1.1.1-ui1"',
        'LABEL org.opencontainers.image.revision="' + args.revision + '"',
        'COPY --chmod=755 ai-infra-guard /app/ai-infra-guard', ''])
    (root / 'Dockerfile').write_text(recipe, encoding='utf-8', newline='\n')
    subprocess.run(['docker', 'build', '--network=none', '--pull=false', '-t', TARGET_IMAGE, str(root)], check=True)
    target = inspect(TARGET_IMAGE)
    keys = ('Env', 'User', 'WorkingDir', 'Entrypoint', 'Cmd', 'ExposedPorts', 'Volumes', 'Healthcheck')
    if any(target['Config'].get(k) != base['Config'].get(k) for k in keys):
        raise ValueError('补丁不允许改变原 Web 运行配置')
    before, after = base['RootFS']['Layers'], target['RootFS']['Layers']
    if after[:len(before)] != before or len(after) != len(before) + 1:
        raise ValueError('补丁镜像必须继承已验证源层且只增加 Web 层')
    subprocess.run(['docker', 'save', '-o', str(root / 'web.tar'), TARGET_IMAGE], check=True)
    ui_record = json.loads(ui)
    ui_record.update(static='common/websocket/static', binary_verified='ai-infra-guard')
    record = {'version': 'v1.1.1-ui1', 'source_commit': args.revision,
              'source_image': BASE_IMAGE, 'source_image_id': BASE_ID,
              'image': TARGET_IMAGE, 'image_id': target['Id'],
              'binary_sha256': sha256(binary), 'archive_sha256': sha256(root / 'web.tar'),
              'ui_verification': ui_record, 'runtime_config_preserved': True,
              'parent_layers_preserved': True, 'agent_unchanged': True}
    (root / 'PROVENANCE.json').write_text(json.dumps(record, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
    print(json.dumps(record, ensure_ascii=False, indent=2))


if __name__ == '__main__':
    main()
