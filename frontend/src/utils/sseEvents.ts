/**
 * 功能：管理任务 SSE 的业务错误、网络重连与连接清理。
 * 实现：业务 `event: error` 是 MessageEvent；失败后只等待一次短暂的明细补全窗口。
 * 输入：EventSource 事件及状态、关闭回调、连接注册表和可替换的计时器。
 * 输出：事件判断、终态关闭控制器与批量清理函数。
 */
export function isSsePayloadEvent(event: Event): event is MessageEvent<string> {
  return event instanceof MessageEvent && typeof event.data === 'string';
}

export function shouldCloseSseTransport(readyState: number): boolean {
  // EventSource.CONNECTING (0) 会自动重连；CLOSED (2) 才是不可恢复的终态。
  return readyState === 2;
}

export function sseCloseActionForTask(status: string | undefined, hadTask = false): 'keep' | 'schedule' | 'close' {
  // SSE 可以先于乐观任务建档；只有已存在过的任务消失才视为删除。
  if (status === undefined) return hadTask ? 'close' : 'keep';
  if (status === 'error' || status === 'completed' || status === 'done' || status === 'terminated') {
    return 'schedule';
  }
  return 'keep';
}

export function shouldStartSseConnection(status: string | undefined, mustExist = false): boolean {
  return (!mustExist || status !== undefined) && sseCloseActionForTask(status) === 'keep';
}

export function closeAllSseConnections<T extends { close: () => void }>(connections: Map<string, T>): void {
  const closers = [...connections.values()];
  connections.clear();
  closers.forEach(connection => connection.close());
}

interface SseTimer {
  start(callback: () => void, delayMs: number): ReturnType<typeof setTimeout>;
  stop(handle: ReturnType<typeof setTimeout>): void;
}

const defaultTimer: SseTimer = {
  start: (callback, delayMs) => setTimeout(callback, delayMs),
  stop: handle => clearTimeout(handle),
};

const TERMINAL_ERROR_GRACE_MS = 5000;

export function createTerminalErrorCloser(onClose: () => void, timer: SseTimer = defaultTimer) {
  let handle: ReturnType<typeof setTimeout> | undefined;
  let closed = false;

  const close = () => {
    if (closed) return;
    closed = true;
    if (handle !== undefined) {
      timer.stop(handle);
      handle = undefined;
    }
    onClose();
  };

  return {
    schedule() {
      if (closed || handle !== undefined) return;
      handle = timer.start(close, TERMINAL_ERROR_GRACE_MS);
    },
    close,
  };
}
