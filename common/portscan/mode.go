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

// Package portscan 定义端口发现模式:仅作用于裸 IPv4 目标(含范围/CIDR/通配符
// 展开产物);URL、域名及带端口目标走 Web 扫描路径,不做端口发现。
package portscan

import "fmt"

// 端口发现模式
const (
	ModeFullTCP = "full_tcp" // 全量 TCP 端口 1-65535
	ModeFixedAI = "fixed_ai" // AI 框架常用固定端口
)

// ModeDefault Web/Agent 路径的默认模式(与 v1.0.0 全端口行为一致)
const ModeDefault = ModeFullTCP

// FullTCPPorts 全量 TCP 端口范围
const FullTCPPorts = "1-65535"

// FixedAIPorts AI 框架常用端口清单,共 2,004 个
const FixedAIPorts = "11434,1337,7000-9000,18789"

// Normalize 校验并归一端口模式;空值返回 defaultMode。
// 用户入口传 ModeDefault；Agent 已执行端口发现后的内部 Runner 可保留空值跳过二次扫描。
// 非法值显式报错,不允许静默回退。
func Normalize(mode, defaultMode string) (string, error) {
	if mode == "" {
		return defaultMode, nil
	}
	if mode == ModeFullTCP || mode == ModeFixedAI {
		return mode, nil
	}
	return "", fmt.Errorf("无效的端口发现模式 %q,支持: full_tcp / fixed_ai", mode)
}

// Ports 返回模式对应的 nmap 端口参数
func Ports(mode string) string {
	if mode == ModeFixedAI {
		return FixedAIPorts
	}
	return FullTCPPorts
}
