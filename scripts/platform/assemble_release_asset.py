#!/usr/bin/env python3
"""功能：在 GitHub 内重组并核验本次草稿的离线包，避免慢速单连接上传。
实现：只读取固定清单中的分段，逐段及整包验证大小与 SHA256，上传完整包后清理本次分段。
输入：同目录固定清单及 GitHub Actions 仓库令牌；输出：草稿完整附件，不公开 Release。
依赖：Python 标准库与预装 gh；不修改程序、版本标签、其他 Release 或规则资源。
"""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time


def gh(*args, **kwargs):
    return subprocess.check_output(['gh', *args], **kwargs)


def digest(path):
    result = hashlib.sha256()
    with path.open('rb') as stream:
        for chunk in iter(lambda: stream.read(4 * 1024 * 1024), b''):
            result.update(chunk)
    return result.hexdigest()


def main():
    manifest = json.loads(Path(__file__).with_name('release-transfer-manifest.json').read_text())
    repo, tag = manifest['repository'], manifest['tag']
    assert os.environ['GITHUB_REPOSITORY'] == repo and tag == 'v1.1.1'
    assert gh('api', 'repos/' + repo + '/commits/' + tag, '--jq', '.sha').decode().strip() == manifest['commit']
    names = {p['name'] for p in manifest['parts']}
    assert len(names) == 16 and all(n.startswith('v111-offline-transfer-20261021-') and n.endswith('.part') for n in names)
    deadline = time.monotonic() + 30 * 60
    while True:
        release = json.loads(gh('release', 'view', tag, '--repo', repo, '--json', 'isDraft,assets,tagName'))
        assert release['isDraft'] and release['tagName'] == tag, 'Only owned draft is accepted'
        assets = {a['name']: a for a in release['assets'] if a['state'] == 'uploaded'}
        ready = len(names.intersection(assets))
        print('Uploaded transfer segments: %d/16' % ready, flush=True)
        if names.issubset(assets):
            break
        if time.monotonic() >= deadline:
            raise TimeoutError('Transfer segments not complete; draft preserved')
        time.sleep(30)
    with tempfile.TemporaryDirectory() as work:
        package = Path(work) / manifest['filename']
        with package.open('xb') as output:
            for part in manifest['parts']:
                asset = assets[part['name']]
                assert asset['size'] == part['size'] and asset['digest'] == 'sha256:' + part['sha256']
                path = Path(work) / part['name']
                with path.open('xb') as target:
                    subprocess.run(['gh', 'api', asset['apiUrl'], '-H', 'Accept: application/octet-stream'], stdout=target, check=True)
                assert path.stat().st_size == part['size'] and digest(path) == part['sha256']
                with path.open('rb') as source:
                    for block in iter(lambda: source.read(4 * 1024 * 1024), b''):
                        output.write(block)
                path.unlink()
        assert package.stat().st_size == manifest['size'] and digest(package) == manifest['sha256']
        # 只有验证草稿、标签、每段和整包后，才写入本次最终附件。
        gh('release', 'upload', tag, str(package), '--repo', repo, '--clobber')
        result = json.loads(gh('release', 'view', tag, '--repo', repo, '--json', 'isDraft,assets'))
        assert result['isDraft']
        final = next(a for a in result['assets'] if a['name'] == manifest['filename'])
        assert final['state'] == 'uploaded' and final['size'] == manifest['size'] and final['digest'] == 'sha256:' + manifest['sha256']
    for name in sorted(names):
        gh('api', '--method', 'DELETE', assets[name]['apiUrl'])
    print('Verified complete package uploaded; temporary segments removed; release remains draft', flush=True)


if __name__ == '__main__':
    main()
