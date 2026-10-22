#!/usr/bin/env python3
"""功能：在完全独立的嵌套Docker引擎中提供升级事务测试服务。
实现：模拟Web只读知识库、真实HTTP Checker探针及Agent就绪接口；不执行扫描。
输入：ROLE环境变量及挂载；输出：回环HTTP、测试卷文件和注册日志。
依赖：Python标准库；不随正式平台包交付。
"""
from http.server import BaseHTTPRequestHandler, HTTPServer
import json
import os
from pathlib import Path
import urllib.request
import signal
import sys

signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))

role = os.environ.get('ROLE', 'web')
if role == 'agent':
    path = Path('/api-checker-data/keep.txt')
    if not path.exists():
        path.write_text('checker-original')
else:
    print('Agent注册成功: fixture-real-http-checker', flush=True)


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_GET(self):
        if self.path == '/healthz':
            value = {'ok': True}
        elif self.path == '/api/v1/agents/status':
            opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
            try:
                opener.open('http://agent:8000/healthz', timeout=2).read()
                value = {'connected_count': 1, 'agents': [{'agent_id': 'fixture', 'connected': True}]}
            except Exception:
                value = {'connected_count': 0, 'agents': []}
        elif self.path.startswith('/api/v1/knowledge/'):
            value = {'status': 0, 'data': {'total': 1, 'items': [{'name': 'isolated-harmless-fixture'}]}}
        else:
            body = b'<!doctype html><html><body>Isolated fixture</body></html>'
            self.send_response(200)
            self.end_headers()
            self.wfile.write(body)
            return
        body = json.dumps(value).encode()
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.end_headers()
        self.wfile.write(body)


HTTPServer(('0.0.0.0', 8000 if role == 'agent' else 8088), Handler).serve_forever()
