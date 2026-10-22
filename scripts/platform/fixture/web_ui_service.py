#!/usr/bin/env python3
"""功能：专用 Web UI 事务夹具的无扫描 HTTP 服务。
实现：返回最小带端口选项的入口及 JS，保留知识库和真实回环 Checker 探针。
输入：ROLE 环境与测试卷；输出：内层网络 HTTP，不向外部目标发请求。
依赖：Python 标准库，仅 COPY 到隔离测试目标镜像，禁止作为正式 Web 交付。
"""

from http.server import BaseHTTPRequestHandler, HTTPServer
import json
import os
from pathlib import Path
import signal
import sys
import urllib.request

signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))
role = os.environ.get("ROLE", "web")
if role == "agent":
    path = Path("/api-checker-data/keep.txt")
    if not path.exists():
        path.write_text("checker-original")
else:
    print("Agent注册成功: fixture-web-ui-only", flush=True)

JAVASCRIPT = b'const a={type:"radio",name:"scan-port-mode",value:"full_tcp"};const b={type:"radio",name:"scan-port-mode",value:"fixed_ai"};function submit(params,mode){params.port_mode=mode;}const text={portModeTitle:"Port mode",fullTcpDetail:"1-65535"};'


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_GET(self):
        content_type = "application/json"
        if self.path == "/healthz":
            value = {"ok": True}
        elif self.path == "/api/v1/agents/status":
            opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
            try:
                opener.open("http://agent:8000/healthz", timeout=2).read()
                value = {"connected_count": 1, "agents": [{"agent_id": "fixture", "connected": True}]}
            except Exception:
                value = {"connected_count": 0, "agents": []}
        elif self.path.startswith("/api/v1/knowledge/"):
            value = {"status": 0, "data": {"total": 1, "items": [{"name": "isolated-harmless-fixture"}]}}
        elif self.path == "/assets/fixture-port-mode.js":
            value = JAVASCRIPT
            content_type = "application/javascript"
        else:
            value = b'<!doctype html><html><body>Isolated Web UI fixture<script type="module" src="/assets/fixture-port-mode.js"></script></body></html>'
            content_type = "text/html"
        body = value if isinstance(value, bytes) else json.dumps(value).encode()
        self.send_response(200)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


HTTPServer(("0.0.0.0", 8000 if role == "agent" else 8088), Handler).serve_forever()
