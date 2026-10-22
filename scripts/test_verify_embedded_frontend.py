"""功能：验证前端发布守卫拒绝缺失、陈旧或未真正嵌入的产物。
实现：创建独立最小静态发布目录，逐项损坏入口、控件和请求参数后验收。
输入：无；依赖 Python 标准库。输出：unittest 结果，不启动服务或扫描。
用法：python -m unittest discover -s scripts -p test_verify_embedded_frontend.py
"""

from pathlib import Path
import tempfile
import unittest

from verify_embedded_frontend import VerificationError, verify


class EmbeddedFrontendTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        (self.root / "assets").mkdir()
        self.index = self.root / "index.html"
        self.index.write_text('<script type="module" src="/assets/main.js"></script>'
                              '<link rel="stylesheet" href="/assets/main.css">', encoding="utf-8")
        (self.root / "assets/main.css").write_text("body{}", encoding="utf-8")
        self.script = self.root / "assets/main.js"
        self.bundle = ('const a={type:"radio",name:"scan-port-mode",value:"full_tcp"};'
                       'const b={type:"radio",name:"scan-port-mode",value:"fixed_ai"};'
                       'const c={portModeTitle:"Port discovery mode",fullTcpDetail:"1–65535"};'
                       'function submit(p,m){p.port_mode=m;}')
        self.script.write_text(self.bundle, encoding="utf-8")

    def test_accepts_real_entry_and_embedded_bytes(self):
        binary = self.root / "web"
        binary.write_bytes(b"prefix" + self.index.read_bytes() + self.script.read_bytes())
        result = verify(self.root, binary)
        self.assertEqual(result["entry_scripts"], ["assets/main.js"])

    def test_missing_entry_resource_is_rejected(self):
        self.script.unlink()
        with self.assertRaisesRegex(VerificationError, "missing.*main.js"):
            verify(self.root)

    def test_missing_stylesheet_is_rejected(self):
        (self.root / "assets/main.css").unlink()
        with self.assertRaisesRegex(VerificationError, "missing.*main.css"):
            verify(self.root)

    def test_unreferenced_new_bundle_cannot_mask_old_entry(self):
        (self.root / "assets/new.js").write_text(self.bundle, encoding="utf-8")
        self.script.write_text("console.log('old release');", encoding="utf-8")
        with self.assertRaisesRegex(VerificationError, "radio"):
            verify(self.root)

    def test_each_option_and_request_assignment_are_required(self):
        for missing in ('value:"full_tcp"', 'value:"fixed_ai"', 'p.port_mode=m'):
            with self.subTest(missing=missing):
                self.script.write_text(self.bundle.replace(missing, "removed"), encoding="utf-8")
                with self.assertRaises(VerificationError):
                    verify(self.root)

    def test_markers_in_text_cannot_replace_compiled_controls(self):
        self.script.write_text('const text="scan-port-mode full_tcp fixed_ai portModeTitle fullTcpDetail port_mode";', encoding="utf-8")
        with self.assertRaisesRegex(VerificationError, "radio"):
            verify(self.root)

    def test_old_binary_is_rejected(self):
        binary = self.root / "web"
        binary.write_bytes(self.index.read_bytes() + b"old script")
        with self.assertRaisesRegex(VerificationError, "binary.*main.js"):
            verify(self.root, binary)

    def test_referenced_split_bundle_is_checked(self):
        self.script.write_text('import "./ports.js";', encoding="utf-8")
        (self.root / "assets/ports.js").write_text(self.bundle, encoding="utf-8")
        self.assertIn("assets/ports.js", verify(self.root)["javascript"])

    def test_entry_cannot_escape_static_root(self):
        self.index.write_text('<script type="module" src="../outside.js"></script>', encoding="utf-8")
        with self.assertRaisesRegex(VerificationError, "outside"):
            verify(self.root)


if __name__ == "__main__":
    unittest.main()
