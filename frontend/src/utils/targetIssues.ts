/**
 * 功能：读取解析错误事件，并在实时与历史任务消息中保留逐行明细。
 * 实现：先校验服务端可选字段，再按错误摘要合并，避免晚到事件降级。
 * 输入：未知来源的错误事件与当前任务消息。
 * 输出：安全的错误消息、队列去重判断和更新后的消息数组；无副作用。
 */
import type { Message } from '../types';

export interface TargetIssue {
  line: number;
  input: string;
  reason: string;
  suggest: string;
}

export function parseTargetIssues(value: unknown): TargetIssue[] {
  if (!Array.isArray(value)) return [];
  const issues: TargetIssue[] = [];
  for (const item of value) {
    if (typeof item !== 'object' || item === null) continue;
    const raw = item as Record<string, unknown>;
    if (!Number.isInteger(raw.line) || (raw.line as number) < 0) continue;
    if (typeof raw.reason !== 'string' || raw.reason.trim() === '') continue;
    if (raw.input !== undefined && typeof raw.input !== 'string') continue;
    if (raw.suggest !== undefined && typeof raw.suggest !== 'string') continue;
    issues.push({
      line: raw.line as number,
      input: (raw.input as string | undefined) ?? '',
      reason: raw.reason.trim(),
      suggest: (raw.suggest as string | undefined) ?? '',
    });
  }
  return issues;
}

function eventFields(event: unknown): Record<string, unknown> {
  return typeof event === 'object' && event !== null ? event as Record<string, unknown> : {};
}

function eventSummary(event: unknown, fallback: string): string {
  const fields = eventFields(event);
  if (typeof fields.message === 'string' && fields.message) return fields.message;
  if (typeof fields.error === 'string' && fields.error) return fields.error;
  return fallback;
}

export function errorMessageFromEvent(event: unknown, fallback: string, id: string, timestamp: Date): Message {
  const targetIssues = parseTargetIssues(eventFields(event).targetIssues);
  return {
    id,
    type: 'error',
    content: eventSummary(event, fallback),
    timestamp,
    ...(targetIssues.length > 0 ? { targetIssues } : {}),
  };
}

export function shouldDropQueuedError(existing: unknown, incoming: unknown): boolean {
  if (eventSummary(existing, '') !== eventSummary(incoming, '')) return false;
  const previous = parseTargetIssues(eventFields(existing).targetIssues);
  const next = parseTargetIssues(eventFields(incoming).targetIssues);
  return JSON.stringify(mergeTargetIssues(previous, next)) === JSON.stringify(previous);
}

function mergeTargetIssues(previous: TargetIssue[], incoming: TargetIssue[]): TargetIssue[] {
  if (incoming.length === 0 || incoming.length < previous.length) return previous;
  if (previous.length === 0) return incoming;

  // 同一错误的可选字段可能分批到达；空字段不能覆盖已知的原输入和建议。
  const merged = incoming.map(issue => {
    const earlier = previous.find(item => item.line === issue.line && item.reason === issue.reason);
    return earlier
      ? { ...issue, input: issue.input || earlier.input, suggest: issue.suggest || earlier.suggest }
      : issue;
  });
  if (incoming.length === previous.length && !incoming.every((issue, index) =>
    issue.line === previous[index].line && issue.reason === previous[index].reason
  )) {
    const completeness = (issues: TargetIssue[]) => issues.reduce((score, issue) =>
      score + Number(Boolean(issue.input)) + Number(Boolean(issue.suggest)), 0);
    return completeness(merged) > completeness(previous) ? merged : previous;
  }
  return merged;
}

export function upsertErrorMessage(messages: Message[], incoming: Message): Message[] {
  const index = messages.findIndex(message => message.type === 'error' && message.content === incoming.content);
  if (index === -1) return [...messages, incoming];
  const previous = messages[index];
  const oldIssues = previous.targetIssues ?? [];
  const newIssues = incoming.targetIssues ?? [];
  const mergedIssues = mergeTargetIssues(oldIssues, newIssues);
  if (JSON.stringify(mergedIssues) === JSON.stringify(oldIssues)) return messages;
  return messages.map((message, i) => i === index ? { ...message, targetIssues: mergedIssues } : message);
}
