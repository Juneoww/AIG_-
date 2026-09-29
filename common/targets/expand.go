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

// IPv4 地址范围、CIDR 网段与末尾通配符的展开逻辑(仅 IPv4,按 uint32 位运算)。

package targets

import (
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// expandRangeTargets 展开完整 IPv4 地址范围,含两端。
func expandRangeTargets(line string) ([]Target, *LineError) {
	m := reFullRange.FindStringSubmatch(line)
	start, err1 := ipv4ToUint32(m[1])
	end, err2 := ipv4ToUint32(m[2])
	if err1 != nil || err2 != nil {
		return nil, &LineError{Reason: "无效的 IPv4 地址范围"}
	}
	if start > end {
		return nil, &LineError{
			Reason:  "起始地址不能大于结束地址",
			Suggest: "例如 192.168.10.2-192.168.10.10",
		}
	}
	if uint64(end-start+1) > maxExpandOnce {
		return nil, &LineError{
			Reason:  fmt.Sprintf("地址范围展开数量 %d 超过单任务上限 %d", uint64(end-start+1), maxExpandOnce),
			Suggest: "拆分为多个范围或缩小跨度",
		}
	}
	ts := make([]Target, 0, end-start+1)
	for v := start; ; v++ {
		s := uint32ToIPv4(v)
		ts = append(ts, Target{Kind: KindIPv4, Raw: line, Value: s})
		if v == end {
			break
		}
	}
	return ts, nil
}

// classifyCIDR 校验并展开 CIDR 网段;归一到网络起点,展开含网络地址与广播地址。
func classifyCIDR(line string, ipnet *net.IPNet) ([]Target, *LineError) {
	if ipnet.IP.To4() == nil {
		return nil, &LineError{Reason: "不支持 IPv6 网段和范围展开"}
	}
	maskSize, _ := ipnet.Mask.Size()
	count := uint64(1) << (32 - maskSize)
	if count > maxExpandOnce {
		return nil, &LineError{
			Reason:  fmt.Sprintf("网段展开数量 %d 超过单任务上限 %d", count, maxExpandOnce),
			Suggest: "使用更小的网段,单网段最大 /16",
		}
	}
	start := binary.BigEndian.Uint32(ipnet.IP.To4())
	mask := binary.BigEndian.Uint32(ipnet.Mask)
	finish := (start & mask) | (mask ^ 0xffffffff)
	ts := make([]Target, 0, count)
	for v := start; ; v++ {
		ts = append(ts, Target{Kind: KindIPv4, Raw: line, Value: uint32ToIPv4(v)})
		if v == finish {
			break
		}
	}
	return ts, nil
}

// classifyWildcard 校验并展开末尾通配符;仅支持 IPv4 末尾 1~2 个整段,连续位于末尾。
func classifyWildcard(line string) ([]Target, *LineError) {
	parts := strings.Split(line, ".")
	if len(parts) != 4 {
		return nil, &LineError{
			Reason:  "不支持域名通配符/子域名枚举,或通配符表达式格式无效",
			Suggest: "通配符仅支持 IPv4 末尾 1~2 个整段,如 192.168.10.* 或 192.168.*.*",
		}
	}
	firstStar := -1
	for i, p := range parts {
		if p == "*" {
			if firstStar == -1 {
				firstStar = i
			}
			continue
		}
		if firstStar != -1 {
			return nil, &LineError{
				Reason:  "通配符后不能再出现固定数字段",
				Suggest: "通配符必须占据完整的一段,并连续位于末尾,如 192.168.*.*",
			}
		}
		if v, err := strconv.Atoi(p); err != nil || v < 0 || v > 255 {
			return nil, &LineError{
				Reason:  "通配符表达式无效",
				Suggest: "通配符仅支持 IPv4 末尾 1~2 个整段,如 192.168.10.*",
			}
		}
	}
	stars := len(parts) - firstStar
	if stars > 2 {
		return nil, &LineError{
			Reason:  "通配符段数过多",
			Suggest: "最多末尾两段通配,如 192.168.*.*",
		}
	}

	base := make([]int, firstStar)
	for i, p := range parts[:firstStar] {
		base[i], _ = strconv.Atoi(p)
	}
	ts := make([]Target, 0, 1<<(8*stars))
	switch stars {
	case 1:
		for a := 0; a < 256; a++ {
			s := fmt.Sprintf("%d.%d.%d.%d", base[0], base[1], base[2], a)
			ts = append(ts, Target{Kind: KindIPv4, Raw: line, Value: s})
		}
	case 2:
		for a := 0; a < 256; a++ {
			for b := 0; b < 256; b++ {
				s := fmt.Sprintf("%d.%d.%d.%d", base[0], base[1], a, b)
				ts = append(ts, Target{Kind: KindIPv4, Raw: line, Value: s})
			}
		}
	}
	return ts, nil
}

func ipv4ToUint32(s string) (uint32, error) {
	ip := net.ParseIP(s)
	if ip == nil || ip.To4() == nil {
		return 0, fmt.Errorf("invalid ipv4 %q", s)
	}
	return binary.BigEndian.Uint32(ip.To4()), nil
}

func uint32ToIPv4(v uint32) string {
	ip := make(net.IP, 4)
	binary.BigEndian.PutUint32(ip, v)
	return ip.String()
}
