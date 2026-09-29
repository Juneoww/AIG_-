/**
 * 功能：在扫描错误卡片中逐行展示无效目标及修正建议。
 * 实现：按服务端顺序渲染语义列表，限制高度并让长目标自动换行。
 * 输入：已校验的解析错误和界面语言标签。
 * 输出：React 错误明细视图；文本由 React 自动转义。
 */
import type { TargetIssue } from '../utils/targetIssues';

interface TargetIssueListProps {
  issues: TargetIssue[];
  labels: {
    line: (number: number) => string;
    global: string;
    suggestion: string;
  };
}

export default function TargetIssueList({ issues, labels }: TargetIssueListProps) {
  return (
    <ol className="mt-3 max-h-64 space-y-2 overflow-y-auto border-t border-red-200 pt-3 text-left">
      {issues.map((issue, index) => (
        <li key={`${issue.line}-${index}`} className="rounded-md bg-white/70 px-3 py-2 text-sm">
          <div className="mb-1 text-xs font-semibold text-red-700">
            {issue.line === 0 ? labels.global : labels.line(issue.line)}
          </div>
          {issue.input && <code className="mb-1 block break-all rounded bg-red-50 px-1 py-0.5 font-mono text-xs text-red-900">{issue.input}</code>}
          <p className="break-words leading-relaxed">{issue.reason}</p>
          {issue.suggest && (
            <p className="mt-1 break-words text-xs leading-relaxed text-red-700">
              <span className="font-semibold">{labels.suggestion}：</span>{issue.suggest}
            </p>
          )}
        </li>
      ))}
    </ol>
  );
}
