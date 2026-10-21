#!/usr/local/bin/python3
"""功能：隔离测试镜像内提供curl最小HTTP读取接口。
实现：仅转发最后一个URL到urllib；输入：curl风格参数；输出：HTTP正文或非零退出。
依赖：Python标准库；仅限测试镜像，不进入平台交付包。
"""
import sys
import urllib.request
try:
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    sys.stdout.buffer.write(opener.open(sys.argv[-1], timeout=10).read())
except Exception as error:
    print(str(error), file=sys.stderr)
    sys.exit(1)
