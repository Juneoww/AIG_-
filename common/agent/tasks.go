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

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Tencent/AI-Infra-Guard/pkg/vulstruct"

	"github.com/Tencent/AI-Infra-Guard/common/utils"

	"github.com/Tencent/AI-Infra-Guard/common/utils/models"

	"github.com/Tencent/AI-Infra-Guard/common/portscan"
	"github.com/Tencent/AI-Infra-Guard/common/runner"
	targetparser "github.com/Tencent/AI-Infra-Guard/common/targets"
	"github.com/Tencent/AI-Infra-Guard/internal/gologger"
	"github.com/Tencent/AI-Infra-Guard/internal/options"
	"github.com/google/uuid"
)

// ResultCallback 任务结果回调函数类型
type ResultCallback func(result map[string]interface{})

// ActionLogCallback 插件日志回调函数类型
type ActionLogCallback func(actionId, tool, planStepId, actionLog string)

// ToolUsedCallback 插件工作状态回调函数类型
type ToolUsedCallback func(planStepId, statusId, description string, tools []Tool)

// NewPlanStepCallback 新建执行步骤回调函数类型
type NewPlanStepCallback func(stepId, title string)

// StatusUpdateCallback 更新步骤状态回调函数类型
type StatusUpdateCallback func(planStepId, statusId, agentStatus, brief, description string)

// PlanUpdateCallback 更新任务计划回调函数类型
type PlanUpdateCallback func(tasks []SubTask)

type ErrorCallback func(error string)

// TaskCallbacks 任务回调函数集合
type TaskCallbacks struct {
	ResultCallback           ResultCallback       // 任务结果回调
	ToolUseLogCallback       ActionLogCallback    // 插件日志回调
	ToolUsedCallback         ToolUsedCallback     // 插件状态回调
	NewPlanStepCallback      NewPlanStepCallback  // 新建执行步骤回调
	StepStatusUpdateCallback StatusUpdateCallback // 更新步骤状态回调
	PlanUpdateCallback       PlanUpdateCallback   // 更新任务计划回调
	ErrorCallback            ErrorCallback        // 错误回调
}

type TaskInterface interface {
	GetName() string
	Execute(ctx context.Context, request TaskRequest, callbacks TaskCallbacks) error
}

// ScanRequest 扫描请求结构
type ScanRequest struct {
	Target   []string          `json:"-"`
	Headers  map[string]string `json:"headers"`
	Timeout  int               `json:"timeout,omitempty"`
	PortMode string            `json:"port_mode,omitempty"` // 端口发现模式:full_tcp / fixed_ai;空值取默认 full_tcp
	Model    struct {
		Model   string `json:"model"`
		Token   string `json:"token"`
		BaseUrl string `json:"base_url"`
	} `json:"model,omitempty"`
}

type AIInfraScanAgent struct {
	Server string
}

func (t *AIInfraScanAgent) GetName() string {
	return TaskTypeAIInfraScan
}
func (t *AIInfraScanAgent) Execute(ctx context.Context, request TaskRequest, callbacks TaskCallbacks) error {
	var reqScan ScanRequest
	if len(request.Params) > 0 {
		if err := json.Unmarshal(request.Params, &reqScan); err != nil {
			return err
		}
	}

	language := request.Language
	if language == "" {
		language = "zh"
	}

	// 初始化语言文本
	texts := initTexts(language)

	// 处理目标和附件
	targets, err := t.prepareTargetsContext(ctx, request, reqScan, texts)
	if err != nil {
		return err
	}
	reqScan.Target = targets

	if reqScan.Timeout == 0 {
		reqScan.Timeout = 30
	}

	// 判断是否使用AI模式
	useAI := reqScan.Model.Token != "" || reqScan.Model.BaseUrl != ""
	var model *models.OpenAI
	if useAI {
		if reqScan.Model.BaseUrl == "" && reqScan.Model.Model == "" {
			return fmt.Errorf("model parameters are required")
		}
		model = &models.OpenAI{
			BaseUrl: reqScan.Model.BaseUrl,
			Model:   reqScan.Model.Model,
			Key:     reqScan.Model.Token,
		}
	}

	return t.executeScan(ctx, request, reqScan, texts, callbacks, model)
}

// scanTexts 包含所有语言相关的文本
type scanTexts struct {
	initEnv, execScan, genReport, aigWorking, aigCompleted, initConfig, portDetection, targetConfig, scanComplete, reportGen                               string
	createTempDir, downloadFile, readFile, portScan, foundPort, portCount, targetCount, foundVuln, noVuln, execError, config, scanResult                   string
	initDescTemplate, portDetectDescTemplate, portCompleteTemplate, targetCountTemplate, webAppTemplate, vulnFoundTemplate, noVulnTemplate, errorTemplate  string
	scanningDesc, execScanDesc, scanCompleteDesc, taskCompleteDesc, reportGenDesc, reportGenToolDesc, scanResultTemplate                                   string
	downloadFileLog, execScanTool, scanTool, generateReportTool, nmapTool, aiScannerTool, reportGeneratorTool, scanOperation, targetSystem, generateReport string
}

// initTexts 初始化语言文本
func initTexts(language string) scanTexts {
	var texts scanTexts
	if language == "en" {
		texts.initEnv = "Initialize scan environment"
		texts.execScan = "Execute AI infrastructure scan"
		texts.genReport = "Generate scan report"
		texts.aigWorking = "A.I.G is working"
		texts.aigCompleted = "A.I.G completed work"
		texts.initConfig = "Initialization configuration completed"
		texts.portDetection = "Auto-detecting ports"
		texts.targetConfig = "Target configuration completed"
		texts.scanComplete = "Scan completed"
		texts.reportGen = "Generating scan report"
		texts.createTempDir = "Failed to create temporary directory"
		texts.downloadFile = "Failed to download file"
		texts.readFile = "Failed to read file"
		texts.portScan = "Port scan"
		texts.foundPort = "Found port"
		texts.portCount = "Port count"
		texts.targetCount = "Target count"
		texts.foundVuln = "Found vulnerabilities"
		texts.noVuln = "Scan completed, no vulnerabilities found"
		texts.execError = "Execution error"
		texts.config = "Configuration"
		texts.scanResult = "Scan result"
		texts.initDescTemplate = "Starting to initialize AI infrastructure scan environment"
		texts.portDetectDescTemplate = "Auto-detecting IP: %s"
		texts.portCompleteTemplate = "%s port detection completed"
		texts.targetCountTemplate = "Target count: %d"
		texts.webAppTemplate = "WEB Application: %s "
		texts.vulnFoundTemplate = "URL:%s %sFound vulnerabilities:%d\n"
		texts.noVulnTemplate = "URL:%s %sScan completed, no vulnerabilities found\n"
		texts.errorTemplate = "Execution error: host:%s %s\n"
		texts.scanningDesc = "Scanning..."
		texts.execScanDesc = "Executing AI infrastructure scan"
		texts.scanCompleteDesc = "Scan completed"
		texts.taskCompleteDesc = "AI infrastructure scan task completed"
		texts.reportGenDesc = "Generating scan report"
		texts.reportGenToolDesc = "Generating scan report"
		texts.scanResultTemplate = "Scan results: %d items"
		texts.downloadFileLog = "Starting to download file: %s"
		texts.execScanTool = "Execute scan"
		texts.scanTool = "Scan"
		texts.generateReportTool = "Generate report"
		texts.nmapTool = "nmap"
		texts.aiScannerTool = "ai_scanner"
		texts.reportGeneratorTool = "report_generator"
		texts.scanOperation = "Scan"
		texts.targetSystem = "Target system"
		texts.generateReport = "Generate report"
	} else {
		texts.initEnv = "准备扫描环境"
		texts.execScan = "执行深度扫描"
		texts.genReport = "智能分析与报告生成"
		texts.aigWorking = "Agent正在工作"
		texts.aigCompleted = "Agent完成工作"
		texts.initConfig = "初始化配置完成"
		texts.portDetection = "正在自动识别端口"
		texts.targetConfig = "目标配置完成"
		texts.scanComplete = "扫描完成"
		texts.reportGen = "生成扫描报告"
		texts.createTempDir = "创建临时目录失败"
		texts.downloadFile = "下载文件失败"
		texts.readFile = "读取文件失败"
		texts.portScan = "端口扫描"
		texts.foundPort = "发现端口"
		texts.portCount = "端口数量"
		texts.targetCount = "目标数量"
		texts.foundVuln = "发现漏洞"
		texts.noVuln = "扫描完成,未发现漏洞"
		texts.execError = "执行错误"
		texts.config = "配置"
		texts.scanResult = "Agent发现"
		texts.initDescTemplate = "开始初始化AI基础设施扫描环境"
		texts.portDetectDescTemplate = "正在自动识别IP: %s"
		texts.portCompleteTemplate = "%s 端口探测完成"
		texts.targetCountTemplate = "目标数量: %d"
		texts.webAppTemplate = "WEB应用: %s "
		texts.vulnFoundTemplate = "URL:%s %s发现漏洞:%d\n"
		texts.noVulnTemplate = "URL:%s %s扫描完成,未发现漏洞\n"
		texts.errorTemplate = "执行错误: host:%s %s\n"
		texts.scanningDesc = "开始对目标系统进行多维度扫描"
		texts.execScanDesc = "正在执行AI基础设施扫描"
		texts.scanCompleteDesc = "完成扫描"
		texts.taskCompleteDesc = "AI基础设施扫描任务完成"
		texts.reportGenDesc = "我需要提供更有价值的洞察..."
		texts.reportGenToolDesc = "正在生成扫描报告"
		texts.scanResultTemplate = "扫描结果: %d 条"
		texts.downloadFileLog = "开始下载文件: %s"
		texts.execScanTool = "执行扫描"
		texts.scanTool = "扫描"
		texts.generateReportTool = "生成报告"
		texts.nmapTool = "nmap"
		texts.aiScannerTool = "ai_scanner"
		texts.reportGeneratorTool = "report_generator"
		texts.scanOperation = "扫描"
		texts.targetSystem = "目标系统"
		texts.generateReport = "生成结构化扫描报告"
	}
	return texts
}

// prepareTargets 处理目标和附件
func (t *AIInfraScanAgent) prepareTargets(request TaskRequest, reqScan ScanRequest, texts scanTexts) ([]string, error) {
	return t.prepareTargetsContext(context.Background(), request, reqScan, texts)
}

func (t *AIInfraScanAgent) prepareTargetsContext(ctx context.Context, request TaskRequest, reqScan ScanRequest, texts scanTexts) ([]string, error) {
	targets := strings.Split(strings.TrimSpace(request.Content), "\n")

	if len(request.Attachments) == 0 {
		return targets, nil
	}

	tempDir := "temp_uploads"
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		gologger.Errorf("%s: %v", texts.createTempDir, err)
		return nil, err
	}

	for _, file := range request.Attachments {
		gologger.Infof(texts.downloadFileLog, file)
		fileName := filepath.Join(tempDir, fmt.Sprintf("tmp-%d%s", time.Now().UnixMicro(), filepath.Ext(file)))
		// Verify the path is within tempDir to prevent path traversal
		absTempDir, _ := filepath.Abs(tempDir)
		absFileName, _ := filepath.Abs(fileName)
		if !strings.HasPrefix(absFileName, absTempDir+string(os.PathSeparator)) {
			gologger.WithError(fmt.Errorf("非法路径: %s", file)).Errorln(texts.downloadFile)
			return nil, fmt.Errorf("非法文件路径")
		}
		if err := utils.DownloadFileContext(ctx, t.Server, request.SessionId, file, fileName); err != nil {
			gologger.WithError(err).Errorln(texts.downloadFile)
			return nil, err
		}
		lines, err := os.ReadFile(fileName)
		if err != nil {
			gologger.WithError(err).Errorln(texts.readFile)
			return nil, err
		}
		targets = append(targets, strings.Split(string(lines), "\n")...)
	}

	return targets, nil
}

// scanPortsAndPrepareTargets 解析目标输入并扫描端口,准备最终目标列表。
// 输入先过 common/targets 统一解析(范围/CIDR/通配符展开、去重、逐行报错),
// 随后仅对裸 IPv4(含展开产物)按端口发现模式执行 nmap,其余目标直接进入 Web 扫描。
func (t *AIInfraScanAgent) scanPortsAndPrepareTargets(targetLines []string, step01 string, texts scanTexts, callbacks TaskCallbacks, portMode string) ([]string, error) {
	targets, _, err := t.scanPortsAndPrepareTargetsContext(context.Background(), targetLines, step01, texts, callbacks, portMode)
	return targets, err
}

func (t *AIInfraScanAgent) scanPortsAndPrepareTargetsContext(ctx context.Context, targetLines []string, step01 string, texts scanTexts, callbacks TaskCallbacks, portMode string) ([]string, []targetExecutionError, error) {
	res, err := targetparser.Parse(targetLines)
	if err != nil {
		return nil, nil, &targetParseError{err: err, issues: res.Issues}
	}
	mode, err := portscan.Normalize(portMode, portscan.ModeDefault)
	if err != nil {
		return nil, nil, err
	}
	config, err := portscan.ConfigFromEnv()
	if err != nil {
		return nil, nil, err
	}
	status, tool := uuid.NewString(), uuid.NewString()
	callbacks.StepStatusUpdateCallback(step01, status, AgentStatusRunning, texts.portDetection, "")
	callbacks.ToolUsedCallback(step01, status, texts.nmapTool, []Tool{CreateTool(tool, texts.nmapTool, SubTaskStatusDoing, texts.portScan, texts.nmapTool, fmt.Sprintf("-T4 -p %s", portscan.Ports(mode)), "")})
	var progressMu sync.Mutex
	lastProgress := time.Time{}
	targets, failures, err := portscan.Discover(ctx, res.Targets, mode, config, func(progress portscan.DiscoveryProgress) {
		progressMu.Lock()
		defer progressMu.Unlock()
		if progress.Completed == progress.Total || time.Since(lastProgress) >= time.Second || progress.Err != nil {
			message := fmt.Sprintf("Port discovery: %d/%d, target=%s", progress.Completed, progress.Total, diagnosticTarget(progress.Target))
			if progress.Err != nil {
				message += " failed: " + safeOperationError(progress.Err)
			}
			callbacks.ToolUseLogCallback(tool, texts.nmapTool, step01, message+"\n")
			lastProgress = time.Now()
		}
	})
	final := make([]string, 0, len(targets))
	for _, target := range targets {
		final = append(final, target.Value)
	}
	targetErrors := make([]targetExecutionError, 0, len(failures))
	for _, failure := range failures {
		targetErrors = append(targetErrors, targetExecutionError{Target: reportTarget(failure.Target), Stage: "discovery", Error: safeOperationError(failure.Err)})
	}
	state := AgentStatusCompleted
	brief := "Port discovery complete"
	if err != nil || len(failures) > 0 {
		state = AgentStatusFailed
		brief = "Port discovery incomplete / 端口发现未完成"
	}
	callbacks.StepStatusUpdateCallback(step01, status, state, brief, fmt.Sprintf("targets=%d, failed=%d", len(final), len(failures)))
	callbacks.ToolUsedCallback(step01, status, texts.nmapTool, []Tool{CreateTool(tool, texts.nmapTool, SubTaskStatusDone, brief, texts.nmapTool, "", fmt.Sprintf("targets=%d, failed=%d", len(final), len(failures)))})
	return final, targetErrors, err
}

// dedupStrings 稳定去重,保留首次出现顺序
func dedupStrings(items []string) []string {
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, s := range items {
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// executeScan 执行扫描任务的统一入口
func (t *AIInfraScanAgent) executeScan(ctx context.Context, request TaskRequest, reqScan ScanRequest, texts scanTexts, callbacks TaskCallbacks, model *models.OpenAI) error {
	// 创建任务计划
	taskTitles := []string{texts.initEnv, texts.execScan, texts.genReport}
	var tasks []SubTask
	for _, title := range taskTitles {
		tasks = append(tasks, CreateSubTask(SubTaskStatusTodo, title, 0, uuid.NewString()))
	}
	callbacks.PlanUpdateCallback(tasks)

	// 步骤1: 初始化环境
	step01 := tasks[0].StepId
	callbacks.NewPlanStepCallback(step01, texts.initEnv)
	statusId01 := uuid.NewString()
	callbacks.StepStatusUpdateCallback(step01, statusId01, AgentStatusRunning, "Thinking", "")

	// 配置选项
	opts := &options.Options{
		Context:      ctx,
		TimeOut:      reqScan.Timeout,
		RateLimit:    200,
		FPTemplates:  t.Server,
		AdvTemplates: t.Server,
		WebServer:    false,
		Target:       reqScan.Target,
		LoadRemote:   true,
		Language:     request.Language,
	}

	headers := make([]string, 0)
	for k, v := range reqScan.Headers {
		headers = append(headers, k+":"+v)
	}
	opts.Headers = headers

	cfg, err := loadPostprocessConfig()
	if err != nil {
		return err
	}
	if raw := os.Getenv("AIG_HTTP_TIMEOUT"); raw != "" {
		timeout, err := time.ParseDuration(raw)
		if err != nil || timeout <= 0 {
			return fmt.Errorf("AIG_HTTP_TIMEOUT must be a positive duration")
		}
		opts.TimeOut = int(math.Ceil(timeout.Seconds()))
	}
	// 端口发现错误进入覆盖报告；单目标失败不终止其他目标。
	targets, targetErrors, err := t.scanPortsAndPrepareTargetsContext(ctx, reqScan.Target, step01, texts, callbacks, reqScan.PortMode)
	discoveryFailed := len(targetErrors)
	if err != nil {
		if ctx.Err() != nil {
			for _, target := range targets {
				targetErrors = append(targetErrors, targetExecutionError{Target: reportTarget(target), Stage: "scan_unfinished", Error: ctx.Err().Error()})
			}
			coverage, status := summarizeCoverage(len(targets), nil, targetErrors, discoveryFailed, true)
			callbacks.ResultCallback(finalScanReport(0, 0, nil, targetErrors, coverage, status))
		}
		return err
	}
	opts.Target = targets

	// AI模式下的初始化反馈
	if model != nil {
		config := ""
		var configMu sync.Mutex

		// 临时回调收集配置信息
		tempCallback := func(data interface{}) {
			if v, ok := data.(runner.Step01); ok {
				configMu.Lock()
				config += v.Text + "\n"
				configMu.Unlock()
			}
		}
		opts.SetCallback(tempCallback)

		// 创建runner获取配置
		r, err := runner.New(opts)
		if err != nil {
			return fmt.Errorf("new runner failed: %v", err)
		}
		r.Close()

		// AI分析初始化配置
		prompt := fmt.Sprintf(`你在执行AI基础设施扫描任务，你正在完成todo:%s
然后将以下文本转换为进度任务中的todo,加入你自己的思考，而不是简单罗列:
target count:%s
%s
## 返回格式(只返回json格式)
`+"```json\n"+`
[
{"title":"思考","desc":"在开始扫描前，需要确保所有必要的工具和数据库都已就绪。这就像医生手术前检查器械一样重要。"},
{"title":"执行1","desc":"✓ 目标锁定成功 - 识别到1个待扫描目标 ✓ 指纹库加载完成 - 已装载36种识别模式 ✓ 漏洞数据库就绪 - 涵盖394个已知漏洞特征"},
{"title":"Agent反思","desc":"环境配置符合预期，所有组件状态良好。我现在已经具备了执行任务所需的全部能力。"},
]
`+"\n```\n", texts.initEnv, fmt.Sprintf(texts.targetCountTemplate, len(targets)), config)

		if request.Language == "en" {
			prompt += "## Return in English"
		}

		aiCtx, aiCancel := context.WithTimeout(ctx, cfg.aiTimeout)
		response, err := model.ChatResponse(aiCtx, prompt)
		aiCancel()
		gologger.Infof("AI分析初始化配置结果: %s", response)
		if err == nil {
			type Item struct {
				Title string `json:"title"`
				Desc  string `json:"desc"`
			}
			var items []Item
			data := models.GetJsonString(response)
			err = json.Unmarshal([]byte(data), &items)
			if err != nil {
				callbacks.StepStatusUpdateCallback(step01, statusId01, AgentStatusCompleted, texts.initConfig, "")
				callbacks.StepStatusUpdateCallback(step01, uuid.NewString(), AgentStatusCompleted, texts.targetConfig, fmt.Sprintf(texts.targetCountTemplate, len(targets)))
			}
			if len(items) > 0 {
				callbacks.StepStatusUpdateCallback(step01, statusId01, AgentStatusCompleted, items[0].Title, items[0].Desc)
				if len(items) >= 2 {
					for _, item := range items[1 : len(items)-1] {
						s1 := uuid.NewString()
						//callbacks.StepStatusUpdateCallback(step01, s1, AgentStatusRunning, "思考中", "AI思考中")
						//time.Sleep(time.Millisecond * 600)
						callbacks.StepStatusUpdateCallback(step01, s1, AgentStatusCompleted, item.Title, item.Desc)
					}
				}
			}
		}
	} else {
		callbacks.StepStatusUpdateCallback(step01, statusId01, AgentStatusCompleted, texts.initConfig, "")
		callbacks.StepStatusUpdateCallback(step01, uuid.NewString(), AgentStatusCompleted, texts.targetConfig, fmt.Sprintf(texts.targetCountTemplate, len(targets)))
	}

	// 更新任务计划
	tasks[0].Status = SubTaskStatusDone
	tasks[1].Status = SubTaskStatusDoing
	tasks[1].StartedAt = time.Now().Unix()
	callbacks.PlanUpdateCallback(tasks)

	// 步骤2: 执行扫描
	step02 := tasks[1].StepId
	callbacks.NewPlanStepCallback(step02, texts.execScan)
	statusId02 := uuid.NewString()
	callbacks.StepStatusUpdateCallback(step02, statusId02, AgentStatusCompleted, texts.aigWorking, texts.scanningDesc)

	toolId02 := uuid.NewString()
	callbacks.ToolUsedCallback(step02, statusId02, texts.execScanTool,
		[]Tool{CreateTool(toolId02, texts.aiScannerTool, ToolStatusDoing, texts.execScanDesc, texts.scanOperation, texts.targetSystem, "")})

	// 回调只收集基础检测，不等待截图；所有失败与结果均参与最终覆盖报告。
	scanResults := make([]runner.CallbackScanResult, 0)
	observedTargets := make(map[string]bool)
	var mu sync.Mutex
	processFunc := func(data interface{}) {
		switch value := data.(type) {
		case runner.CallbackScanResult:
			mu.Lock()
			scanResults = append(scanResults, value)
			observedTargets[value.TargetURL] = true
			mu.Unlock()
			callbacks.ToolUseLogCallback(toolId02, texts.aiScannerTool, step02, fmt.Sprintf("target=%s scan complete, vulnerabilities=%d\n", diagnosticTarget(value.TargetURL), len(value.Vulnerabilities)))
		case runner.CallbackErrorInfo:
			message := safeOperationError(value.Error)
			mu.Lock()
			targetErrors = append(targetErrors, targetExecutionError{Target: reportTarget(value.Target), Stage: "scan", Error: message})
			observedTargets[value.Target] = true
			mu.Unlock()
			callbacks.ToolUseLogCallback(toolId02, texts.aiScannerTool, step02, fmt.Sprintf("target=%s scan failed: %s\n", diagnosticTarget(value.Target), message))
		}
	}

	opts.SetCallback(processFunc)
	r, err := runner.New(opts)
	if err != nil {
		return fmt.Errorf("new runner failed: %v", err)
	}
	defer r.Close()

	// 核心扫描取消后仍生成含已完成结果与覆盖缺口的报告。
	scanErr := r.RunEnumerationContext(ctx)
	if ctx.Err() != nil {
		for _, target := range targets {
			if !observedTargets[target] && !observedTargets["http://"+target] && !observedTargets["https://"+target] {
				targetErrors = append(targetErrors, targetExecutionError{Target: reportTarget(target), Stage: "scan_unfinished", Error: ctx.Err().Error()})
			}
		}
	}
	ops := postprocessOperations{
		screenshot: runner.ScreenShotContext,
		upload: func(ctx context.Context, data []byte) (string, error) {
			file, err := os.CreateTemp("", "aig-screenshot-*.jpg")
			if err != nil {
				return "", err
			}
			filename := file.Name()
			defer os.Remove(filename)
			_, writeErr := file.Write(data)
			closeErr := file.Close()
			if writeErr != nil {
				return "", writeErr
			}
			if closeErr != nil {
				return "", closeErr
			}
			info, err := utils.UploadFileContext(ctx, t.Server, filename)
			if err != nil {
				return "", err
			}
			if info.Data.FileUrl == "" {
				return "", fmt.Errorf("upload returned empty image reference")
			}
			return "/api/v1/images/" + info.Data.FileUrl, nil
		},
		progress: func(target, stage, status string) {
			message := fmt.Sprintf("target=%s stage=%s status=%s", diagnosticTarget(target), stage, status)
			gologger.Infof("postprocess %s", message)
			callbacks.ToolUseLogCallback(toolId02, texts.aiScannerTool, step02, message+"\n")
		},
	}
	if model != nil {
		ops.analyze = func(ctx context.Context, result *runner.CallbackScanResult, data []byte) error {
			localModel := *model
			info, reason, err := runner.AnalysisImageContext(ctx, result.TargetURL, result.Resp, request.Language, &localModel, data)
			if err != nil {
				return err
			}
			result.Reason = reason
			if info != nil && (info.Severity == "high" || info.Severity == "medium") {
				result.Vulnerabilities = append(result.Vulnerabilities, *info)
			}
			serialized, _ := json.Marshal(result.Vulnerabilities)
			prompt := "根据以下漏洞信息简要总结发现、业务风险与修复建议。没有漏洞信息时说明暂未发现漏洞：\n" + string(serialized)
			if request.Language == "en" {
				prompt += "\nReturn in English"
			}
			summary, err := localModel.ChatResponse(ctx, prompt)
			if err != nil {
				return err
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if strings.TrimSpace(summary) == "" {
				return fmt.Errorf("AI summary returned empty response")
			}
			result.Summary = summary
			return nil
		}
	}
	scanResults = postprocessResults(ctx, scanResults, cfg, ops)
	for _, result := range scanResults {
		if result.Postprocess != nil && result.Postprocess.Error != "" {
			targetErrors = append(targetErrors, targetExecutionError{Target: reportTarget(result.TargetURL), Stage: "postprocess", Error: safeOperationError(fmt.Errorf("%s", result.Postprocess.Error))})
		}
	}
	coverage, completionStatus := summarizeCoverage(len(targets), scanResults, targetErrors, discoveryFailed, ctx.Err() != nil)
	coverageMessage := fmt.Sprintf("Coverage / 覆盖: status=%s targets=%d successful=%d failed=%d unfinished=%d discovery_failed=%d postprocess_failed=%d. Results retained / 已保留检测结果。", completionStatus, coverage.TargetsTotal, coverage.ScanSucceeded, coverage.ScanFailed, coverage.ScanUnfinished, coverage.DiscoveryFailed, coverage.PostprocessFailed)
	callbacks.ToolUseLogCallback(toolId02, texts.aiScannerTool, step02, coverageMessage+"\n")
	if !coverage.Complete {
		callbacks.StepStatusUpdateCallback(step02, uuid.NewString(), AgentStatusFailed, "Coverage incomplete / 覆盖未完成", coverageMessage)
	}

	// 计算安全评分
	advies := make([]vulstruct.Info, 0)
	for _, item := range scanResults {
		advies = append(advies, item.Vulnerabilities...)
	}
	score := r.CalcSecScore(advies)

	finalStatus, finalBrief, finalDescription := AgentStatusCompleted, texts.aigCompleted, texts.scanCompleteDesc
	finalToolBrief := texts.scanComplete
	finalToolResult := fmt.Sprintf(texts.scanResultTemplate, len(scanResults))
	if !coverage.Complete {
		finalStatus = AgentStatusFailed
		finalBrief = "Coverage incomplete / 覆盖未完成"
		finalDescription = coverageMessage + " Score covers successful targets only / 评分仅限成功检测的目标，不能代表全部目标安全。"
		finalToolBrief = finalBrief
		finalToolResult = finalDescription
		tasks[1].Title += " (Coverage incomplete / 覆盖未完成)"
	}
	callbacks.StepStatusUpdateCallback(step02, statusId02, finalStatus, finalBrief, finalDescription)
	callbacks.ToolUsedCallback(step02, statusId02, texts.execScanTool, []Tool{CreateTool(toolId02, texts.aiScannerTool, ToolStatusDone, finalToolBrief, texts.scanOperation, texts.targetSystem, finalToolResult)})
	completionDescription := texts.taskCompleteDesc
	if !coverage.Complete {
		completionDescription = finalDescription
	}
	callbacks.StepStatusUpdateCallback(step02, uuid.NewString(), finalStatus, finalBrief, completionDescription)

	// 更新任务计划
	tasks[1].Status = SubTaskStatusDone
	tasks[2].Status = SubTaskStatusDoing
	tasks[2].StartedAt = time.Now().Unix()
	callbacks.PlanUpdateCallback(tasks)

	// 步骤3: 生成报告
	step03 := tasks[2].StepId
	callbacks.NewPlanStepCallback(step03, texts.genReport)
	statustool := uuid.NewString()
	callbacks.StepStatusUpdateCallback(step03, statustool, AgentStatusCompleted, texts.aigWorking, texts.reportGenDesc)

	toolId03 := uuid.NewString()
	callbacks.ToolUsedCallback(step03, statustool, texts.generateReportTool,
		[]Tool{CreateTool(toolId03, texts.reportGeneratorTool, ToolStatusDone, texts.reportGenToolDesc, texts.generateReport, "", fmt.Sprintf("%d", len(scanResults)))})

	// 发送最终结果
	result := finalScanReport(len(advies), score.SecScore, scanResults, targetErrors, coverage, completionStatus)
	tasks[2].Status = SubTaskStatusDone
	callbacks.PlanUpdateCallback(tasks)
	callbacks.ResultCallback(result)
	if scanErr != nil {
		return scanErr
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}
