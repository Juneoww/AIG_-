// Copyright (c) 2024-2026 Tencent Zhuque Lab. All rights reserved.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package agent

import targetparser "github.com/Tencent/AI-Infra-Guard/common/targets"

// targetParseError 保留原解析错误摘要和逐行问题，供任务失败事件使用。
type targetParseError struct {
	err    error
	issues []targetparser.LineError
}

func (e *targetParseError) Error() string { return e.err.Error() }
func (e *targetParseError) Unwrap() error { return e.err }

// TargetIssue 是 SSE 事件中的稳定 JSON 契约，不直接暴露解析器内部类型。
type TargetIssue struct {
	Line    int    `json:"line"`
	Input   string `json:"input"`
	Reason  string `json:"reason"`
	Suggest string `json:"suggest"`
}

func (e *targetParseError) targetIssues() []TargetIssue {
	issues := make([]TargetIssue, 0, len(e.issues))
	for _, issue := range e.issues {
		issues = append(issues, TargetIssue{
			Line:    issue.Line,
			Input:   issue.Input,
			Reason:  issue.Reason,
			Suggest: issue.Suggest,
		})
	}
	return issues
}
