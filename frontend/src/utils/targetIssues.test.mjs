/**
 * 功能：验证解析错误事件的安全读取、排队和消息合并。
 * 实现：用 Node 内置测试运行器执行纯函数，无浏览器或服务依赖。
 * 输入：模拟 SSE 与持久化任务中的错误事件。
 * 输出：测试断言；运行命令为 node --test src/utils/targetIssues.test.mjs。
 */
import test from 'node:test';
import assert from 'node:assert/strict';
import {
  parseTargetIssues,
  errorMessageFromEvent,
  shouldDropQueuedError,
  upsertErrorMessage,
} from './targetIssues.ts';

const at = new Date('2026-09-29T00:00:00.000Z');
const issue = { line: 2, input: '192.168.10.2-10', reason: '不支持缩写', suggest: '请写完整地址' };
const plain = { message: '目标解析失败,共 1 处无效输入' };
const detailed = { ...plain, targetIssues: [issue] };

test('parseTargetIssues keeps ordered valid issues and global line zero', () => {
  assert.deepEqual(parseTargetIssues([
    { line: 0, reason: '容量超限' },
    issue,
    { line: -1, reason: 'invalid' },
    { line: 3, reason: '  ' },
    { line: 4, reason: 'invalid optional', input: 42 },
  ]), [
    { line: 0, input: '', reason: '容量超限', suggest: '' },
    issue,
  ]);
  assert.deepEqual(parseTargetIssues({ line: 1, reason: 'wrong shape' }), []);
  assert.deepEqual(parseTargetIssues(null), []);
});

test('upsertErrorMessage upgrades plain summary and never downgrades detail', () => {
  const first = errorMessageFromEvent(plain, '未知错误', 'first', at);
  const rich = errorMessageFromEvent(detailed, '未知错误', 'second', at);
  let messages = upsertErrorMessage([], first);
  messages = upsertErrorMessage(messages, rich);
  assert.equal(messages.length, 1);
  assert.equal(messages[0].id, 'first');
  assert.deepEqual(messages[0].targetIssues, [issue]);
  messages = upsertErrorMessage(messages, first);
  messages = upsertErrorMessage(messages, rich);
  assert.equal(messages.length, 1);
  assert.deepEqual(messages[0].targetIssues, [issue]);
  messages = upsertErrorMessage(messages, errorMessageFromEvent({ message: '其他错误' }, '未知错误', 'other', at));
  assert.equal(messages.length, 2);
});

test('same-count sparse event cannot erase input or suggestion', () => {
  const complete = errorMessageFromEvent(detailed, '未知错误', 'complete', at);
  const sparse = errorMessageFromEvent({
    ...plain,
    targetIssues: [{ line: 2, reason: '不支持缩写' }],
  }, '未知错误', 'sparse', at);
  const restored = upsertErrorMessage([complete], sparse);
  assert.deepEqual(restored[0].targetIssues, [issue]);

  const partiallyNew = errorMessageFromEvent({
    ...plain,
    targetIssues: [{ line: 2, input: '192.168.10.2-10', reason: '不支持缩写' }],
  }, '未知错误', 'partially-new', at);
  const merged = upsertErrorMessage([sparse], partiallyNew);
  assert.deepEqual(merged[0].targetIssues, [{ ...issue, suggest: '' }]);
});

test('queue admission does not swallow late detailed errors', () => {
  assert.equal(shouldDropQueuedError(plain, detailed), false);
  assert.equal(shouldDropQueuedError(detailed, plain), true);
  assert.equal(shouldDropQueuedError(detailed, detailed), true);
  assert.equal(shouldDropQueuedError(detailed, { ...plain, targetIssues: [{ line: 2, reason: '不支持缩写' }] }), true);
  assert.equal(shouldDropQueuedError(plain, { message: '其他错误' }), false);
});

test('persisted event folding retains the richest details in either order', () => {
  for (const events of [[plain, detailed], [detailed, plain]]) {
    const restored = events.reduce((messages, event, index) =>
      upsertErrorMessage(messages, errorMessageFromEvent(event, '未知错误', String(index), at)), []);
    assert.equal(restored.length, 1);
    assert.deepEqual(restored[0].targetIssues, [issue]);
  }
});

test('invalid detail is ignored while generic message remains visible', () => {
  const message = errorMessageFromEvent({ error: '连接失败', targetIssues: 'bad' }, '未知错误', 'x', at);
  assert.equal(message.content, '连接失败');
  assert.equal(message.targetIssues, undefined);
});
