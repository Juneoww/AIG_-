// Copyright (c) 2024-2026 Tencent Zhuque Lab. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// Requirement: Any integration or derivative work must explicitly attribute
// Tencent Zhuque Lab (https://github.com/Tencent/AI-Infra-Guard) in its
// documentation or user interface, as detailed in the NOTICE file.

package targets

import "fmt"

// LineError 单行输入的结构化错误:行号 + 原因 + 建议写法。
// Line 为 1-based 行号;0 表示全局性错误(如容量超限)。
type LineError struct {
	Line    int
	Input   string
	Reason  string
	Suggest string
}

func (e LineError) Error() string {
	where := fmt.Sprintf("第 %d 行", e.Line)
	if e.Line == 0 {
		where = "输入"
	}
	msg := where
	if e.Input != "" {
		msg += fmt.Sprintf(" %q", e.Input)
	}
	msg += ": " + e.Reason
	if e.Suggest != "" {
		msg += "(" + e.Suggest + ")"
	}
	return msg
}

// LogSummary 保留行号与原因，不记录可能带凭据的原目标文本。
func (e LineError) LogSummary() string {
	where := fmt.Sprintf("第 %d 行", e.Line)
	if e.Line == 0 {
		where = "整体输入"
	}
	return where + ": " + e.Reason
}
