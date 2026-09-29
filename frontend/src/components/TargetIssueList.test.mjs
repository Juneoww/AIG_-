/**
 * 功能：验证目标解析错误列表的结构、顺序与文本转义。
 * 实现：Vite 加载 TSX 组件后用 React 服务端渲染检查 HTML。
 * 输入：模拟逐行错误和中英文标签。
 * 输出：测试断言；运行命令为 node --test src/components/TargetIssueList.test.mjs。
 * 依赖：项目已安装的 Vite、React 和 React DOM。
 */
import test from 'node:test';
import assert from 'node:assert/strict';
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { createServer } from 'vite';

test('TargetIssueList renders ordered issues, global label and escaped input', async () => {
  const server = await createServer({
    configFile: false,
    server: { middlewareMode: true },
    appType: 'custom',
    optimizeDeps: { noDiscovery: true, include: [] },
  });
  try {
    const { default: TargetIssueList } = await server.ssrLoadModule('/src/components/TargetIssueList.tsx');
    const markup = renderToStaticMarkup(React.createElement(TargetIssueList, {
      issues: [
        { line: 2, input: '<script>alert(1)</script>', reason: '不支持缩写', suggest: '请写完整地址' },
        { line: 0, input: '', reason: '容量超限', suggest: '' },
      ],
      labels: { line: (number) => `第 ${number} 行`, global: '整体输入', suggestion: '建议' },
    }));
    assert.match(markup, /<ol/);
    assert.match(markup, /第 2 行/);
    assert.match(markup, /整体输入/);
    assert.match(markup, /不支持缩写/);
    assert.match(markup, /建议/);
    assert.match(markup, /请写完整地址/);
    assert.ok(markup.indexOf('第 2 行') < markup.indexOf('整体输入'));
    assert.match(markup, /&lt;script&gt;alert\(1\)&lt;\/script&gt;/);
    assert.doesNotMatch(markup, /<script>/);
  } finally {
    await server.close();
  }
});
