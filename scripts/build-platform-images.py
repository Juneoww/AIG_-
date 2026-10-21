#!/usr/bin/env python3
"""功能：从已验证 v1.1.0 运行时构建不携带基础设施规则层的 v1.1.1 镜像。
实现：导出旧容器文件系统，流式过滤规则/旧二进制，再从 scratch 添加干净根文件系统。
输入：构建目录、已编译 Linux amd64 二进制；输出：Docker 镜像和来源配置记录。
依赖：Python 标准库、Linux Docker daemon、已缓存旧镜像；不联网安装依赖。
用法：python scripts/build-platform-images.py --build-root PATH --binaries PATH --revision COMMIT。
"""
import argparse
import hashlib
import json
from pathlib import Path, PurePosixPath
import subprocess
import tarfile

SCOPES = {'fingerprints', 'vuln', 'vuln_en'}
SOURCE_IDS = {
    'webserver': 'sha256:9cfc382ead21bc32a99d3ae43826695d59286f1cce164b2cfd53668c175d898d',
    'agent': 'sha256:c2e81a8e49d2d1bde7bdff780a0e0681a920128627789f06f46df5fd6a24a918',
}


def file_hash(path):
    value = hashlib.sha256()
    with Path(path).open('rb') as stream:
        for block in iter(lambda: stream.read(4 * 1024 * 1024), b''):
            value.update(block)
    return value.hexdigest()


def require_source(info, role):
    if info['Id'] != SOURCE_IDS[role] or info['Architecture'] != 'amd64' or info['Os'] != 'linux':
        raise ValueError('运行时必须为已交付的固定 Linux amd64 v1.1.0 镜像: ' + role)


def verify_cache(path, role, provenance, source_hash=None):
    if (not provenance.is_file() or not path.is_file()
            or path.is_symlink() or provenance.is_symlink()):
        raise ValueError('缓存缺少完整来源记录，使用新构建目录重新导出: ' + str(path))
    record = json.loads(provenance.read_text(encoding='utf-8'))
    if (record.get('image_id') != SOURCE_IDS[role] or record.get('sha256') != file_hash(path)
            or (source_hash is not None and record.get('source_sha256') != source_hash)):
        raise ValueError('运行时缓存内容或来源不符: ' + str(path))
    return record['sha256']


def excluded(name):
    parts = PurePosixPath(name.lstrip('./')).parts
    return (len(parts) >= 3 and parts[:2] == ('app', 'data') and parts[2] in SCOPES
            or tuple(parts) in {('app', 'agent'), ('app', 'ai-infra-guard')})


def clean_rootfs(source, destination):
    removed = []
    with tarfile.open(source, 'r|') as archive, tarfile.open(destination, 'w|', format=tarfile.PAX_FORMAT) as output:
        for member in archive:
            if excluded(member.name):
                removed.append(member.name)
                continue
            if member.islnk() and excluded(member.linkname):
                raise ValueError('规则目录之外存在指向规则的硬链接: ' + member.name)
            output.addfile(member, archive.extractfile(member) if member.isfile() else None)
    return removed


def run(*args, **kwargs):
    return subprocess.check_output(args, text=True, **kwargs).strip()


def dockerfile(config, role, revision):
    lines = ['FROM scratch', 'ADD rootfs.tar /', 'WORKDIR ' + config['WorkingDir']]
    for value in config.get('Env') or []:
        key, val = value.split('=', 1)
        lines.append('ENV ' + key + '=' + json.dumps(val))
    for key, value in (config.get('Labels') or {}).items():
        lines.append('LABEL ' + json.dumps(key) + '=' + json.dumps(value))
    lines += ['LABEL org.opencontainers.image.version="v1.1.1"',
              'LABEL org.opencontainers.image.revision=' + json.dumps(revision)]
    for port in config.get('ExposedPorts') or {}:
        lines.append('EXPOSE ' + port)
    for volume in config.get('Volumes') or {}:
        lines.append('VOLUME ' + json.dumps([volume]))
    if config.get('User'):
        lines.append('USER ' + config['User'])
    binary = 'ai-infra-guard' if role == 'webserver' else 'agent'
    lines.append('COPY --chmod=755 ' + binary + ' /app/' + binary)
    if role == 'webserver':
        lines.append('COPY --chmod=755 start_platform_web.sh /app/start_platform_web.sh')
        lines.append('CMD ["/app/start_platform_web.sh"]')
    else:
        # Docker export 的 capabilities xattr 不可靠；显式恢复原运行权限。
        # 旧镜像要求 NET_ADMIN，但原 Compose 并未授予该 bounding capability，
        # 会使 exec nmap 返回 EPERM；只恢复扫描所需且默认容器已有的 NET_RAW。
        lines.append('RUN setcap cap_net_raw+eip /usr/bin/nmap')
        if config.get('Entrypoint'):
            lines.append('ENTRYPOINT ' + json.dumps(config['Entrypoint']))
        if config.get('Cmd'):
            lines.append('CMD ' + json.dumps(config['Cmd']))
    health = config.get('Healthcheck') or {}
    if health.get('Test'):
        options = ' '.join('--' + key + '=' + str(health[field]) + 'ns'
                           for field, key in [('Interval', 'interval'), ('Timeout', 'timeout'), ('StartPeriod', 'start-period')]
                           if health.get(field))
        options += ' --retries=' + str(health.get('Retries', 3))
        command = ('CMD ' + json.dumps(health['Test'][1:]) if health['Test'][0] == 'CMD'
                   else 'CMD ' + health['Test'][1])
        lines.append('HEALTHCHECK ' + options + ' ' + command)
    return '\n'.join(lines) + '\n'


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--build-root', type=Path, required=True)
    parser.add_argument('--binaries', type=Path, required=True)
    parser.add_argument('--revision', required=True)
    args = parser.parse_args()
    import shutil
    for role, short in [('webserver', 'web'), ('agent', 'agent')]:
        image = 'ai-infra-guard-' + role + ':v1.1.0'
        info = json.loads(run('docker', 'image', 'inspect', image))[0]
        require_source(info, role)
        context = args.build_root / role
        context.mkdir(parents=True, exist_ok=True)
        original = args.build_root / (short + '-rootfs-original.tar')
        original_record = original.with_suffix('.provenance.json')
        if not original.exists():
            name = 'aig-v111-export-' + short
            run('docker', 'create', '--name', name, SOURCE_IDS[role])
            try:
                run('docker', 'export', '-o', str(original), name)
            finally:
                run('docker', 'rm', '-v', name)
            original_record.write_text(json.dumps({'image_id': SOURCE_IDS[role], 'sha256': file_hash(original)}), encoding='utf-8')
        source_hash = verify_cache(original, role, original_record)
        rootfs = context / 'rootfs.tar'
        rootfs_record = context / 'rootfs.provenance.json'
        if not rootfs.exists():
            removed = clean_rootfs(original, rootfs)
            (context / 'removed-paths.json').write_text(json.dumps(removed), encoding='utf-8')
            rootfs_record.write_text(json.dumps({'image_id': SOURCE_IDS[role], 'sha256': file_hash(rootfs), 'source_sha256': source_hash}), encoding='utf-8')
        verify_cache(rootfs, role, rootfs_record, source_hash)
        (context / 'runtime-source.json').write_text(json.dumps(info, indent=2), encoding='utf-8')
        binary = 'ai-infra-guard' if role == 'webserver' else 'agent'
        shutil.copy2(args.binaries / binary, context / binary)
        if role == 'webserver':
            shutil.copy2(Path(__file__).parent / 'start_platform_web.sh', context / 'start_platform_web.sh')
        (context / 'Dockerfile').write_text(dockerfile(info['Config'], role, args.revision), encoding='utf-8', newline='\n')
        subprocess.run(['docker', 'build', '--network=none', '--pull=false', '-t',
                        'ai-infra-guard-' + role + ':v1.1.1', str(context)], check=True)


if __name__ == '__main__':
    main()
