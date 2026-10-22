// 功能：对展开的 IPv4 目标进行有限并发端口发现，保留逐目标失败与稳定输出。
// 实现：worker 调度 nmap；context 取消后回收所有 worker；不设置隐式主机超时。
// 输入输出：解析后的目标、端口模式与配置；返回目标、失败清单及全局取消错误。
// 依赖及用法：需要 nmap；Agent 与 CLI 共用 Discover，环境配置由 ConfigFromEnv 读取。
package portscan

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/Tencent/AI-Infra-Guard/common/targets"
	"github.com/Tencent/AI-Infra-Guard/common/utils"
)

type DiscoveryConfig struct {
	Workers int
	Timeout time.Duration
	scan    func(context.Context, string, string) (*utils.NmapRun, error)
}

type DiscoveryError struct {
	Target string
	Err    error
}
type DiscoveryProgress struct {
	Completed, Total int
	Target           string
	Err              error
}

// ConfigFromEnv 限制并发数，超时默认关闭；显式超时必须在报告中标记未完成。
func ConfigFromEnv() (DiscoveryConfig, error) {
	c := DiscoveryConfig{Workers: 4}
	if value := os.Getenv("AIG_NMAP_WORKERS"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 32 {
			return c, fmt.Errorf("AIG_NMAP_WORKERS 必须是 1-32")
		}
		c.Workers = n
	}
	if value := os.Getenv("AIG_NMAP_TIMEOUT"); value != "" {
		d, err := time.ParseDuration(value)
		if err != nil || d < 0 {
			return c, fmt.Errorf("AIG_NMAP_TIMEOUT 必须是非负时间，例如 30m，0 表示关闭")
		}
		c.Timeout = d
	}
	return c, nil
}

// Discover 回调从调用方串行执行，不占 worker；单目标失败不丢失其余已发现端口。
func Discover(ctx context.Context, items []targets.Target, mode string, config DiscoveryConfig, onProgress func(DiscoveryProgress)) ([]targets.Target, []DiscoveryError, error) {
	if _, err := Normalize(mode, ModeDefault); err != nil {
		return nil, nil, err
	}
	if config.Workers == 0 {
		config.Workers = 4
	}
	if config.Workers < 1 || config.Workers > 32 || config.Timeout < 0 {
		return nil, nil, fmt.Errorf("无效的端口发现配置")
	}
	scan := config.scan
	if scan == nil {
		scan = utils.NmapScanContext
	}
	// 输入去重也能避免重复进程；输出按输入顺序归并，不依赖网络完成顺序。
	unique := make([]targets.Target, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		if !seen[item.Value] {
			seen[item.Value] = true
			unique = append(unique, item)
		}
	}
	type outcome struct {
		index int
		run   *utils.NmapRun
		err   error
	}
	jobs := make(chan int)
	results := make(chan outcome, config.Workers)
	var wg sync.WaitGroup
	total := 0
	for _, item := range unique {
		if item.Kind == targets.KindIPv4 {
			total++
		}
	}
	for n := 0; n < config.Workers; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				if ctx.Err() != nil {
					return
				}
				operation := ctx
				cancel := func() {}
				if config.Timeout > 0 {
					operation, cancel = context.WithTimeout(ctx, config.Timeout)
				}
				run, err := scan(operation, unique[index].Value, Ports(mode))
				if operation.Err() != nil {
					err = operation.Err()
				}
				cancel()
				results <- outcome{index: index, run: run, err: err}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for index, item := range unique {
			if item.Kind != targets.KindIPv4 {
				continue
			}
			select {
			case <-ctx.Done():
				return
			case jobs <- index:
			}
		}
	}()
	go func() { wg.Wait(); close(results) }()
	byIndex := make(map[int]outcome, total)
	completed := 0
	for result := range results {
		byIndex[result.index] = result
		completed++
		if onProgress != nil {
			onProgress(DiscoveryProgress{Completed: completed, Total: total, Target: unique[result.index].Value, Err: result.err})
		}
	}
	out := make([]targets.Target, 0, len(unique))
	failures := make([]DiscoveryError, 0)
	seen = map[string]bool{}
	appendUnique := func(item targets.Target) {
		if !seen[item.Value] {
			out = append(out, item)
			seen[item.Value] = true
		}
	}
	for index, item := range unique {
		appendUnique(item)
		if item.Kind != targets.KindIPv4 {
			continue
		}
		result, ok := byIndex[index]
		if !ok {
			if ctx.Err() != nil {
				failures = append(failures, DiscoveryError{Target: item.Value, Err: ctx.Err()})
			}
			continue
		}
		if result.err != nil {
			failures = append(failures, DiscoveryError{Target: item.Value, Err: result.err})
			continue
		}
		if result.run == nil {
			failures = append(failures, DiscoveryError{Target: item.Value, Err: fmt.Errorf("nmap 返回空结果")})
			continue
		}
		for _, host := range result.run.Hosts {
			for _, port := range host.Ports.PortList {
				if port.State.State == "open" {
					appendUnique(targets.Target{Kind: targets.KindIPv4Port, Raw: item.Raw, Value: fmt.Sprintf("%s:%d", host.Address.Addr, port.PortID)})
				}
			}
		}
	}
	return out, failures, ctx.Err()
}
