"""验证规则管理适配仅改变管理代码，不改变规则快照且拒绝未知输入。"""
import importlib.util
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('rule_adapter', Path(__file__).with_name('prepare_rules_v111.py'))
adapter = importlib.util.module_from_spec(spec)
spec.loader.exec_module(adapter)


class AdapterTest(unittest.TestCase):
    def test_ui_patch_tag_is_accepted_without_accepting_other_platforms(self):
        layout = "WORKING_DIR = BASE + '/v1.1.0'\ndef accepts(tag):\n    return tag in ('v1.1.0', '1.1.0')\nMESSAGE = '契约 1.0 仅支持程序基线 v1.1.0'\n"
        namespace = {'BASE': '/opt/aig'}
        exec(adapter.adapt_layout(layout), namespace)
        self.assertTrue(namespace['accepts']('v1.1.1-ui1'))
        self.assertTrue(namespace['accepts']('v1.1.1'))
        self.assertFalse(namespace['accepts']('v1.1.0'))
        self.assertFalse(namespace['accepts']('v1.1.2'))

    def test_unknown_delivery_rejected_before_output(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            (root / 'unknown.sh').write_text('echo no')
            (root / 'unknown.tar.gz').write_bytes(b'not a package')
            with self.assertRaises(ValueError):
                adapter.prepare(root / 'unknown.sh', root / 'unknown.tar.gz', root / 'out')
            self.assertFalse((root / 'out').exists())

    def test_version_pins_are_replaced_exactly_and_keep_lock_protocol(self):
        layout = "WORKING_DIR = BASE + '/v1.1.0'\n('v1.1.0', '1.1.0')\n契约 1.0 仅支持程序基线 v1.1.0"
        updated = adapter.adapt_layout(layout)
        self.assertIn("BASE + '/v1.1.1'", updated)
        self.assertIn("('v1.1.1', '1.1.1', 'v1.1.1-ui1')", updated)
        with self.assertRaises(ValueError):
            adapter.adapt_layout(layout + "\nWORKING_DIR = BASE + '/v1.1.0'")


if __name__ == '__main__':
    unittest.main()
