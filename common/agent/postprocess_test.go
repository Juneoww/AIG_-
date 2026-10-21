package agent

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tencent/AI-Infra-Guard/common/runner"
)

func TestPostprocessPreserves155ResultsWhenOneScreenshotTimesOut(t *testing.T) {
	results := make([]runner.CallbackScanResult, 155)
	for i := range results {
		results[i].TargetURL = fmt.Sprint(i)
	}
	var active atomic.Int32
	ops := postprocessOperations{screenshot: func(ctx context.Context, target string) ([]byte, error) {
		active.Add(1)
		defer active.Add(-1)
		if target == "42" {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return []byte("jpg"), nil
	}, upload: func(context.Context, []byte) (string, error) { return "/image.jpg", nil }}
	cfg := postprocessConfig{workers: 5, screenshotTimeout: 20 * time.Millisecond, uploadTimeout: time.Second, aiTimeout: time.Second}
	out := postprocessResults(context.Background(), results, cfg, ops)
	if len(out) != 155 || out[41].ScreenShot != "/image.jpg" || out[43].ScreenShot != "/image.jpg" {
		t.Fatalf("base results lost: %d", len(out))
	}
	if out[42].Postprocess.Screenshot != "failed" || out[42].Postprocess.Error == "" {
		t.Fatalf("missing evidence gap: %+v", out[42])
	}
	if active.Load() != 0 {
		t.Fatal("operation still running")
	}
}

func TestPostprocessCancellationJoinsWorkersAndPreservesQueuedResults(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var active atomic.Int32
	ops := postprocessOperations{screenshot: func(ctx context.Context, _ string) ([]byte, error) {
		active.Add(1)
		defer active.Add(-1)
		cancel()
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	out := postprocessResults(ctx, make([]runner.CallbackScanResult, 155), postprocessConfig{workers: 5, screenshotTimeout: time.Second}, ops)
	if len(out) != 155 || active.Load() != 0 {
		t.Fatal("cancel lost results or left work running")
	}
	for _, r := range out {
		if r.Postprocess.Screenshot != "cancelled" {
			t.Fatalf("missing cancellation status: %+v", r.Postprocess)
		}
	}
}

func TestCoverageCannotClaimAllFailedOrPartialScanComplete(t *testing.T) {
	for _, tc := range []struct {
		success, failed, unfinished int
		cancelled                   bool
	}{{155, 21, 0, false}, {0, 176, 0, false}, {1, 0, 175, true}} {
		results := make([]runner.CallbackScanResult, tc.success)
		errs := make([]targetExecutionError, tc.failed)
		coverage, status := summarizeCoverage(176, results, errs, 0, tc.cancelled)
		if coverage.Complete || status == "complete" || coverage.ScanUnfinished != tc.unfinished {
			t.Fatalf("misleading coverage: %+v %s", coverage, status)
		}
	}
}

func TestPostprocessUploadAndAIErrorRetainBaseAndScreenshot(t *testing.T) {
	ops := postprocessOperations{screenshot: func(context.Context, string) ([]byte, error) { return []byte("jpg"), nil }, upload: func(context.Context, []byte) (string, error) { return "", errors.New("upload unavailable") }, analyze: func(context.Context, *runner.CallbackScanResult, []byte) error {
		return errors.New("model unavailable")
	}}
	out := postprocessResults(context.Background(), []runner.CallbackScanResult{{Title: "base detection"}}, postprocessConfig{workers: 1, screenshotTimeout: time.Second, uploadTimeout: time.Second, aiTimeout: time.Second}, ops)
	if out[0].Title != "base detection" || out[0].Postprocess.Upload != "failed" || out[0].Postprocess.AI != "failed" {
		t.Fatalf("lost status: %+v", out[0])
	}
}

func TestIncompleteReportsNeverPresentAnAllFailedCleanScore(t *testing.T) {
	coverage, status := summarizeCoverage(176, nil, make([]targetExecutionError, 176), 0, false)
	report := finalScanReport(0, 100, nil, nil, coverage, status)
	if report["score"] != 0 || report["score_valid"] != false || report["completion_status"] != "incomplete" {
		t.Fatalf("misleading clean report: %+v", report)
	}
	coverage, status = summarizeCoverage(176, make([]runner.CallbackScanResult, 155), make([]targetExecutionError, 21), 0, false)
	report = finalScanReport(3, 70, make([]runner.CallbackScanResult, 155), nil, coverage, status)
	if report["score"] != 70 || report["score_valid"] != false || report["score_scope"] != "completed_targets_only" {
		t.Fatalf("partial score not scoped: %+v", report)
	}
}
