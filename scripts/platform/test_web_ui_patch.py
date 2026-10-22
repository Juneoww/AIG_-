#!/usr/bin/env python3
"""功能：验证 Web UI 补丁的归档边界、只读预检和保留写入的恢复。
实现：真实临时文件和SQLite；Docker命令由独立隔离fixture验证。
输入：同目录安装器；输出：unittest结果；依赖：Python标准库。
"""
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from contextlib import ExitStack
from unittest.mock import patch


class WebPatchTest(unittest.TestCase):
    def setUp(self):
        source = Path(__file__).with_name('web_ui_patch.py')
        self.assertTrue(source.exists(), '必须实现独立Web UI补丁安装器')
        spec = importlib.util.spec_from_file_location('web_ui_patch', source)
        self.u = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.u)
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)

    def bundle(self, extra=None, mutate=None):
        payload = {'payload/images/web.tar': b'image', 'tools/web_ui_patch.py': b'helper',
                   'tools/platform_upgrade.py': b'library', 'LICENSE': b'license', 'NOTICE': b'notice',
                   'OPERATIONS.md': b'ops', 'RELEASE-NOTES.md': b'notes'}
        payload.update(extra or {})
        manifest = {'format': 'aig-web-ui-patch-v1', 'version': self.u.VERSION,
                    'source_web': self.u.SOURCE_WEB, 'source_agent': self.u.SOURCE_AGENT,
                    'source_commit': 'a' * 40, 'image_source_commit': 'b' * 40,
                    'architecture': 'linux/amd64', 'contains_infrastructure_rules': False,
                    'migration': {'required': False}, 'web_image': {'tag': self.u.TARGET_TAG,
                    'id': 'sha256:' + 'c' * 64, 'archive': 'payload/images/web.tar'},
                    'tools': {}, 'files': [{'path': n, 'size': len(b), 'sha256': hashlib.sha256(b).hexdigest()}
                                          for n, b in sorted(payload.items())]}
        if mutate:
            mutate(manifest)
        payload['manifest.json'] = json.dumps(manifest).encode()
        payload['SHA256SUMS.txt'] = ''.join(e['sha256'] + '  ' + e['path'] + '\n' for e in manifest['files']).encode()
        path = self.root / 'bundle.tgz'
        with tarfile.open(path, 'w:gz') as archive:
            for name, body in payload.items():
                info = tarfile.TarInfo(name)
                info.size = len(body)
                archive.addfile(info, io.BytesIO(body))
        return path

    def test_bundle_validation_writes_nothing(self):
        bundle = self.bundle()
        before = list(self.root.iterdir())
        self.assertEqual(self.u.validate_bundle(bundle)['version'], self.u.VERSION)
        self.assertEqual(before, list(self.root.iterdir()))

    def test_packager_records_exact_provenance_and_refuses_overwrite(self):
        source = Path(__file__).with_name('package_web_ui_patch.py')
        self.assertTrue(source.exists(), '必须实现独立Web补丁打包器')
        import sys
        with patch.dict(sys.modules, {'web_ui_patch': self.u}):
            spec = importlib.util.spec_from_file_location('package_web_ui_patch', source)
            package = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(package)
        image = self.root / 'web.tar'; image.write_bytes(b'image')
        args = package.parser().parse_args(['--web-image', str(image), '--web-id', 'sha256:' + 'd' * 64,
                                           '--source-commit', 'a' * 40, '--image-source-commit', 'b' * 40,
                                           '--output-dir', str(self.root / 'out')])
        bundle, entry = package.build(args)
        manifest = self.u.validate_bundle(bundle)
        self.assertEqual(manifest['image_source_commit'], 'b' * 40)
        self.assertIn(self.u.p.sha256(bundle), entry.read_text(encoding='utf-8'))
        self.assertIn('platform_upgrade', entry.read_text(encoding='utf-8'))
        with self.assertRaises(ValueError):
            package.build(args)

    def test_bundle_rejects_unlisted_or_unknown_payload(self):
        for name in ('data/x', 'rules.tar', 'tools/unknown.py', '../escape', '/absolute'):
            with self.subTest(name=name), self.assertRaises(ValueError):
                self.u.validate_bundle(self.bundle({name: b'danger'}))

    def test_bundle_rejects_digest_or_migration_change(self):
        for mutate in (lambda m: m['files'][0].update(sha256='0' * 64),
                       lambda m: m.update(migration={'required': True}),
                       lambda m: m.update(source_agent='wrong')):
            with self.assertRaises(ValueError):
                self.u.validate_bundle(self.bundle(mutate=mutate))

    def test_image_edit_preserves_every_other_field(self):
        path = self.root / self.u.p.COMPOSE_NAMES[0]
        original = {'services': {'webserver': {'image': 'old', 'ports': ['127.0.0.1:18088:8088'],
                    'environment': {'SECRET': 'retained'}, 'networks': ['custom']}, 'agent': {'image': 'agent'}}}
        path.write_text(json.dumps(original))
        self.u.write_web_image(path, 'new')
        original['services']['webserver']['image'] = 'new'
        self.assertEqual(json.loads(path.read_text()), original)

    def test_restore_configuration_never_restores_business_files(self):
        live, backup = self.root / 'live', self.root / 'backup'
        live.mkdir(); backup.mkdir(); (backup / 'configuration').mkdir()
        for name in self.u.p.COMPOSE_NAMES:
            (live / name).write_text('new config')
            (backup / 'configuration' / name).write_text('old config')
        (live / 'PLATFORM-DEPLOYED.json').write_text('new marker')
        (backup / 'configuration/PLATFORM-DEPLOYED.json').write_text('old marker')
        (live / 'db').mkdir(); (live / 'db/tasks.db').write_text('new business rows')
        with patch.object(self.u, 'ROOT', live):
            self.u.restore_configuration(backup)
        self.assertEqual((live / 'db/tasks.db').read_text(), 'new business rows')
        self.assertEqual((live / self.u.p.COMPOSE_NAMES[0]).read_text(), 'old config')

    def test_opening_access_is_durable_before_firewall_changes(self):
        class Gate:
            def end(inner):
                record = json.loads((self.root / 'STATE.json').read_text())
                self.assertTrue(record['access_may_be_public'])
        state = {}
        self.u.p.begin_access_opening(Gate(), state, 'web', self.root / 'STATE.json')

    def test_failed_maintenance_begin_keeps_source_and_cleans_partial_gate(self):
        class Gate:
            ended = False
            def begin(inner):
                raise RuntimeError('firewall failure')
            def end(inner):
                inner.ended = True
        gate = Gate()
        with self.assertRaisesRegex(RuntimeError, 'firewall failure'):
            self.u.enter_maintenance(gate)
        self.assertTrue(gate.ended)

    def test_checker_backup_digest_matches_volume_digest_contract(self):
        (self.root / 'file').write_bytes(b'checker data')
        value = self.u.checker_inventory(self.root)
        self.assertIn('.', value)
        self.assertEqual(value['file']['sha256'], hashlib.sha256(b'checker data').hexdigest())
        self.assertNotIn('kind', value['file'])
        self.assertNotIn('size', value['file'])

    def test_web_runtime_rejects_unexpected_port_environment_and_network_changes(self):
        original = {'HostConfig': {'PortBindings': {'8088/tcp': [{'HostIp': '127.0.0.1', 'HostPort': '18088'}]}},
                    'Config': {'Env': ['A=B'], 'Cmd': ['webserver'], 'Entrypoint': None, 'User': '', 'WorkingDir': '/app'},
                    'Mounts': [], 'NetworkSettings': {'Networks': {'custom': {'NetworkID': 'id'}}}}
        import copy
        for field in ('port', 'env', 'network'):
            changed = copy.deepcopy(original)
            if field == 'port':
                changed['HostConfig']['PortBindings']['8088/tcp'][0]['HostPort'] = '8088'
            elif field == 'env':
                changed['Config']['Env'] = ['A=lost']
            else:
                changed['NetworkSettings']['Networks']['custom']['NetworkID'] = 'other'
            with self.subTest(field=field), self.assertRaises(ValueError):
                self.u.assert_web_runtime(original, changed)

    def test_compose_null_startup_inherits_image_but_empty_list_clears_it(self):
        self.assertTrue(hasattr(self.u, 'verify_startup'), '需要正确解析Compose null与清空语义')
        defaults = {'Entrypoint': ['/python', '/app/service.py'], 'Cmd': ['serve'], 'User': '', 'WorkingDir': '/app'}
        actual = {'Config': dict(defaults)}
        image = {'Config': dict(defaults)}
        self.u.verify_startup({'entrypoint': None, 'command': None}, actual, image)
        with self.assertRaises(ValueError):
            self.u.verify_startup({'entrypoint': []}, actual, image)
        self.u.verify_startup({'entrypoint': []}, {'Config': dict(defaults, Entrypoint=None, Cmd=None)}, image)
        with self.assertRaises(ValueError):
            self.u.verify_startup({'entrypoint': None}, {'Config': dict(defaults, Entrypoint=['drift'])}, image)

    def test_check_does_not_take_lock_extract_or_import_images(self):
        bundle = self.bundle()
        layout = {'web': {'Id': 'web'}, 'agent': {'Id': 'agent'}}
        before = list(self.root.iterdir())
        with patch.object(self.u, 'preflight'), patch.object(self.u, 'discover', return_value=layout), \
             patch.object(self.u, 'check_site'), patch.object(self.u.p, 'lock_all', side_effect=AssertionError('lock')):
            self.u.main(['--bundle', str(bundle), '--check'])
        self.assertEqual(before, list(self.root.iterdir()))

    def upgrade_fixture(self, stack, opening_failure=False, acceptance_failure=False):
        live = self.root / 'live'; live.mkdir()
        for name in self.u.p.COMPOSE_NAMES:
            (live / name).write_text(json.dumps({'services': {'webserver': {'image': 'source'}, 'agent': {}}}))
        (live / 'db').mkdir(); (live / 'db/tasks.db').write_text('preserve all new rows')
        web = {'Id': 'source-id', 'Image': self.u.SOURCE_WEB, 'Config': {'Image': 'source'}}
        agent = {'Id': 'agent-id', 'Image': self.u.SOURCE_AGENT}
        layout = {'web': web, 'agent': agent, 'marker': {'new_web': web, 'new_agent': agent},
                  'agent_identity': {}, 'config_sha256': {}, 'files': [str(live / n) for n in self.u.p.COMPOSE_NAMES]}
        (live / 'PLATFORM-DEPLOYED.json').write_text(json.dumps(layout['marker']))
        target = {'Id': 'target-id', 'Image': 'sha256:' + 'c' * 64}
        restored = {'Id': 'restored-id', 'Image': self.u.SOURCE_WEB}
        events = []
        class Gate:
            def __init__(inner, *args):
                pass
            def begin(inner):
                events.append('maintenance')
            def end(inner):
                events.append('opening')
                if opening_failure:
                    raise RuntimeError('opening firewall failed')
        stack.enter_context(patch.object(self.u, 'ROOT', live))
        stack.enter_context(patch.object(self.u, 'BASE', self.root))
        stack.enter_context(patch.object(self.u, 'discover', return_value=layout))
        stack.enter_context(patch.object(self.u, 'check_site', return_value={'sessions': {'schema': 'same'}}))
        stack.enter_context(patch.object(self.u.p, 'lock_all', return_value=[]))
        stack.enter_context(patch.object(self.u.p, 'current_rules', return_value={'rule': 'hash'}))
        stack.enter_context(patch.object(self.u.p, 'knowledge', return_value={'knowledge': 'same'}))
        stack.enter_context(patch.object(self.u.p, 'database_snapshot', return_value={'sessions': {'schema': 'same'}}))
        stack.enter_context(patch.object(self.u.p, 'Gate', Gate))
        stack.enter_context(patch.object(self.u.p, 'run', side_effect=lambda args, **kwargs: events.append(args)))
        stack.enter_context(patch.object(self.u.p, 'docker_json', return_value=[{'Id': target['Image']}]))
        def backup(work, _):
            config = work / 'configuration'; config.mkdir()
            import shutil
            for name in (*self.u.p.COMPOSE_NAMES, 'PLATFORM-DEPLOYED.json'):
                shutil.copy2(live / name, config / name)
        stack.enter_context(patch.object(self.u, 'cold_backup', side_effect=backup))
        stack.enter_context(patch.object(self.u, 'up_web', side_effect=lambda _: events.append('up-web')))
        acceptance = [RuntimeError('candidate startup failed'), restored] if acceptance_failure else [target]
        stack.enter_context(patch.object(self.u, 'accept', side_effect=acceptance))
        return live, events

    def test_candidate_failure_recovers_only_web_and_preserves_business(self):
        bundle = self.bundle(); manifest = self.u.validate_bundle(bundle)
        with ExitStack() as stack:
            live, events = self.upgrade_fixture(stack, acceptance_failure=True)
            with self.assertRaisesRegex(RuntimeError, 'candidate startup failed'):
                self.u.upgrade(bundle, manifest)
            self.assertEqual((live / 'db/tasks.db').read_text(), 'preserve all new rows')
            self.assertEqual(json.loads((live / self.u.p.COMPOSE_NAMES[0]).read_text())['services']['webserver']['image'], 'source')
            self.assertEqual(json.loads((live / 'PLATFORM-DEPLOYED.json').read_text())['new_web']['Id'], 'restored-id')
            state = json.loads(next(self.root.glob('backups/*/STATE.json')).read_text(encoding='utf-8'))
            self.assertEqual(state['recovery'], 'source-web-restored-business-preserved')
            self.assertEqual(events.count('up-web'), 2)
            self.assertFalse(any(isinstance(e, list) and 'agent-id' in e for e in events))

    def test_opening_failure_preserves_accepted_web_and_current_writes(self):
        bundle = self.bundle(); manifest = self.u.validate_bundle(bundle)
        with ExitStack() as stack:
            live, events = self.upgrade_fixture(stack, opening_failure=True)
            with self.assertRaisesRegex(RuntimeError, 'opening firewall failed'):
                self.u.upgrade(bundle, manifest)
            self.assertEqual((live / 'db/tasks.db').read_text(), 'preserve all new rows')
            self.assertEqual(json.loads((live / self.u.p.COMPOSE_NAMES[0]).read_text())['services']['webserver']['image'], self.u.TARGET_TAG)
            state = json.loads(next(self.root.glob('backups/*/STATE.json')).read_text(encoding='utf-8'))
            self.assertTrue(state['access_may_be_public'])
            self.assertEqual(state['recovery'], 'current-web-and-business-preserved')
            self.assertEqual(events.count('up-web'), 1)

    def rollback_fixture(self, stack):
        bundle = self.bundle(); manifest = self.u.validate_bundle(bundle)
        live, events = self.upgrade_fixture(stack)
        backup = self.u.upgrade(bundle, manifest)
        state = json.loads((backup / 'STATE.json').read_text(encoding='utf-8'))
        current = dict(state['layout'], web=state['new_web'], config_sha256=state['config_after'],
                       marker=json.loads((live / 'PLATFORM-DEPLOYED.json').read_text(encoding='utf-8')))
        stack.enter_context(patch.object(self.u, 'discover', return_value=current))
        stack.enter_context(patch.object(self.u.p, 'docker_json', return_value=[{'Id': self.u.SOURCE_WEB}]))
        stack.enter_context(patch.object(self.u, 'accept', return_value={'Id': 'rolled-back-web', 'Image': self.u.SOURCE_WEB}))
        (live / 'db/tasks.db').write_text('new rows after successful patch')
        events.clear()
        return bundle, manifest, backup, live, events

    def test_active_rollback_keeps_post_patch_business_writes(self):
        with ExitStack() as stack:
            bundle, manifest, backup, live, events = self.rollback_fixture(stack)
            self.u.rollback(backup, manifest, bundle)
            self.assertEqual((live / 'db/tasks.db').read_text(), 'new rows after successful patch')
            self.assertEqual(json.loads((live / self.u.p.COMPOSE_NAMES[0]).read_text())['services']['webserver']['image'], 'source')
            self.assertEqual(events.count('up-web'), 1)
            marker = json.loads((live / 'PLATFORM-DEPLOYED.json').read_text(encoding='utf-8'))
            self.assertTrue(marker['web_ui_patch']['rolled_back'])
            self.assertEqual(marker['new_web']['Id'], 'rolled-back-web')

    def test_rollback_pre_stop_failure_validates_source_then_exits_maintenance(self):
        with ExitStack() as stack:
            bundle, manifest, backup, live, events = self.rollback_fixture(stack)
            stack.enter_context(patch.object(self.u.p, 'knowledge', side_effect=[RuntimeError('knowledge temporarily down'), {'same': 'knowledge'}]))
            with self.assertRaisesRegex(RuntimeError, 'knowledge temporarily down'):
                self.u.rollback(backup, manifest, bundle)
            self.assertIn('maintenance', events)
            self.assertIn('opening', events)
            self.assertEqual(events.count('up-web'), 0)
            self.assertEqual((live / 'db/tasks.db').read_text(), 'new rows after successful patch')

    def test_failed_rollback_recovery_updates_receipt_and_allows_retry(self):
        with ExitStack() as stack:
            bundle, manifest, backup, live, events = self.rollback_fixture(stack)
            patched = {'Id': 'recreated-patched-id', 'Image': manifest['web_image']['id']}
            stack.enter_context(patch.object(self.u, 'accept', side_effect=[RuntimeError('old Web cannot start'), patched]))
            with self.assertRaisesRegex(RuntimeError, 'old Web cannot start'):
                self.u.rollback(backup, manifest, bundle)
            state = json.loads((backup / 'STATE.json').read_text(encoding='utf-8'))
            self.assertEqual(state['new_web']['Id'], 'recreated-patched-id')
            self.assertEqual(state['web_identity_history'][-1]['previous_web_id'], 'target-id')
            current = dict(state['layout'], web=patched, config_sha256=state['config_after'],
                           marker=json.loads((live / 'PLATFORM-DEPLOYED.json').read_text(encoding='utf-8')))
            stack.enter_context(patch.object(self.u, 'discover', return_value=current))
            stack.enter_context(patch.object(self.u, 'accept', return_value={'Id': 'successful-retry', 'Image': self.u.SOURCE_WEB}))
            self.u.rollback(backup, manifest, bundle)
            self.assertEqual((live / 'db/tasks.db').read_text(), 'new rows after successful patch')

    def test_stop_error_after_actual_stop_restarts_source_before_opening_gate(self):
        bundle = self.bundle(); manifest = self.u.validate_bundle(bundle)
        with ExitStack() as stack:
            live, events = self.upgrade_fixture(stack)
            def run(args, **kwargs):
                events.append(args)
                if args[:2] == ['docker', 'stop']:
                    raise RuntimeError('stop response lost after source stopped')
            stack.enter_context(patch.object(self.u.p, 'run', side_effect=run))
            stack.enter_context(patch.object(self.u.p, 'inspect', return_value={'Id': 'source-id', 'Image': self.u.SOURCE_WEB, 'State': {'Running': False}}))
            with self.assertRaisesRegex(RuntimeError, 'stop response lost'):
                self.u.upgrade(bundle, manifest)
            self.assertIn(['docker', 'start', 'source-id'], events)
            self.assertLess(events.index(['docker', 'start', 'source-id']), events.index('opening'))
            self.assertEqual((live / 'db/tasks.db').read_text(), 'preserve all new rows')

    def test_rollback_stop_error_restarts_patched_web_without_recreating_agent(self):
        with ExitStack() as stack:
            bundle, manifest, backup, live, events = self.rollback_fixture(stack)
            patched = {'Id': 'target-id', 'Image': manifest['web_image']['id'], 'State': {'Running': False}}
            def run(args, **kwargs):
                events.append(args)
                if args[:2] == ['docker', 'stop']:
                    raise RuntimeError('rollback stop response lost')
            stack.enter_context(patch.object(self.u.p, 'run', side_effect=run))
            stack.enter_context(patch.object(self.u.p, 'inspect', return_value=patched))
            stack.enter_context(patch.object(self.u, 'accept', return_value=patched))
            with self.assertRaisesRegex(RuntimeError, 'rollback stop response lost'):
                self.u.rollback(backup, manifest, bundle)
            self.assertIn(['docker', 'start', 'target-id'], events)
            self.assertLess(events.index(['docker', 'start', 'target-id']), events.index('opening'))
            self.assertEqual(events.count('up-web'), 0)
            self.assertEqual((live / 'db/tasks.db').read_text(), 'new rows after successful patch')

    def test_stop_error_and_uninspectable_state_keeps_maintenance(self):
        bundle = self.bundle(); manifest = self.u.validate_bundle(bundle)
        with ExitStack() as stack:
            live, events = self.upgrade_fixture(stack)
            def run(args, **kwargs):
                events.append(args)
                if args[:2] == ['docker', 'stop']:
                    raise RuntimeError('stop response lost')
            stack.enter_context(patch.object(self.u.p, 'run', side_effect=run))
            stack.enter_context(patch.object(self.u.p, 'inspect', side_effect=RuntimeError('daemon unavailable')))
            with self.assertRaisesRegex(RuntimeError, 'stop response lost'):
                self.u.upgrade(bundle, manifest)
            self.assertNotIn('opening', events)
            state = json.loads(next(self.root.glob('backups/*/STATE.json')).read_text(encoding='utf-8'))
            self.assertEqual(state['recovery'], 'manual-required-maintenance-preserved')

    def test_automatic_recovery_persists_access_marker_before_reopening(self):
        bundle = self.bundle(); manifest = self.u.validate_bundle(bundle)
        with ExitStack() as stack:
            live, events = self.upgrade_fixture(stack, acceptance_failure=True)
            class Gate:
                def __init__(inner, layout, work):
                    inner.work = work
                def begin(inner):
                    pass
                def end(inner):
                    record = json.loads((inner.work / 'STATE.json').read_text(encoding='utf-8'))
                    self.assertTrue(record['access_may_be_public'], '恢复开放入口前也必须持久化访问标记')
            stack.enter_context(patch.object(self.u.p, 'Gate', Gate))
            with self.assertRaisesRegex(RuntimeError, 'candidate startup failed'):
                self.u.upgrade(bundle, manifest)
            state = json.loads(next(self.root.glob('backups/*/STATE.json')).read_text(encoding='utf-8'))
            self.assertEqual(state['recovery'], 'source-web-restored-business-preserved')

    def test_preflight_requires_current_platform_only(self):
        from types import SimpleNamespace
        checked = []
        with patch.object(self.u.sys, 'platform', 'linux'), \
             patch.object(self.u.os, 'geteuid', return_value=0, create=True), \
             patch.object(self.u.os, 'uname', return_value=SimpleNamespace(machine='x86_64'), create=True), \
             patch.object(self.u.shutil, 'which', return_value='/bin/available'), \
             patch.object(self.u.p, 'docker_json', return_value={'OSType': 'linux'}), \
             patch.object(self.u.p, 'run'), \
             patch.object(self.u.p, 'real_path', side_effect=lambda path: checked.append(path)):
            self.u.preflight()
        self.assertIn(self.u.ROOT, checked)
        self.assertNotIn(self.u.p.OLD, checked)

    def test_readonly_space_check_rejects_separate_full_docker_storage(self):
        from types import SimpleNamespace
        live = self.root / 'live'; live.mkdir()
        for name in ('data', 'db', 'logs', 'uploads'):
            (live / name).mkdir()
        layout = {'web': {'Id': 'web'}, 'checker_volume': {'Mountpoint': str(self.root)}}
        manifest = self.u.validate_bundle(self.bundle())
        with patch.object(self.u, 'ROOT', live), patch.object(self.u, 'BASE', self.root), \
             patch.object(self.u.p, 'database_snapshot', return_value={}), \
             patch.object(self.u.p, 'current_rules'), patch.object(self.u.p, 'knowledge'), \
             patch.object(self.u.p, 'fetch_agent_status'), \
             patch.object(self.u.p, 'docker_json', return_value={'DockerRootDir': '/docker-store'}), \
             patch.object(self.u.shutil, 'disk_usage', side_effect=lambda path: SimpleNamespace(free=0 if str(path) == '/docker-store' else 10 ** 12)):
            with self.assertRaisesRegex(ValueError, 'Docker'):
                self.u.check_site(layout, manifest)


if __name__ == '__main__':
    unittest.main()
