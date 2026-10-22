package agent

// 后处理使用固定数量 worker；操作自身接受 context，取消后等待真实操作退出。
import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Tencent/AI-Infra-Guard/common/runner"
)

// 日志使用可关联的目标摘要，报告目标去除用户信息、查询参数与片段。
func diagnosticTarget(target string) string {
	sum := sha256.Sum256([]byte(target))
	return fmt.Sprintf("%x", sum[:6])
}
func reportTarget(target string) string {
	parsed, err := url.Parse(target)
	if err != nil {
		return diagnosticTarget(target)
	}
	if parsed.Host == "" {
		return strings.Split(strings.Split(target, "?")[0], "#")[0]
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

var operationURLPattern = regexp.MustCompile(`https?://[^\s]+`)

func safeOperationError(err error) string {
	if err == nil {
		return ""
	}
	return operationURLPattern.ReplaceAllStringFunc(err.Error(), reportTarget)
}

type postprocessConfig struct {
	workers                                     int
	screenshotTimeout, uploadTimeout, aiTimeout time.Duration
}
type postprocessOperations struct {
	screenshot func(context.Context, string) ([]byte, error)
	upload     func(context.Context, []byte) (string, error)
	analyze    func(context.Context, *runner.CallbackScanResult, []byte) error
	progress   func(string, string, string)
}
type targetExecutionError struct {
	Target string `json:"target"`
	Stage  string `json:"stage"`
	Error  string `json:"error"`
}
type scanCoverage struct {
	TargetsTotal      int  `json:"targets_total"`
	ScanSucceeded     int  `json:"scan_succeeded"`
	ScanFailed        int  `json:"scan_failed"`
	ScanUnfinished    int  `json:"scan_unfinished"`
	DiscoveryFailed   int  `json:"discovery_failed"`
	PostprocessFailed int  `json:"postprocess_failed"`
	Complete          bool `json:"complete"`
}

func loadPostprocessConfig() (postprocessConfig, error) {
	cfg := postprocessConfig{workers: 5, screenshotTimeout: 120 * time.Second, uploadTimeout: 120 * time.Second, aiTimeout: 300 * time.Second}
	for _, v := range []struct {
		name string
		dst  *time.Duration
	}{{"AIG_SCREENSHOT_TIMEOUT", &cfg.screenshotTimeout}, {"AIG_UPLOAD_TIMEOUT", &cfg.uploadTimeout}, {"AIG_AI_TIMEOUT", &cfg.aiTimeout}} {
		if value := os.Getenv(v.name); value != "" {
			d, err := time.ParseDuration(value)
			if err != nil || d <= 0 {
				return cfg, fmt.Errorf("%s must be a positive duration", v.name)
			}
			*v.dst = d
		}
	}
	return cfg, nil
}

func summarizeCoverage(total int, results []runner.CallbackScanResult, errors []targetExecutionError, discoveryFailed int, cancelled bool) (scanCoverage, string) {
	c := scanCoverage{TargetsTotal: total, ScanSucceeded: len(results), DiscoveryFailed: discoveryFailed}
	for _, err := range errors {
		if err.Stage == "scan" || err.Stage == "" {
			c.ScanFailed++
		}
	}
	c.ScanUnfinished = total - c.ScanSucceeded - c.ScanFailed
	if c.ScanUnfinished < 0 {
		c.ScanUnfinished = 0
	}
	for _, r := range results {
		if s := r.Postprocess; s != nil && (s.Screenshot != "complete" || s.Upload != "complete" || (s.AI != "complete" && s.AI != "skipped")) {
			c.PostprocessFailed++
		}
	}
	c.Complete = !cancelled && c.ScanFailed == 0 && c.ScanUnfinished == 0 && c.DiscoveryFailed == 0 && c.PostprocessFailed == 0
	if cancelled {
		return c, "cancelled"
	}
	if !c.Complete {
		return c, "incomplete"
	}
	return c, "complete"
}

func finalScanReport(total, score int, results []runner.CallbackScanResult, targetErrors []targetExecutionError, coverage scanCoverage, status string) map[string]interface{} {
	if len(results) == 0 && !coverage.Complete {
		score = 0
	}
	if results == nil {
		results = []runner.CallbackScanResult{}
	}
	return map[string]interface{}{"total": total, "score": score, "results": results, "completion_status": status, "coverage": coverage, "target_errors": targetErrors, "score_valid": coverage.Complete && len(results) > 0, "score_scope": "completed_targets_only"}
}

func postprocessResults(ctx context.Context, results []runner.CallbackScanResult, cfg postprocessConfig, ops postprocessOperations) []runner.CallbackScanResult {
	out := append([]runner.CallbackScanResult(nil), results...)
	jobs := make(chan int)
	var wg sync.WaitGroup
	workers := cfg.workers
	if workers < 1 {
		workers = 1
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				postprocessOne(ctx, &out[index], cfg, ops)
			}
		}()
	}
	for i := range out {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return out
}

func postprocessOne(ctx context.Context, result *runner.CallbackScanResult, cfg postprocessConfig, ops postprocessOperations) {
	state := &runner.PostprocessStatus{Screenshot: "pending", Upload: "skipped", AI: "skipped"}
	result.Postprocess = state
	failed := func(stage string, err error) string {
		status := "failed"
		if ctx.Err() != nil {
			status = "cancelled"
		}
		state.Error = strings.TrimSpace(state.Error + " " + stage + ": " + safeOperationError(err))
		return status
	}
	if err := ctx.Err(); err != nil {
		state.Screenshot = failed("screenshot", err)
		return
	}
	call := func(stage string, timeout time.Duration, run func(context.Context) error) error {
		if ops.progress != nil {
			ops.progress(result.TargetURL, stage, "started")
		}
		operationCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		err := run(operationCtx)
		status := "complete"
		if err != nil {
			status = "failed"
		}
		if ops.progress != nil {
			ops.progress(result.TargetURL, stage, status)
		}
		return err
	}
	var data []byte
	err := call("screenshot", cfg.screenshotTimeout, func(ctx context.Context) error {
		var e error
		data, e = ops.screenshot(ctx, result.TargetURL)
		return e
	})
	if err != nil {
		state.Screenshot = failed("screenshot", err)
		return
	}
	state.Screenshot = "complete"
	if ops.upload != nil {
		err = call("upload", cfg.uploadTimeout, func(ctx context.Context) error { var e error; result.ScreenShot, e = ops.upload(ctx, data); return e })
		if err != nil {
			state.Upload = failed("upload", err)
		} else {
			state.Upload = "complete"
		}
	}
	if ops.analyze != nil {
		err = call("ai", cfg.aiTimeout, func(ctx context.Context) error { return ops.analyze(ctx, result, data) })
		if err != nil {
			state.AI = failed("ai", err)
		} else {
			state.AI = "complete"
		}
	}
}
