#!/usr/bin/env python3
"""功能：为可信的历史规则交付生成 v1.1.1 双服务平台兼容管理入口。
实现：核验固定来源摘要，只适配管理代码的程序路径/版本；内层规则和其余负载逐字保持。
输入：原配套脚本和资源包；输出：独立新目录内的兼容脚本、资源包及变更记录。
依赖：Python3.8+ 标准库；不连接 Docker，不执行规则更新，不修改原交付或当前规则。
用法：python prepare_rules_v111.py --script 原脚本 --resource 原包 --output 新目录。
"""
import argparse
import hashlib
import io
import json
from pathlib import Path, PurePosixPath
import re
import tarfile

TRUSTED = {
    'rules-2026.10.10-002': ('d92ae88627b779b2c5879143128f07d600219037ceaa70819e2cb324ee0e8942',
                           'd69828701fb994cb253ae9f44b035531f7deecfed7d65affd70306a6d8c4c1eb'),
    'rules-2026.10.19-004': ('4f9a058519c25ca056677fc75f9bd9c228edd6fd947ea5722a376f131849114b',
                           'd2d43d8b1cbe8d7cfd78cd645719546748639752ee86dcf7dd20eb235c02f32e'),
}


def digest(data):
    return hashlib.sha256(data).hexdigest()


def replace_one(text, old, new):
    if text.count(old) != 1:
        raise ValueError('来源代码契约不匹配: ' + old)
    return text.replace(old, new)


def adapt_layout(text):
    text = replace_one(text, "WORKING_DIR = BASE + '/v1.1.0'", "WORKING_DIR = BASE + '/v1.1.1'")
    # UI 补丁不改变规则引擎/业务目录；保留 v1.1.1 管理入口对已验证补丁镜像的支持。
    text = replace_one(text, "('v1.1.0', '1.1.0')", "('v1.1.1', '1.1.1', 'v1.1.1-ui1')")
    return replace_one(text, '契约 1.0 仅支持程序基线 v1.1.0', '契约 1.0 此管理入口仅支持程序基线 v1.1.1')


def prepare(script, resource, output):
    script, resource, output = Path(script), Path(resource), Path(output)
    script_data, resource_data = script.read_bytes(), resource.read_bytes()
    version = next((v for v, hashes in TRUSTED.items()
                    if (digest(script_data), digest(resource_data)) == hashes), None)
    if not version:
        raise ValueError('来源不在已验证的规则交付白名单；拒绝未知或被修改的脚本/资源包')
    if output.exists():
        raise ValueError('输出目录已存在，禁止覆盖历史交付')
    with tarfile.open(fileobj=io.BytesIO(resource_data), mode='r:gz') as archive:
        members = archive.getmembers()
        names = [m.name for m in members]
        if len(names) != len(set(names)):
            raise ValueError('重复归档路径')
        entries = {}
        metadata = {}
        for member in members:
            path = PurePosixPath(member.name)
            if not member.isfile() or path.is_absolute() or '..' in path.parts or '\\' in member.name:
                raise ValueError('非法归档成员')
            entries[member.name] = archive.extractfile(member).read()
            metadata[member.name] = member
    original = {name: digest(data) for name, data in entries.items()}
    entries['layout.py'] = adapt_layout(entries['layout.py'].decode()).encode()
    entries['business.py'] = replace_one(entries['business.py'].decode(),
                                         "startswith('/opt/aig/v1.1.0/')", "startswith('/opt/aig/v1.1.1/')").encode()
    for filename in ('layout.py', 'business.py'):
        compile(entries[filename], filename, 'exec')
    buffer = io.BytesIO()
    with tarfile.open(fileobj=buffer, mode='w:gz', format=tarfile.PAX_FORMAT) as archive:
        for name in names:
            member = metadata[name]
            member.size = len(entries[name])
            archive.addfile(member, io.BytesIO(entries[name]))
    new_resource = buffer.getvalue()
    name = version + '-platform-v1.1.1-intranet.tar.gz'
    text = script_data.decode()
    text = replace_one(text, 'PROGRAM=/opt/aig/v1.1.0', 'PROGRAM=/opt/aig/v1.1.1')
    text = replace_one(text, "EXPECTED_SHA='" + TRUSTED[version][1] + "'", "EXPECTED_SHA='" + digest(new_resource) + "'")
    text = replace_one(text, 'PACKAGE="$SCRIPT_DIR/$VERSION-intranet.tar.gz"', 'PACKAGE="$SCRIPT_DIR/' + name + '"')
    text = text.replace('已有v1.1.0镜像', '已有v1.1.1镜像')
    if '.upgrade-v1.1.0.lock' not in text:
        raise ValueError('锁协议缺失')
    # 在全部来源及变更校验完成后才创建独立输出，不覆盖原规则交付。
    output.mkdir(parents=True)
    (output / name).write_bytes(new_resource)
    new_script = output / ('update-' + version + '-platform-v1.1.1.sh')
    new_script.write_bytes(text.encode())
    new_script.chmod(0o755)
    record = {'platform_version': 'v1.1.1', 'rule_version': version,
              'source_script_sha256': digest(script_data), 'source_resource_sha256': digest(resource_data),
              'output_script_sha256': digest(text.encode()), 'output_resource_sha256': digest(new_resource),
              'inner_rules_sha256': original['rules.tar.gz'], 'changed_files': ['layout.py', 'business.py'],
              'preserved_payloads': {k: v for k, v in original.items() if k not in ('layout.py', 'business.py')}}
    if any(digest(entries[k]) != v for k, v in record['preserved_payloads'].items()):
        raise ValueError('非管理代码负载被改变')
    (output / 'ADAPTER-RECORD.json').write_text(json.dumps(record, ensure_ascii=False, indent=2), encoding='utf-8')
    return record


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--script', type=Path, required=True)
    parser.add_argument('--resource', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    record = prepare(args.script, args.resource, args.output)
    print('兼容管理交付已生成；规则快照保持不变: ' + record['inner_rules_sha256'])


if __name__ == '__main__':
    main()
