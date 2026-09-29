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

// Package targets 提供扫描目标输入的统一解析:
// 支持 URL、域名、IPv4、指定端口、完整 IPv4 地址范围、CIDR 网段与末尾通配符,
// 多行混合输入、展开去重、容量上限校验,不支持写法逐行返回结构化错误。
package targets

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
)

// Kind 目标类型
type Kind int

const (
	KindURL Kind = iota // http(s)://... 完整 URL,原样透传
	KindHost            // 域名
	KindHostPort        // 域名:端口
	KindIPv4            // 裸 IPv4(含范围/CIDR/通配符展开产物)
	KindIPv4Port        // IPv4:端口
)

// 容量上限,对齐《AI 基础设施扫描输入格式清单》
const (
	MaxRawTargets    = 65536 // 非空原始目标表达式数量上限(去重前)
	MaxUniqueTargets = 65536 // 展开去重后的唯一目标数量上限
	maxExpandOnce    = 65536 // 单个范围/网段/通配符表达式展开上限
)

// Target 解析后的单个目标
type Target struct {
	Kind  Kind
	Raw   string // 原始输入行(已 trim)
	Value string // 归一化后的目标值(URL/域名/IP 或 host:port)
}

// Result 解析结果
type Result struct {
	Targets  []Target    // 展开去重后,保留首次出现顺序
	RawCount int         // 非空原始表达式数量
	Issues   []LineError // 逐行错误明细
}

var (
	reFullRange    = regexp.MustCompile(`^(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})-(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})$`)
	rePartialRange = regexp.MustCompile(`^[\d.]+-[\d.]+$`)
	rePortRange    = regexp.MustCompile(`^\d+-\d+$`)
)

// Parse 解析多行目标输入。
// 忽略空行、去掉每行首尾空白;范围/CIDR/通配符展开为 KindIPv4 目标并稳定去重
// (保留首次出现顺序)。存在任一错误行时返回非 nil error,Result 仍携带明细,
// 调用方应中止本次扫描并把 Issues 呈现给用户,不得静默丢弃。
func Parse(lines []string) (*Result, error) {
	res := &Result{}
	seen := make(map[string]struct{})
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		res.RawCount++
		if res.RawCount > MaxRawTargets {
			res.Issues = append(res.Issues, LineError{
				Line:   0,
				Input:  line,
				Reason: fmt.Sprintf("非空目标表达式数量超过上限 %d", MaxRawTargets),
			})
			return res, errOf(res)
		}
		ts, lerr := classify(line)
		if lerr != nil {
			lerr.Line = i + 1
			lerr.Input = line
			res.Issues = append(res.Issues, *lerr)
			continue
		}
		for _, t := range ts {
			if _, dup := seen[t.Value]; dup {
				continue
			}
			if len(res.Targets) >= MaxUniqueTargets {
				res.Issues = append(res.Issues, LineError{
					Line:   0,
					Input:  line,
					Reason: fmt.Sprintf("展开去重后的唯一目标数量超过上限 %d", MaxUniqueTargets),
				})
				return res, errOf(res)
			}
			seen[t.Value] = struct{}{}
			res.Targets = append(res.Targets, t)
		}
	}
	return res, errOf(res)
}

func errOf(res *Result) error {
	if len(res.Issues) == 0 {
		return nil
	}
	return fmt.Errorf("目标解析失败,共 %d 处无效输入", len(res.Issues))
}

// classify 单行目标分类与展开;错误时返回携带原因与建议写法的 LineError。
func classify(line string) ([]Target, *LineError) {
	// 空格分隔多个目标 / 范围内部含空格
	if len(strings.Fields(line)) > 1 {
		collapsed := strings.Join(strings.Fields(line), "")
		if strings.Contains(collapsed, "-") && rePartialRange.MatchString(collapsed) {
			return nil, &LineError{
				Reason:  "地址范围内部不能有空格",
				Suggest: "使用英文 - 连接两个完整 IPv4,连接符两侧不加空格",
			}
		}
		return nil, &LineError{
			Reason:  "不能用空格分隔多个目标",
			Suggest: "每个目标单独一行",
		}
	}

	lower := strings.ToLower(line)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return classifyURL(line)
	}

	if strings.Contains(line, ",") {
		return nil, &LineError{
			Reason:  "不按逗号拆分多个目标",
			Suggest: "每个目标单独一行",
		}
	}
	if strings.Contains(line, "~") {
		return nil, &LineError{
			Reason:  "不支持波浪号连接",
			Suggest: "地址范围使用英文 - 连接两个完整 IPv4,如 192.168.10.2-192.168.10.10",
		}
	}

	// CIDR 先于 IPv6/冒号判断,使 IPv6 网段获得专属错误提示
	if _, ipnet, err := net.ParseCIDR(line); err == nil {
		return classifyCIDR(line, ipnet)
	}

	// IPv6:裸地址、[v6]:port 或 v6 网段(未走 CIDR 分支的补漏)
	if strings.HasPrefix(line, "[") || strings.Contains(line, "::") || isIPv6(line) {
		return nil, &LineError{
			Reason:  "不支持裸 IPv6 或非 URL 形式的 IPv6 端点",
			Suggest: "使用完整 HTTP(S) URL,如 https://[2001:db8::1]:443/path",
		}
	}

	if strings.Contains(line, ":") {
		return classifyHostPort(line)
	}
	if strings.Contains(line, "*") {
		return classifyWildcard(line)
	}
	if reFullRange.MatchString(line) {
		return expandRangeTargets(line)
	}
	if rePartialRange.MatchString(line) {
		return nil, &LineError{
			Reason:  "地址范围必须使用两个完整 IPv4 和英文连字符 -",
			Suggest: "结束地址不能缩写,例如 192.168.10.2-192.168.10.10",
		}
	}
	if ip := net.ParseIP(line); ip != nil {
		if ip.To4() != nil {
			return []Target{{Kind: KindIPv4, Raw: line, Value: line}}, nil
		}
		return nil, &LineError{Reason: "不支持裸 IPv6"}
	}
	return []Target{{Kind: KindHost, Raw: line, Value: line}}, nil
}

// classifyURL URL 目标:整体透传,仅拦截主机部分的通配符与范围。
func classifyURL(line string) ([]Target, *LineError) {
	rest := line[strings.Index(line, "://")+3:]
	hostPart := rest
	if j := strings.IndexAny(rest, "/?"); j >= 0 {
		hostPart = rest[:j]
	}
	if hostPart == "" {
		return nil, &LineError{Reason: "URL 缺少主机部分"}
	}
	host := hostPart
	if k := strings.LastIndex(hostPart, ":"); k >= 0 {
		host = hostPart[:k]
	}
	if strings.Contains(host, "*") {
		return nil, &LineError{
			Reason:  "URL 主机不支持通配符",
			Suggest: "URL 路径或查询中的 * 不作为 IP 通配符展开",
		}
	}
	if strings.Contains(host, "-") && rePartialRange.MatchString(host) {
		return nil, &LineError{
			Reason:  "URL 中的范围不会展开",
			Suggest: "填写不带协议的完整 IP 范围,如 192.168.10.2-192.168.10.10",
		}
	}
	return []Target{{Kind: KindURL, Raw: line, Value: line}}, nil
}

// classifyHostPort host:port 目标;拦截范围/CIDR/通配符与端口组合及端口范围表达式。
func classifyHostPort(line string) ([]Target, *LineError) {
	parts := strings.SplitN(line, ":", 2)
	host, portStr := parts[0], parts[1]
	if strings.Contains(portStr, ":") {
		return nil, &LineError{
			Reason:  "不支持地址范围携带端口",
			Suggest: "逐行列出具体端点,如 192.168.10.2:80",
		}
	}
	if port, perr := strconv.Atoi(portStr); perr == nil {
		if port < 1 || port > 65535 {
			return nil, &LineError{Reason: "无效端口"}
		}
	} else if rePortRange.MatchString(portStr) {
		return nil, &LineError{
			Reason:  "不支持目标内的端口范围表达式",
			Suggest: "逐行列出具体端点,如 192.168.10.2:8000 与 192.168.10.2:9000",
		}
	} else {
		return nil, &LineError{Reason: "无效端口"}
	}
	if _, _, err := net.ParseCIDR(host); err == nil {
		return nil, &LineError{
			Reason:  "不支持 CIDR 与端口组合",
			Suggest: "网段目标不带端口,逐行列出具体 IP:端口",
		}
	}
	if strings.Contains(host, "*") {
		return nil, &LineError{
			Reason:  "不支持通配符与端口组合",
			Suggest: "逐行列出具体 IP:端口",
		}
	}
	if reFullRange.MatchString(host) || rePartialRange.MatchString(host) {
		return nil, &LineError{
			Reason:  "不支持地址范围携带端口",
			Suggest: "逐行列出具体端点,如 192.168.10.2:80",
		}
	}
	if host == "" {
		return nil, &LineError{Reason: "无效目标"}
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.To4() != nil {
			return []Target{{Kind: KindIPv4Port, Raw: line, Value: line}}, nil
		}
		return nil, &LineError{Reason: "不支持裸 IPv6 或非 URL 形式的 IPv6 端点"}
	}
	return []Target{{Kind: KindHostPort, Raw: line, Value: line}}, nil
}

func isIPv6(line string) bool {
	ip := net.ParseIP(line)
	return ip != nil && ip.To4() == nil
}
