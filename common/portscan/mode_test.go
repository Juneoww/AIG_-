// Copyright (c) 2024-2026 Tencent Zhuque Lab. All rights reserved.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package portscan

import "testing"

func TestNormalizePortModeAndPortLists(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  string
		bad   bool
	}{
		{"", ModeDefault, false},
		{ModeFullTCP, ModeFullTCP, false},
		{ModeFixedAI, ModeFixedAI, false},
		{"other", "", true},
	} {
		got, err := Normalize(tc.input, ModeDefault)
		if (err != nil) != tc.bad || got != tc.want {
			t.Errorf("Normalize(%q) = %q, %v; want %q, bad=%v", tc.input, got, err, tc.want, tc.bad)
		}
	}
	if Ports(ModeFullTCP) != "1-65535" || Ports(ModeFixedAI) != "11434,1337,7000-9000,18789" {
		t.Fatalf("unexpected port lists: full=%q fixed=%q", Ports(ModeFullTCP), Ports(ModeFixedAI))
	}
}
