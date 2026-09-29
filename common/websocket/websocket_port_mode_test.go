// Copyright (c) 2024-2026 Tencent Zhuque Lab. All rights reserved.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package websocket

import (
	"testing"

	"github.com/Tencent/AI-Infra-Guard/common/portscan"
)

func TestWebSocketPortModeDefaultAndNormalization(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  string
		bad   bool
	}{
		{"", portscan.ModeFullTCP, false},
		{portscan.ModeFullTCP, portscan.ModeFullTCP, false},
		{portscan.ModeFixedAI, portscan.ModeFixedAI, false},
		{"invalid", "", true},
	} {
		got, err := normalizeWebSocketPortMode(tc.input)
		if (err != nil) != tc.bad || got != tc.want {
			t.Errorf("normalizeWebSocketPortMode(%q) = %q, %v; want %q, bad=%v", tc.input, got, err, tc.want, tc.bad)
		}
	}
	if portscan.Ports(portscan.ModeFixedAI) != "11434,1337,7000-9000,18789" {
		t.Fatalf("fixed_ai ports changed: %q", portscan.Ports(portscan.ModeFixedAI))
	}
}
