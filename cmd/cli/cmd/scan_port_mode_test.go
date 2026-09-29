// Copyright (c) 2024-2026 Tencent Zhuque Lab. All rights reserved.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package cmd

import "testing"

func TestCLIPortModeDefaultAndNormalization(t *testing.T) {
	flag := scanCmd.Flags().Lookup("port-mode")
	if flag == nil || flag.DefValue != "full_tcp" {
		t.Fatalf("--port-mode default = %v, want full_tcp", flag)
	}
	for _, tc := range []struct {
		input string
		want  string
		bad   bool
	}{
		{"", "full_tcp", false},
		{"full_tcp", "full_tcp", false},
		{"fixed_ai", "fixed_ai", false},
		{"invalid", "", true},
	} {
		got, err := normalizeCLIPortMode(tc.input)
		if (err != nil) != tc.bad || got != tc.want {
			t.Errorf("normalizeCLIPortMode(%q) = %q, %v; want %q, bad=%v", tc.input, got, err, tc.want, tc.bad)
		}
	}
}
