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

import (
	"strings"
	"testing"
)

// TestParseSupported 用例对齐《AI 基础设施扫描输入格式清单》支持格式表。
func TestParseSupported(t *testing.T) {
	cases := []struct {
		name    string
		lines   []string
		wantN   int             // 展开去重后目标数;-1 表示不校验
		wantK0  Kind            // 第一个目标的 Kind
		wantV0  string          // 第一个目标的 Value(空串不校验)
		wantVL0 string          // 最后一个目标的 Value(空串不校验)
	}{
		{name: "http url", lines: []string{"http://ai.example.com"}, wantN: 1, wantK0: KindURL, wantV0: "http://ai.example.com"},
		{name: "https url", lines: []string{"https://ai.example.com"}, wantN: 1, wantK0: KindURL},
		{name: "url with port", lines: []string{"http://192.168.10.2:11434"}, wantN: 1, wantK0: KindURL},
		{name: "url with path", lines: []string{"http://192.168.10.2:11434/api/version"}, wantN: 1, wantK0: KindURL},
		{name: "url with query", lines: []string{"https://ai.example.com/search?q=test"}, wantN: 1, wantK0: KindURL},
		{name: "url wildcard in path not expanded", lines: []string{"https://ai.example.com/api/*"}, wantN: 1, wantK0: KindURL},
		{name: "domain", lines: []string{"ai.example.com"}, wantN: 1, wantK0: KindHost},
		{name: "domain with port", lines: []string{"ai.example.com:8080"}, wantN: 1, wantK0: KindHostPort},
		{name: "single ipv4", lines: []string{"192.168.10.2"}, wantN: 1, wantK0: KindIPv4},
		{name: "ipv4 with port", lines: []string{"192.168.10.2:11434"}, wantN: 1, wantK0: KindIPv4Port},
		{name: "ipv4 range", lines: []string{"192.168.10.2-192.168.10.10"}, wantN: 9, wantK0: KindIPv4, wantV0: "192.168.10.2", wantVL0: "192.168.10.10"},
		{name: "cross subnet range", lines: []string{"192.168.10.250-192.168.11.5"}, wantN: 12, wantV0: "192.168.10.250", wantVL0: "192.168.11.5"},
		{name: "range same ends", lines: []string{"192.168.10.2-192.168.10.2"}, wantN: 1, wantV0: "192.168.10.2"},
		{name: "cidr /32", lines: []string{"192.168.10.2/32"}, wantN: 1, wantV0: "192.168.10.2"},
		{name: "cidr /30 includes network and broadcast", lines: []string{"192.168.10.0/30"}, wantN: 4, wantV0: "192.168.10.0", wantVL0: "192.168.10.3"},
		{name: "cidr /24", lines: []string{"192.168.10.0/24"}, wantN: 256},
		{name: "cidr /16 at unique limit", lines: []string{"192.168.0.0/16"}, wantN: 65536},
		{name: "cidr normalized to network start", lines: []string{"192.168.10.2/24"}, wantN: 256, wantV0: "192.168.10.0"},
		{name: "last segment wildcard", lines: []string{"192.168.10.*"}, wantN: 256, wantV0: "192.168.10.0", wantVL0: "192.168.10.255"},
		{name: "last two segments wildcard", lines: []string{"192.168.*.*"}, wantN: 65536},
		{name: "ipv6 url accepted as single url", lines: []string{"https://[2001:db8::1]:443/path"}, wantN: 1, wantK0: KindURL},
		{name: "empty lines ignored", lines: []string{"", "  ", "192.168.10.2", ""}, wantN: 1},
		{name: "whitespace trimmed", lines: []string{"  192.168.10.2  "}, wantN: 1, wantK0: KindIPv4},
		{name: "mixed multi-line list", lines: []string{
			"https://ai.example.com",
			"http://192.168.10.2:11434",
			"model-gateway.example.com:8080",
			"192.168.20.5",
			"192.168.30.2-192.168.30.10",
			"192.168.40.0/30",
			"192.168.50.*",
		}, wantN: 1 + 1 + 1 + 1 + 9 + 4 + 256, wantK0: KindURL},
		{name: "overlap dedup keeps first occurrence", lines: []string{"192.168.10.2", "192.168.10.2-192.168.10.10"}, wantN: 9, wantV0: "192.168.10.2"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Parse(tc.lines)
			if err != nil {
				t.Fatalf("unexpected error: %v, issues: %v", err, res.Issues)
			}
			if tc.wantN >= 0 && len(res.Targets) != tc.wantN {
				t.Fatalf("got %d targets, want %d", len(res.Targets), tc.wantN)
			}
			if len(res.Issues) != 0 {
				t.Fatalf("unexpected issues: %v", res.Issues)
			}
			nonEmpty := 0
			for _, l := range tc.lines {
				if strings.TrimSpace(l) != "" {
					nonEmpty++
				}
			}
			if res.RawCount != nonEmpty {
				t.Fatalf("raw count = %d, want %d", res.RawCount, nonEmpty)
			}
			if tc.wantV0 != "" && res.Targets[0].Value != tc.wantV0 {
				t.Fatalf("first value = %s, want %s", res.Targets[0].Value, tc.wantV0)
			}
			if tc.wantK0 != 0 && res.Targets[0].Kind != tc.wantK0 {
				t.Fatalf("first kind = %d, want %d", res.Targets[0].Kind, tc.wantK0)
			}
			if tc.wantVL0 != "" && res.Targets[len(res.Targets)-1].Value != tc.wantVL0 {
				t.Fatalf("last value = %s, want %s", res.Targets[len(res.Targets)-1].Value, tc.wantVL0)
			}
		})
	}
}

// TestParseUnsupported 用例对齐《AI 基础设施扫描输入格式清单》不支持写法表:
// 每种写法必须显式报错,不允许静默失败。
func TestParseUnsupported(t *testing.T) {
	cases := []struct {
		name     string
		line     string
		wantSub  string // 错误文案必须包含的关键字
	}{
		{name: "abbreviated range", line: "192.168.10.2-10", wantSub: "完整 IPv4"},
		{name: "spaces inside range", line: "192.168.10.2 - 192.168.10.10", wantSub: "空格"},
		{name: "tilde range", line: "192.168.10.2~192.168.10.10", wantSub: "波浪号"},
		{name: "reversed range", line: "192.168.10.10-192.168.10.2", wantSub: "起始地址不能大于结束地址"},
		{name: "range with ports", line: "192.168.10.2:80-192.168.10.10:80", wantSub: "地址范围携带端口"},
		{name: "port range in target", line: "192.168.10.2:8000-9000", wantSub: "端口范围表达式"},
		{name: "cidr with port", line: "192.168.10.0/24:11434", wantSub: "CIDR 与端口组合"},
		{name: "wildcard with port", line: "192.168.10.*:11434", wantSub: "通配符与端口组合"},
		{name: "digit after wildcard", line: "192.168.*.10", wantSub: "固定数字段"},
		{name: "domain wildcard", line: "*.example.com", wantSub: "域名通配符"},
		{name: "three wildcard segments", line: "10.*.*.*", wantSub: "通配符段数过多"},
		{name: "oversized cidr", line: "10.0.0.0/8", wantSub: "超过单任务上限"},
		{name: "space separated targets", line: "192.168.10.2 192.168.10.3", wantSub: "空格分隔"},
		{name: "comma separated targets", line: "192.168.10.2,192.168.10.3", wantSub: "逗号"},
		{name: "range inside url", line: "http://192.168.10.2-192.168.10.10", wantSub: "URL 中的范围不会展开"},
		{name: "bare ipv6", line: "2001:db8::1", wantSub: "IPv6"},
		{name: "ipv6 endpoint with port", line: "[2001:db8::1]:443", wantSub: "IPv6"},
		{name: "ipv6 cidr", line: "2001:db8::/64", wantSub: "IPv6 网段"},
		{name: "ipv6 range", line: "2001:db8::1-2001:db8::2", wantSub: "IPv6"},
		{name: "invalid port", line: "192.168.10.2:", wantSub: "无效端口"},
		{name: "zero port", line: "192.168.10.2:0", wantSub: "无效端口"},
		{name: "oversized port", line: "192.168.10.2:70000", wantSub: "无效端口"},
		{name: "url wildcard host", line: "http://192.168.10.*:8080", wantSub: "URL 主机不支持通配符"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Parse([]string{tc.line})
			if err == nil {
				t.Fatalf("expected error for %q, got nil (targets=%v)", tc.line, res.Targets)
			}
			if len(res.Issues) != 1 {
				t.Fatalf("expected 1 issue, got %d: %v", len(res.Issues), res.Issues)
			}
			if res.Issues[0].Line != 1 {
				t.Fatalf("expected line 1, got %d", res.Issues[0].Line)
			}
			if !strings.Contains(res.Issues[0].Error(), tc.wantSub) {
				t.Fatalf("error %q does not contain %q", res.Issues[0].Error(), tc.wantSub)
			}
		})
	}
}

// TestParseLimits 容量边界:原始表达式上限、单表达式展开上限、混合输入超限。
func TestParseLimits(t *testing.T) {
	// 单网段恰好 65536 允许,追加任一不重叠目标即拒绝
	res, err := Parse([]string{"192.168.0.0/16"})
	if err != nil {
		t.Fatalf("/16 should pass: %v", err)
	}
	if len(res.Targets) != 65536 {
		t.Fatalf("/16 expanded to %d", len(res.Targets))
	}
	res, err = Parse([]string{"192.168.0.0/16", "10.0.0.1"})
	if err == nil {
		t.Fatal("expected unique limit error when exceeding /16")
	}
	if len(res.Issues) == 0 || res.Issues[0].Line != 0 {
		t.Fatalf("expected global issue, got %v", res.Issues)
	}

	// 跨度 65537 的范围被拒
	if _, err = Parse([]string{"192.168.0.0-192.169.0.0"}); err == nil {
		t.Fatal("expected range expansion limit error")
	}

	// 原始表达式数量上限
	lines := make([]string, MaxRawTargets+1)
	for i := range lines {
		lines[i] = "192.168.10.2"
	}
	if _, err = Parse(lines); err == nil {
		t.Fatal("expected raw count limit error")
	}
}

// TestParseIssuesCarried 混合有效与无效行:错误行不静默丢弃,有效目标仍带明细返回。
func TestParseIssuesCarried(t *testing.T) {
	res, err := Parse([]string{"192.168.10.2", "192.168.10.2-10", "ai.example.com"})
	if err == nil {
		t.Fatal("expected error for invalid line")
	}
	if len(res.Issues) != 1 || res.Issues[0].Line != 2 {
		t.Fatalf("expected issue on line 2, got %v", res.Issues)
	}
	if res.Issues[0].Input != "192.168.10.2-10" {
		t.Fatalf("issue input = %q, want original invalid line", res.Issues[0].Input)
	}
	if len(res.Targets) != 2 {
		t.Fatalf("expected 2 valid targets carried, got %d", len(res.Targets))
	}
}

// TestParseIssueLogSummary 不把含令牌的原输入写入持久化扫描日志。
func TestParseIssueLogSummary(t *testing.T) {
	for _, line := range []string{
		"example.com:secret-token",
		"https://192.168.10.2-192.168.10.10/?token=secret-token",
	} {
		res, err := Parse([]string{line})
		if err == nil || len(res.Issues) != 1 {
			t.Fatalf("expected one issue for %q: %v", line, err)
		}
		issue := res.Issues[0]
		if issue.Input != line {
			t.Fatalf("original input not retained: %q", issue.Input)
		}
		if strings.Contains(issue.LogSummary(), "secret-token") {
			t.Fatalf("log summary leaked target input: %q", issue.LogSummary())
		}
	}
}
