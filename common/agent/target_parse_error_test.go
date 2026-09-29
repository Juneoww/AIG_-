// Copyright (c) 2024-2026 Tencent Zhuque Lab. All rights reserved.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package agent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	targetparser "github.com/Tencent/AI-Infra-Guard/common/targets"
)

func TestTargetParseErrorCarriesOrderedIssues(t *testing.T) {
	scanner := &AIInfraScanAgent{}
	_, err := scanner.scanPortsAndPrepareTargets(
		[]string{"192.168.10.2-10", "192.168.10.2~192.168.10.10"},
		"", scanTexts{}, TaskCallbacks{}, "full_tcp",
	)
	if err == nil {
		t.Fatal("expected parse error")
	}
	var parseErr *targetParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("error type = %T, want *targetParseError", err)
	}
	if got := err.Error(); got != "目标解析失败,共 2 处无效输入" {
		t.Fatalf("summary = %q", got)
	}
	if len(parseErr.issues) != 2 || parseErr.issues[0].Line != 1 || parseErr.issues[1].Line != 2 {
		t.Fatalf("issues out of order: %+v", parseErr.issues)
	}
	if parseErr.issues[0].Input != "192.168.10.2-10" || parseErr.issues[1].Input != "192.168.10.2~192.168.10.10" {
		t.Fatalf("original input lost: %+v", parseErr.issues)
	}
}

func TestSendTaskErrorIncludesStructuredIssues(t *testing.T) {
	agent := NewAgent(AgentConfig{})
	defer agent.cancel()
	parseErr := &targetParseError{
		err: errors.New("目标解析失败,共 2 处无效输入"),
		issues: []targetparser.LineError{
			{Line: 1, Input: "192.168.10.2-10", Reason: "不支持缩写", Suggest: "使用完整 IPv4"},
			{Line: 0, Input: "10.0.0.0/8", Reason: "超过上限"},
		},
	}
	if err := agent.SendTaskError("session-a", parseErr); err != nil {
		t.Fatal(err)
	}
	got := <-agent.sendChan
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Content struct {
			Event struct {
				Message      string `json:"message"`
				TargetIssues []struct {
					Line    int    `json:"line"`
					Input   string `json:"input"`
					Reason  string `json:"reason"`
					Suggest string `json:"suggest"`
				} `json:"targetIssues"`
			} `json:"event"`
		} `json:"content"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Content.Event.Message != parseErr.Error() {
		t.Fatalf("message = %q", envelope.Content.Event.Message)
	}
	issues := envelope.Content.Event.TargetIssues
	if len(issues) != 2 || issues[0].Line != 1 || issues[1].Line != 0 || issues[1].Input != "10.0.0.0/8" {
		t.Fatalf("JSON issues = %+v; raw=%s", issues, data)
	}
	if issues[0].Suggest != "使用完整 IPv4" || issues[1].Suggest != "" {
		t.Fatalf("JSON suggestions = %+v", issues)
	}
}

func TestSendTaskErrorKeepsGenericEventCompatible(t *testing.T) {
	agent := NewAgent(AgentConfig{})
	defer agent.cancel()
	if err := agent.SendTaskError("session-a", errors.New("普通错误")); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(<-agent.sendChan)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "targetIssues") || !strings.Contains(string(data), `"message":"普通错误"`) {
		t.Fatalf("generic error contract changed: %s", data)
	}
}
