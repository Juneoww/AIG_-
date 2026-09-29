/**
 * 功能：验证 SSE 业务错误事件不会被当成网络断线。
 * 实现：使用 Node 的 Event 与 MessageEvent 模拟 EventSource 分发类型。
 * 输入：带数据的业务事件和无数据的传输错误。
 * 输出：测试断言；运行命令为 node --test src/utils/sseEvents.test.mjs。
 */
import test from 'node:test';
import assert from 'node:assert/strict';
import {
  closeAllSseConnections,
  createTerminalErrorCloser,
  isSsePayloadEvent,
  sseCloseActionForTask,
  shouldCloseSseTransport,
  shouldStartSseConnection,
} from './sseEvents.ts';

test('SSE error payload is not a transport failure', () => {
  assert.equal(isSsePayloadEvent(new MessageEvent('error', { data: '{"type":"error"}' })), true);
  assert.equal(isSsePayloadEvent(new Event('error')), false);
  assert.equal(isSsePayloadEvent(new MessageEvent('error', { data: '' })), true);
});

test('terminal error keeps one finite window for late details, then closes once', () => {
  let pending;
  let scheduled = 0;
  let closed = 0;
  const closer = createTerminalErrorCloser(() => { closed += 1; }, {
    start(callback, delay) {
      scheduled += 1;
      pending = { callback, delay };
      return scheduled;
    },
    stop() { pending = undefined; },
  });

  closer.schedule();
  closer.schedule();
  assert.equal(scheduled, 1);
  assert.equal(pending.delay, 5000);
  assert.equal(closed, 0);
  pending.callback();
  closer.close();
  closer.schedule();
  assert.equal(closed, 1);
  assert.equal(scheduled, 1);
});

test('transport failure or result closes early and cancels pending error window', () => {
  let pending;
  let stopped = 0;
  let closed = 0;
  const closer = createTerminalErrorCloser(() => { closed += 1; }, {
    start(callback) { pending = callback; return 1; },
    stop() { stopped += 1; },
  });

  closer.schedule();
  closer.close();
  pending();
  assert.equal(stopped, 1);
  assert.equal(closed, 1);
});

test('transient network failures leave EventSource free to reconnect', () => {
  assert.equal(shouldCloseSseTransport(0), false);
  assert.equal(shouldCloseSseTransport(1), false);
  assert.equal(shouldCloseSseTransport(2), true);
});

test('unmount closes every registered connection and clears the registry', () => {
  const closed = [];
  const connections = new Map([
    ['one', { close: () => closed.push('one') }],
    ['two', { close: () => closed.push('two') }],
  ]);

  closeAllSseConnections(connections);
  closeAllSseConnections(connections);
  assert.deepEqual(closed, ['one', 'two']);
  assert.equal(connections.size, 0);
});

test('task-status fallback eventually closes a connection after a missed terminal event', () => {
  assert.equal(sseCloseActionForTask('running'), 'keep');
  assert.equal(sseCloseActionForTask('pending'), 'keep');
  assert.equal(sseCloseActionForTask('error'), 'schedule');
  assert.equal(sseCloseActionForTask('completed'), 'schedule');
  assert.equal(sseCloseActionForTask('done'), 'schedule');
  assert.equal(sseCloseActionForTask('terminated'), 'schedule');
  assert.equal(sseCloseActionForTask(undefined), 'keep');
  assert.equal(sseCloseActionForTask(undefined, true), 'close');
});

test('delayed connection skips a task that finished or was deleted before the timer fired', () => {
  assert.equal(shouldStartSseConnection(undefined), true);
  assert.equal(shouldStartSseConnection(undefined, true), false);
  assert.equal(shouldStartSseConnection('running', true), true);
  assert.equal(shouldStartSseConnection('error', true), false);
  assert.equal(shouldStartSseConnection('completed', true), false);
});
