#!/usr/bin/env python3
"""功能：验证平台升级的包边界、数据继承和失败关闭行为。
实现：使用真实归档、SQLite 和临时目录；Docker 生命周期另由隔离集成测试覆盖。
输入：同目录 platform_upgrade.py；输出：unittest 结果；依赖：Python 标准库。
"""
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import sqlite3
import tarfile
import tempfile
import unittest
import contextlib
import inspect
from unittest.mock import patch


class PlatformTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.module_path = Path(__file__).with_name('platform_upgrade.py')

    def setUp(self):
        self.assertTrue(self.module_path.exists(), 'platform_upgrade.py must implement delivery validation')
        spec = importlib.util.spec_from_file_location('platform_upgrade', self.module_path)
        self.u = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.u)
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)

    def package(self, extra=None, mutate=None):
        payload = {'tools/platform_upgrade.py': b'# harmless helper\n', 'tools/rules-compatible/adapter.py': b'# harmless adapter\n', 'images/runtime.tar.gz': b'image',
                   'LICENSE': b'license', 'NOTICE': b'notice', 'RELEASE-NOTES.md': b'release', 'OPERATIONS.md': b'operations'}
        if extra:
            payload.update(extra)
        manifest = {'type': 'platform', 'version': 'v1.1.1', 'source_versions': ['v1.1.0'],
                    'architecture': 'linux/amd64', 'contract_version': '1.0', 'source_commit': 'a' * 40,
                    'contains_infrastructure_rules': False, 'migration': {'required': False},
                    'minimum_free_bytes': 1024, 'rules_compatibility': {'engine_baseline': 'c2c18547cef9276c376f86057a5387b7e61bfa62', 'validated': True},
                    'rule_tools_compatibility': {'version': 'v1.1.1', 'layout': 'legacy-two-service', 'validated': True},
                    'images': {s: {'reference': 'aig-' + s + ':v1.1.1', 'id': 'sha256:' + 'b' * 64} for s in ('webserver', 'agent')},
                    'source_images': self.u.SOURCE_IMAGES,
                    'files': [{'path': name, 'size': len(body), 'sha256': hashlib.sha256(body).hexdigest()} for name, body in sorted(payload.items())]}
        if mutate:
            mutate(manifest)
        payload['SHA256SUMS.txt'] = ''.join(e['sha256'] + '  ' + e['path'] + '\n' for e in manifest['files']).encode()
        payload['manifest.json'] = json.dumps(manifest).encode()
        path = self.root / 'package.tar.gz'
        with tarfile.open(path, 'w:gz') as archive:
            for name, body in payload.items():
                info = tarfile.TarInfo(name)
                info.size = len(body)
                archive.addfile(info, io.BytesIO(body))
        return path

    def test_valid_bundle_is_accepted_without_extraction(self):
        path = self.package()
        before = sorted(p.name for p in self.root.iterdir())
        self.assertEqual(self.u.validate_bundle(path)['version'], 'v1.1.1')
        self.assertEqual(before, sorted(p.name for p in self.root.iterdir()))

    def test_path_traversal_rejected(self):
        with self.assertRaisesRegex(ValueError, '路径'):
            self.u.validate_bundle(self.package({'../escape': b'x'}))

    def test_rules_rejected(self):
        with self.assertRaisesRegex(ValueError, '规则'):
            self.u.validate_bundle(self.package({'data/fingerprints/x.yaml': b'x'}))

    def test_rules_disguised_archive_rejected(self):
        with self.assertRaisesRegex(ValueError, '规则|负载'):
            self.u.validate_bundle(self.package({'tools/snapshot.tar.gz': b'x'}))

    def test_manifest_size_mismatch_rejected(self):
        with self.assertRaisesRegex(ValueError, '大小|摘要'):
            self.u.validate_bundle(self.package(mutate=lambda m: m['files'][0].update(size=999)))

    def test_unknown_architecture_rejected(self):
        with self.assertRaisesRegex(ValueError, '架构'):
            self.u.validate_bundle(self.package(mutate=lambda m: m.update(architecture='linux/arm64')))

    def test_unverified_rules_adapter_rejected(self):
        with self.assertRaisesRegex(ValueError, '规则工具'):
            self.u.validate_bundle(self.package(mutate=lambda m: m['rule_tools_compatibility'].update(validated=False)))

    def test_duplicate_tar_member_rejected(self):
        path = self.package()
        with tarfile.open(path, 'r:gz') as source:
            members = [(m, source.extractfile(m).read()) for m in source]
        with tarfile.open(path, 'w:gz') as out:
            for m, body in members + [members[0]]:
                out.addfile(m, io.BytesIO(body))
        with self.assertRaisesRegex(ValueError, '重复'):
            self.u.validate_bundle(path)

    def test_symlink_tar_member_rejected(self):
        path = self.package()
        with tarfile.open(path, 'r:gz') as source:
            members = [(m, source.extractfile(m).read()) for m in source]
        with tarfile.open(path, 'w:gz') as out:
            for m, body in members:
                if m.name == 'LICENSE':
                    m.type, m.linkname, m.size = tarfile.SYMTYPE, '/etc/passwd', 0
                    out.addfile(m)
                else:
                    out.addfile(m, io.BytesIO(body))
        with self.assertRaisesRegex(ValueError, '链接|普通文件'):
            self.u.validate_bundle(path)

    def database(self, status):
        path = self.root / 'tasks.db'
        db = sqlite3.connect(path)
        db.execute('create table sessions(id text, status text)')
        db.execute('insert into sessions values (?,?)', ('keep-history', status))
        db.commit()
        db.close()
        return path

    def test_running_tasks_rejected_without_modification(self):
        path = self.database('doing')
        before = path.read_bytes()
        with self.assertRaisesRegex(ValueError, '任务'):
            self.u.database_snapshot(path, idle=True)
        self.assertEqual(path.read_bytes(), before)

    def test_history_snapshot_detects_changed_rows(self):
        path = self.database('done')
        before = self.u.database_snapshot(path, idle=True)
        db = sqlite3.connect(path)
        db.execute("update sessions set status='error'")
        db.commit()
        db.close()
        self.assertNotEqual(self.u.database_snapshot(path, idle=True), before)

    def test_rule_inventory_includes_directory_attributes(self):
        (self.root / 'fingerprints').mkdir()
        (self.root / 'fingerprints/x.yaml').write_text('rule')
        info = self.u.inventory(self.root)
        self.assertIn('fingerprints', info)
        self.assertIn('mode', info['fingerprints'])
        self.assertIn('sha256', info['fingerprints/x.yaml'])

    def test_copy_omits_only_obsolete_rule_snapshots(self):
        src, dst = self.root / 'old', self.root / 'new'
        (src / 'data/fingerprints').mkdir(parents=True)
        (src / 'data/mcp').mkdir()
        (src / 'data/mcp/custom.json').write_text('custom')
        (src / 'data/fingerprints/obsolete.yaml').write_text('obsolete')
        self.u.copy_program(src, dst)
        self.assertFalse((dst / 'data/fingerprints').exists())
        self.assertEqual((dst / 'data/mcp/custom.json').read_text(), 'custom')

    def test_runtime_rule_mountpoint_directories_are_not_business_data(self):
        self.assertTrue(hasattr(self.u, 'business_data_inventory'), 'business inventory must ignore external rule mountpoints')
        (self.root / 'mcp').mkdir()
        (self.root / 'mcp/custom').write_text('keep')
        before = self.u.business_data_inventory(self.root)
        for scope in self.u.SCOPES:
            (self.root / scope).mkdir()
        self.assertEqual(before, self.u.business_data_inventory(self.root))

    def test_agent_status_requires_positive_live_registration(self):
        self.assertTrue(hasattr(self.u, 'verify_agent_status'), 'live readiness gate must be implemented')
        with self.assertRaisesRegex(ValueError, 'Agent'):
            self.u.verify_agent_status({'connected_count': 0, 'agents': []})
        with self.assertRaisesRegex(ValueError, 'Agent'):
            self.u.verify_agent_status({'connected_count': 1, 'agents': []})
        self.u.verify_agent_status({'connected_count': 1, 'agents': [{'agent_id': 'test', 'connected': True}]})

    def test_partial_knowledge_response_cannot_pass_as_readable_rules(self):
        for scope in self.u.SCOPES:
            (self.root / scope).mkdir()
            (self.root / scope / 'rule.yaml').write_text('fixture')
        with patch.object(self.u, 'RULES', self.root), patch.object(self.u, 'curl_json', return_value={'total': 0, 'items': []}):
            with self.assertRaisesRegex(ValueError, '规则|知识库'):
                self.u.knowledge('fixture')

    def test_agent_readiness_transport_accepts_raw_status_shape(self):
        self.assertTrue(hasattr(self.u, 'fetch_agent_status'), 'raw Agent status transport must exist')
        raw = {'connected_count': 1, 'agents': [{'agent_id': 'real-agent', 'connected': True}]}
        with patch.object(self.u, 'run', return_value=json.dumps(raw)):
            self.assertEqual(self.u.fetch_agent_status('fixture'), raw)

    def test_failed_state_write_never_prevents_stopped_source_recovery(self):
        self.assertTrue(hasattr(self.u, 'recover_failed_upgrade'), 'recovery must tolerate failed diagnostics')
        events = []
        state = {'phase': 'failed'}
        layout = {'agent': {'Id': 'old-agent'}, 'web': {'Id': 'old-web'}}
        class Gate:
            def end(self):
                events.append('gate-open')
        def execute(args, **kwargs):
            events.append('start-old')
        with contextlib.redirect_stderr(io.StringIO()), \
                patch.object(self.u, 'save', side_effect=OSError('disk full')), \
                patch.object(self.u, 'candidate_ids', return_value=['new-web', 'new-agent']), \
                patch.object(self.u, 'stop_ids', side_effect=lambda ids: events.append('stop-candidate')), \
                patch.object(self.u, 'run', side_effect=execute), \
                patch.object(self.u, 'wait_health', side_effect=lambda *args: events.append('verify-old')):
            self.u.recover_failed_upgrade(self.root, state, layout, True, True, Gate(), {})
        self.assertEqual(events, ['stop-candidate', 'start-old', 'verify-old', 'gate-open'])
        self.assertEqual(state['recovery'], 'source-restored')

    def test_access_opening_is_marked_before_teardown_can_expose_writes(self):
        self.assertTrue(hasattr(self.u, 'begin_access_opening'), 'access transition must preserve potentially exposed writers')
        self.assertIn('record_path', inspect.signature(self.u.begin_access_opening).parameters, 'opening phase must persist before exposing writes')
        state = {}
        record = self.root / 'opening.json'
        class Gate:
            def end(self):
                self_test.assertEqual(state['phase'], 'opening-access')
                self_test.assertEqual(state['accepted_service'], 'candidate')
                self_test.assertTrue(json.loads(record.read_text())['access_may_be_public'])
                raise RuntimeError('partial teardown exposed candidate')
        self_test = self
        with self.assertRaisesRegex(RuntimeError, 'partial teardown'):
            self.u.begin_access_opening(Gate(), state, 'candidate', record)
        self.assertTrue(state['access_may_be_public'])

    def test_staging_packager_creates_self_contained_pinned_entry(self):
        packager = self.module_path.with_name('package_platform.py')
        self.assertTrue(packager.is_file(), 'offline packager must exist')
        spec = importlib.util.spec_from_file_location('package_platform', packager)
        pack = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(pack)
        wrapper = pack.wrapper('c' * 64, b'print("safe")')
        self.assertIn('c' * 64, wrapper)
        self.assertIn('AIG-platform-v1.1.1-linux-amd64.tar.gz', wrapper)
        self.assertNotIn('curl ', wrapper)


if __name__ == '__main__':
    unittest.main()
