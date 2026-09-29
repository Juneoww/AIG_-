# 扫描端口模式前端交互 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 AI 基础设施扫描的现有齿轮弹窗中完成 B 版端口模式选择，并把确认后的模式显式写入任务请求。

**Architecture:** `ChatArea` 持有已确认配置，`FloatingInputArea` 提供唯一的齿轮入口，`HttpHeaderDialog` 持有打开期间的草稿。模式值采用前端联合类型；现有任务提交路径仅为 AI 基础设施扫描写入 `params.port_mode`。MCP 扫描继续使用同一弹窗的 Header 部分。

**Tech Stack:** React 18、TypeScript、Tailwind CSS、react-i18next、Vite、pnpm。

**Spec:** `docs/superpowers/specs/2026-09-28-port-mode-frontend-design.md`

---

当前 Git 仓库尚无基线提交，原有源码均未跟踪。本计划不自动创建首个源码提交；各步以定向文件检查、类型检查和浏览器交互验证。

## 文件职责

- 新建 `frontend/src/types/scanConfig.ts`：模式联合类型、默认值、固定 AI 端口规则及数量。
- 修改 `frontend/src/components/ChatArea.tsx`：保存已确认模式、任务请求传参、两处输入区传值、清理未使用的重复弹窗。
- 修改 `frontend/src/components/floatingInputArea/FloatingInputArea.tsx`：齿轮蓝点、弹窗传值与确认回调。
- 修改 `frontend/src/components/HttpHeaderDialog.tsx`：B 版紧凑单选行、草稿/确认/取消、响应式和可访问性。
- 修改 `frontend/src/i18n/locales/zh.json` 与 `en.json`：标题、说明、模式与端口数量文案。

### Task 1: 模式契约与请求

**Files:** `frontend/src/types/scanConfig.ts`、`frontend/src/components/ChatArea.tsx`

- [ ] **Step 1: 定义可用值。** 新模块导出如下契约，供状态、弹窗和请求共用：

```ts
export type PortMode = 'full_tcp' | 'fixed_ai';
export const DEFAULT_PORT_MODE: PortMode = 'full_tcp';
export const FIXED_AI_PORTS = '11434,1337,7000-9000,18789';
export const FIXED_AI_PORT_COUNT = 2004;
```

- [ ] **Step 2: 在 `ChatArea` 中新增 `portMode` 状态。** 初始值使用 `DEFAULT_PORT_MODE`。新增一个确认回调，同时更新 `httpHeaders` 和 `portMode`。
- [ ] **Step 3: 在 `sendMessageToServer` 的 `params` 组装处添加条件。** 仅当 `taskType === 'AI-Infra-Scan'` 时执行 `params.port_mode = portMode`；即使是默认模式也发送。
- [ ] **Step 4: 检查请求条件。** `rg -n 'port_mode|setPortMode' frontend/src/components/ChatArea.tsx` 应显示唯一的任务类型条件与状态更新，不应为 MCP 或其他任务添加该字段。

### Task 2: 弹窗交互

**Files:** `frontend/src/components/HttpHeaderDialog.tsx`

- [ ] **Step 1: 扩展 Props。** 接受 `showPortMode: boolean`、`portMode: PortMode`，并将确认回调改为 `(headers: HttpHeader[], portMode: PortMode) => void`。MCP 调用时 `showPortMode` 为 `false`。
- [ ] **Step 2: 设置草稿。** 每次 `open` 变为 `true`，将 Header 行复制到 `localHeaders` 并将 `portMode` 复制到 `localPortMode`。取消只关闭；确认过滤空键后一次性回传两个草稿值。
- [ ] **Step 3: 渲染 B 版单选行。** 用原生 `fieldset`、`legend`、`input type="radio"` 与 `label` 保持键盘可操作。全量 TCP 行显示 `1–65535` 与 65,535；固定 AI 行显示 `FIXED_AI_PORT_COUNT`，并从 `FIXED_AI_PORTS` 渲染四段完整规则。只有 `showPortMode` 时显示模式区。
- [ ] **Step 4: 完成弹窗基础可访问性与窄屏布局。** 设置 `role="dialog"`、`aria-modal`、可访问的关闭按钮、可见焦点、视口宽度内的最大宽度和高度滚动。保留原 Header 行增删与空键过滤。

### Task 3: 齿轮、共享状态与文案

**Files:** `frontend/src/components/floatingInputArea/FloatingInputArea.tsx`、`frontend/src/components/ChatArea.tsx`、`frontend/src/i18n/locales/zh.json`、`frontend/src/i18n/locales/en.json`

- [ ] **Step 1: 将 `portMode` 和组合确认回调传入两个 `FloatingInputArea` 渲染分支。** 组件把当前模式和任务类型传给 `HttpHeaderDialog`，确认后关闭本地弹窗。
- [ ] **Step 2: 调整齿轮状态。** `httpHeaders` 中有已确认、键非空的行，或当前为 AI 基础设施扫描且 `portMode !== DEFAULT_PORT_MODE` 时显示蓝点；其余情况不显示。保留原有 `data-joyride` 目标。
- [ ] **Step 3: 清理 `ChatArea` 内未使用的两处 `HttpHeaderDialog`、开关状态和相关 import。** 唯一实际配置入口保留在 `FloatingInputArea`。
- [ ] **Step 4: 补齐中英文文案。** AI 基础设施扫描标题为“扫描配置”/“Scan settings”；MCP 保留原 Header 标题。模式说明须覆盖裸 IPv4 及展开后的 IPv4；两种模式名称、数量和按钮文案不得硬编码成单语言。

### Task 4: 验证

**Files:** 上述所有修改文件

- [ ] **Step 1: 安装锁定依赖（若 `node_modules` 不存在）。** 在 `frontend` 目录运行 `pnpm install --frozen-lockfile`；预期锁文件不变。
- [ ] **Step 2: 类型检查与构建。** 在 `frontend` 目录依次运行 `pnpm exec tsc -b`、`pnpm exec vite build --mode openSource`；预期退出码为 0。
- [ ] **Step 3: 浏览器检查。** 在居中与底部输入区分别检查默认模式、切到固定 AI 后完整端口清单、确认后的蓝点、模式和 Header 未保存时关闭/取消、再次打开状态、任务请求的 `params.port_mode`；切到 MCP 扫描检查仅 Header 页面和请求无端口字段。切换英文、窄屏和键盘焦点再检查一次。
- [ ] **Step 4: 检查工作区。** `git status --short` 与锁文件差异；仅保留本任务需要的源码和文档变化。无需修改后端。
