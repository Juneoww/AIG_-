#!/usr/bin/env python3
"""功能：启动独占Docker-in-Docker事务验收，不触碰现存业务容器。
实现：创建带随机名称的特权测试容器，内层引擎仅载入无扫描能力fixture；最后清理自己的容器。
输入：已构建harness/runtime测试镜像；输出：--evidence下JSON/日志。
依赖：宿主Docker与Python；需要允许专用测试容器的privileged权限。
用法：python scripts/platform/docker_fixture_test.py --evidence <新目录>。
"""
import argparse
from pathlib import Path
import subprocess
import tempfile
import time
import uuid


def run(args, timeout=600):
    result = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=timeout)
    output = result.stdout.decode('utf-8', errors='replace')
    if result.returncode:
        raise RuntimeError(' '.join(args[:4]) + '\n' + output)
    return output


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--evidence', required=True)
    parser.add_argument('--adapter-root', help='仅验收：已生成的002/004兼容规则工具目录，只读挂载')
    args = parser.parse_args()
    evidence = Path(args.evidence).resolve()
    if evidence.exists():
        raise ValueError('验收目录必须为新目录，避免覆盖历史结果')
    evidence.mkdir(parents=True)
    name = 'aig-platform-fixture-' + uuid.uuid4().hex[:12]
    tools = Path(__file__).resolve().parent
    started = False
    try:
        command = ['docker', 'run', '-d', '--privileged', '--network=none', '--name', name,
                   '--mount', 'type=bind,src=' + str(tools) + ',dst=/tools,readonly']
        if args.adapter_root:
            command += ['--mount', 'type=bind,src=' + str(Path(args.adapter_root).resolve()) + ',dst=/adapters,readonly']
        run(command + ['aig-platform-upgrade-harness:v111-test'])
        started = True
        for _ in range(60):
            state = subprocess.run(['docker', 'exec', name, 'docker', 'info'], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            if state.returncode == 0:
                break
            time.sleep(1)
        else:
            raise RuntimeError('内层Docker启动失败')
        with tempfile.TemporaryDirectory(prefix='aig-platform-fixture-') as temporary:
            image_file = Path(temporary) / 'seed.tar'
            run(['docker', 'save', '-o', str(image_file), 'aig-platform-upgrade-fixture:seed'])
            run(['docker', 'cp', str(image_file), name + ':/seed.tar'])
            run(['docker', 'exec', name, 'docker', 'load', '-i', '/seed.tar'])
        try:
            output = run(['docker', 'exec', name, 'python3', '/tools/fixture/run_inside.py'], timeout=1200)
        except Exception as error:
            (evidence / 'fixture.log').write_text(str(error), encoding='utf-8')
            run(['docker', 'exec', name, 'python3', '-c', "from pathlib import Path;import shutil;d=Path('/evidence');d.mkdir(exist_ok=True);[(shutil.copyfile(p,d/(p.parent.name+'-STATE.json'))) for p in Path('/opt/aig/backups').glob('*/STATE.json')]"])
            run(['docker', 'cp', name + ':/evidence/.', str(evidence)])
            raise
        (evidence / 'fixture.log').write_text(output, encoding='utf-8')
        run(['docker', 'cp', name + ':/evidence/.', str(evidence)])
        print(output)
    finally:
        if started:
            (evidence / 'engine.log').write_text(run(['docker', 'logs', name]), encoding='utf-8')
            # 唯一随机容器由本脚本创建；-v只删除其匿名测试卷，不使用全局清理命令。
            run(['docker', 'rm', '-f', '-v', name])


if __name__ == '__main__':
    main()
