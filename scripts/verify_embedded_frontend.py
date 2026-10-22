"""功能：发布前验证实际入口资源包含一期端口选项和请求参数，并可核对 Go 二进制。
实现：解析 index.html 的本地资源及入口 JS 依赖，检查构建后的 radio 控件与
  port_mode 赋值；可选逐字节核对 index、JS 和入口引用资源已嵌入指定二进制。
输入：--static 静态目录（默认 common/websocket/static），--binary 可选二进制。
输出：JSON 验收摘要；失败返回非零。仅读取文件，不构建、不启动服务、不执行扫描。
依赖：Python 3 标准库。用法：python scripts/verify_embedded_frontend.py --binary ./web
"""

import argparse
import hashlib
from html.parser import HTMLParser
import json
import mmap
from pathlib import Path
import re
import sys
from urllib.parse import unquote, urlsplit


class VerificationError(ValueError):
    pass


class EntryParser(HTMLParser):
    def __init__(self):
        super().__init__()
        self.resources = []
        self.scripts = []

    def handle_starttag(self, tag, attrs):
        values = dict(attrs)
        if tag == "script" and values.get("src"):
            self.resources.append(values["src"])
            if values.get("type") == "module":
                self.scripts.append(values["src"])
        elif tag == "link" and values.get("href"):
            self.resources.append(values["href"])


def resolve_resource(root, reference, parent=None):
    url = urlsplit(reference)
    if url.scheme or url.netloc:
        raise VerificationError(f"external resource is not embedded: {reference}")
    name = unquote(url.path)
    target = ((root / name.lstrip("/")) if name.startswith("/")
              else (parent or root) / name).resolve()
    if not target.is_relative_to(root):
        raise VerificationError(f"resource outside static root: {reference}")
    if not target.is_file():
        raise VerificationError(f"missing embedded resource: {reference}")
    return target


def verify(static_dir, binary=None):
    root = Path(static_dir).resolve()
    index = root / "index.html"
    if not index.is_file():
        raise VerificationError(f"missing embedded index: {index}")
    parser = EntryParser()
    parser.feed(index.read_text(encoding="utf-8"))
    if not parser.scripts:
        raise VerificationError("index has no module entry script")
    resources = [resolve_resource(root, reference) for reference in parser.resources]
    entries = [resolve_resource(root, src) for src in parser.scripts]
    javascript = {}
    pending = list(entries)
    # 只跟随入口实际加载的依赖，避免遗留的无引用新包掩盖旧入口。
    imports = re.compile(r'''(?:\bimport\s*(?:\(\s*)?|\bfrom\s*)["']([^"']+\.js(?:\?[^"']*)?)["']''')
    while pending:
        script = pending.pop()
        if script in javascript:
            continue
        source = script.read_text(encoding="utf-8")
        javascript[script] = source
        for reference in imports.findall(source):
            pending.append(resolve_resource(root, reference, script.parent))
    content = "\n".join(javascript.values())
    for mode in ("full_tcp", "fixed_ai"):
        control = (r'type\s*:\s*["\']radio["\']\s*,\s*'
                   r'name\s*:\s*["\']scan-port-mode["\']\s*,\s*'
                   r'value\s*:\s*["\']' + mode + r'["\']')
        if not re.search(control, content):
            raise VerificationError(f"built entry missing port mode radio: {mode}")
    for key in ("portModeTitle", "fullTcpDetail"):
        if key not in content:
            raise VerificationError(f"built entry missing port mode text: {key}")
    if not re.search(r'\b[A-Za-z_$][\w$]*\.port_mode\s*=\s*[A-Za-z_$][\w$]*', content):
        raise VerificationError("built entry missing request params.port_mode assignment")
    if binary is not None:
        binary = Path(binary)
        if not binary.is_file() or binary.stat().st_size == 0:
            raise VerificationError(f"missing or empty binary: {binary}")
        with binary.open("rb") as stream, mmap.mmap(stream.fileno(), 0, access=mmap.ACCESS_READ) as data:
            for resource in dict.fromkeys((index, *javascript, *resources)):
                if data.find(resource.read_bytes()) < 0:
                    raise VerificationError(f"binary does not embed current resource: {resource.relative_to(root).as_posix()}")
    return {
        "static": str(root),
        "entry_scripts": [path.relative_to(root).as_posix() for path in entries],
        "javascript": {
            path.relative_to(root).as_posix(): hashlib.sha256(path.read_bytes()).hexdigest()
            for path in sorted(javascript)
        },
        "port_modes": ["full_tcp", "fixed_ai"],
        "request_parameter": "params.port_mode",
        "binary_verified": str(binary) if binary else None,
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--static", type=Path, default=Path(__file__).resolve().parents[1] / "common/websocket/static")
    parser.add_argument("--binary", type=Path)
    args = parser.parse_args()
    try:
        result = verify(args.static, args.binary)
    except (VerificationError, OSError, UnicodeError) as exc:
        print(f"embedded frontend verification failed: {exc}", file=sys.stderr)
        return 1
    print(json.dumps(result, ensure_ascii=True, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())
