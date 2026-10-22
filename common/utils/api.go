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

package utils

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Tencent/AI-Infra-Guard/common/fingerprints/parser"
)

// DownloadFile 下载文件
// path 参数必须由调用方在调用前完成路径安全校验（防止路径穿越），
// 本函数仅负责 HTTP 下载写入，不做路径验证。
func DownloadFile(server, sessionId, uri, path string) error {
	return DownloadFileContext(context.Background(), server, sessionId, uri, path)
}

// Context 入口将初始化/附件 HTTP 请求绑定到任务取消，并保留可配置操作截止。
func DownloadFileContext(ctx context.Context, server, sessionId, uri, path string) error {
	ctx, cancel, timeoutErr := serviceHTTPContext(ctx)
	if timeoutErr != nil {
		return timeoutErr
	}
	defer cancel()

	// Validate that path is not empty and does not contain path traversal sequences.
	// Callers are responsible for ensuring path is within an expected directory.
	if path == "" || strings.Contains(path, "..") {
		return fmt.Errorf("非法文件路径")
	}
	// 创建 HTTP 客户端
	client := &http.Client{}

	data := map[string]string{
		"fileUrl": uri,
	}
	jsonData, err := json.Marshal(data)
	// 创建请求并添加 header
	req, err := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("http://%s/api/v1/app/tasks/%s/downloadFile", server, sessionId), io.NopCloser(bytes.NewBuffer(jsonData)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-APIKey", "zhuque")

	// 发送 POST 请求
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// 检查 HTTP 状态码
	if resp.StatusCode != http.StatusOK {
		dd, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("下载失败，HTTP 状态码：%d content:%s", resp.StatusCode, string(dd))
	}

	// 创建文件
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	// 将响应体复制到文件
	_, err = io.Copy(file, resp.Body)
	if err != nil {
		return err
	}

	return nil
}

// UploadFileResponse 上传文件响应结构
type UploadFileResponse struct {
	Status  int    `json:"status"`
	Message string `json:"message"`
	Data    struct {
		FileUrl  string `json:"fileUrl"`
		Filename string `json:"filename"`
	} `json:"data"`
}

// UploadFile 上传文件到服务器
func UploadFile(server, filePath string) (*UploadFileResponse, error) {
	timeout := 120 * time.Second
	if raw := os.Getenv("AIG_UPLOAD_TIMEOUT"); raw != "" {
		value, err := time.ParseDuration(raw)
		if err != nil || value <= 0 {
			return nil, fmt.Errorf("AIG_UPLOAD_TIMEOUT must be a positive duration")
		}
		timeout = value
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return UploadFileContext(ctx, server, filePath)
}

// UploadFileContext 把上传及响应读取绑定到任务取消和调用方截止。
func UploadFileContext(ctx context.Context, server, filePath string) (*UploadFileResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// 打开文件
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("无法打开文件: %v", err)
	}
	defer file.Close()

	// 创建 multipart writer
	var requestBody bytes.Buffer
	writer := multipart.NewWriter(&requestBody)

	// 创建文件字段
	part, err := writer.CreateFormFile("file", filepath.Base(filePath))
	if err != nil {
		return nil, fmt.Errorf("创建文件字段失败: %v", err)
	}

	// 将文件内容复制到 part
	_, err = io.Copy(part, file)
	if err != nil {
		return nil, fmt.Errorf("复制文件内容失败: %v", err)
	}

	// 关闭 writer
	err = writer.Close()
	if err != nil {
		return nil, fmt.Errorf("关闭 writer 失败: %v", err)
	}

	// 创建 HTTP 请求
	req, err := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("http://%s/api/v1/app/tasks/uploadFile", server), &requestBody)
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %v", err)
	}

	// 设置 Content-Type
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-APIKey", "zhuque")

	// 发送请求
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("发送请求失败: %w", err)
	}
	defer resp.Body.Close()

	// 读取响应体
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}

	// 检查 HTTP 状态码
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("上传失败，HTTP 状态码：%d content:%s", resp.StatusCode, string(respBody))
	}

	// 解析响应 JSON
	var uploadResp UploadFileResponse
	err = json.Unmarshal(respBody, &uploadResp)
	if err != nil {
		return nil, fmt.Errorf("解析响应 JSON 失败: %v", err)
	}

	return &uploadResp, nil
}

func GetEvaluationsDetail(server, name string) ([]byte, error) {
	return GetEvaluationsDetailContext(context.Background(), server, name)
}

func GetEvaluationsDetailContext(ctx context.Context, server, name string) ([]byte, error) {
	ctx, cancel, err := serviceHTTPContext(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()

	path := "/api/v1/knowledge/evaluations/" + name
	// 创建 HTTP 请求
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://%s%s", server, path), nil)
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("X-APIKey", "zhuque")

	// 发送请求
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("发送请求失败: %w", err)
	}
	defer resp.Body.Close()

	// 读取响应体
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}

	// 检查 HTTP 状态码
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("上传失败，HTTP 状态码：%d content:%s", resp.StatusCode, string(respBody))
	}

	var msg struct {
		Data json.RawMessage `json:"data"`
	}
	err = json.Unmarshal(respBody, &msg)
	if err != nil {
		return nil, fmt.Errorf("解析响应 JSON 失败: %v", err)
	}
	return msg.Data, nil
}

func LoadRemoteFingerPrints(hostname string) ([]parser.FingerPrint, error) {
	return LoadRemoteFingerPrintsContext(context.Background(), hostname)
}

// Context 入口将初始化/附件 HTTP 请求绑定到任务取消，并保留可配置操作截止。
func LoadRemoteFingerPrintsContext(ctx context.Context, hostname string) ([]parser.FingerPrint, error) {
	ctx, cancel, timeoutErr := serviceHTTPContext(ctx)
	if timeoutErr != nil {
		return nil, timeoutErr
	}
	defer cancel()

	type msg struct {
		Data struct {
			FingerPrints []json.RawMessage `json:"items"`
			Total        int               `json:"total"`
		} `json:"data"`
		Message string `json:"message"`
	}
	// 创建请求并添加 header
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://%s/api/v1/knowledge/fingerprints?page=1&size=9999", hostname), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-APIKey", "zhuque")

	// 发送请求
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http status code: %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var m msg
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	fps := make([]parser.FingerPrint, 0)
	for _, raw := range m.Data.FingerPrints {
		fp, err := parser.InitFingerPrintFromData(raw)
		if err != nil {
			return nil, fmt.Errorf("无法解析远程指纹模板: %w", err)
		}
		fps = append(fps, *fp)
	}
	return fps, nil
}

func LoadRemoteVulStruct(api string) ([]json.RawMessage, error) {
	return LoadRemoteVulStructContext(context.Background(), api)
}

// Context 入口将初始化/附件 HTTP 请求绑定到任务取消，并保留可配置操作截止。
func LoadRemoteVulStructContext(ctx context.Context, api string) ([]json.RawMessage, error) {
	ctx, cancel, timeoutErr := serviceHTTPContext(ctx)
	if timeoutErr != nil {
		return nil, timeoutErr
	}
	defer cancel()

	type msg struct {
		Data struct {
			Vuls  []json.RawMessage `json:"items"`
			Total int               `json:"total"`
		} `json:"data"`
		Message string `json:"message"`
	}
	// 创建请求并添加 header
	req, err := http.NewRequestWithContext(ctx, "GET", api, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-APIKey", "zhuque")

	// 发送请求
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http status code: %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var m msg
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m.Data.Vuls, nil
}

// serviceHTTPContext 为服务内部知识库和附件访问提供独立操作时限。
func serviceHTTPContext(parent context.Context) (context.Context, context.CancelFunc, error) {
	if parent == nil {
		parent = context.Background()
	}
	timeout := 120 * time.Second
	if value := os.Getenv("AIG_SERVICE_HTTP_TIMEOUT"); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed <= 0 {
			return nil, nil, fmt.Errorf("AIG_SERVICE_HTTP_TIMEOUT must be a positive duration")
		}
		timeout = parsed
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	return ctx, cancel, nil
}
