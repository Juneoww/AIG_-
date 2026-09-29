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

package cmd

import (
	"github.com/Tencent/AI-Infra-Guard/common/portscan"
	"github.com/Tencent/AI-Infra-Guard/common/runner"
	"github.com/Tencent/AI-Infra-Guard/internal/gologger"
	"github.com/Tencent/AI-Infra-Guard/internal/options"
	"github.com/spf13/cobra"
)

// 为scanCmd定义标志变量
var (
	scanTargets         []string
	scanTargetFile      string
	scanOutputFile      string
	scanProxyURL        string
	scanTimeOut         int
	scanRateLimit       int
	scanFpTemplates     string
	scanAdvTemplates    string
	scanListVulTemplate bool
	scanCheckVulTargets bool
	scanLocalScan       bool
	scanAIAnalysis      bool
	scanAIHunyuanToken  string
	scanAIDeepSeekToken string
	scanHeaders         []string
	scanLanguage        string
	scanPortMode        string
)

// scanCmd 表示scan子命令
var scanCmd = &cobra.Command{
	Use:   "scan",
	Short: "执行原始扫描功能",
	Long:  `执行原始的扫描功能，扫描指定目标的安全漏洞。`,
	Run: func(cmd *cobra.Command, args []string) {
		mode, err := normalizeCLIPortMode(scanPortMode)
		if err != nil {
			gologger.Fatalf("端口发现模式错误: %s\n", err)
		}
		// 创建Options对象
		scanOptions := &options.Options{
			Target:          scanTargets,
			TargetFile:      scanTargetFile,
			Output:          scanOutputFile,
			ProxyURL:        scanProxyURL,
			TimeOut:         scanTimeOut,
			RateLimit:       scanRateLimit,
			FPTemplates:     scanFpTemplates,
			AdvTemplates:    scanAdvTemplates,
			ListVulTemplate: scanListVulTemplate,
			CheckVulTargets: scanCheckVulTargets,
			LocalScan:       scanLocalScan,
			Headers:         scanHeaders,
			Language:        scanLanguage,
			PortMode:        mode,
		}

		// 设置日志级别
		gologger.StdLogger.Logrus().SetLevel(gologger.InfoLevel)

		r, err := runner.New(scanOptions)
		if err != nil {
			gologger.Fatalf("Could not create runner: %s\n", err)
		}
		defer r.Close()

		if scanOptions.CheckVulTargets {
			return
		}
		if scanOptions.ListVulTemplate {
			r.ShowFpAndVulList(scanOptions.ListVulTemplate)
			return
		}

		r.RunEnumeration()
	},
}

// normalizeCLIPortMode 将用户入口的空模式统一为全量 TCP；Runner 内部空值仍保留跳过语义。
func normalizeCLIPortMode(mode string) (string, error) {
	return portscan.Normalize(mode, portscan.ModeDefault)
}

func init() {
	rootCmd.AddCommand(scanCmd)

	// 设置scan子命令的标志
	scanCmd.Flags().StringArrayVarP(&scanTargets, "target", "t", []string{}, "目标URL，可以指定多个目标，例如: --target xx.com --target aa.com")
	scanCmd.Flags().StringVarP(&scanTargetFile, "file", "f", "", "包含目标URL的文件路径")
	scanCmd.Flags().StringVarP(&scanOutputFile, "output", "o", "", "输出文件路径")
	scanCmd.Flags().IntVar(&scanTimeOut, "timeout", 5, "请求超时时间(秒)")
	scanCmd.Flags().StringVar(&scanProxyURL, "proxy-url", "", "代理服务器URL")
	scanCmd.Flags().StringArrayVar(&scanHeaders, "header", []string{}, "HTTP请求头，可以指定多个，例如: --header \"key:value\" --header \"key:value\"")
	scanCmd.Flags().IntVar(&scanRateLimit, "limit", 200, "每秒最大请求数")
	scanCmd.Flags().StringVar(&scanFpTemplates, "fps", "data/fingerprints", "指纹模板文件或目录")
	scanCmd.Flags().StringVar(&scanAdvTemplates, "vul", "data/vuln", "漏洞数据库目录")
	scanCmd.Flags().BoolVar(&scanListVulTemplate, "list-vul", false, "列出漏洞模板")
	scanCmd.Flags().BoolVar(&scanCheckVulTargets, "check-vul", false, "验证漏洞模板")
	scanCmd.Flags().BoolVar(&scanLocalScan, "localscan", false, "一键本地扫描")
	scanCmd.Flags().BoolVar(&scanAIAnalysis, "ai", false, "启用AI分析")
	scanCmd.Flags().StringVar(&scanAIHunyuanToken, "hunyuan-token", "", "混元API令牌")
	scanCmd.Flags().StringVar(&scanAIDeepSeekToken, "deepseek-token", "", "DeepSeek API令牌")
	scanCmd.Flags().StringVar(&scanLanguage, "lang", "zh", "响应语言 zh/en")
	scanCmd.Flags().StringVar(&scanPortMode, "port-mode", portscan.ModeDefault, "端口发现模式(仅作用于裸 IPv4 目标):full_tcp(1-65535,默认) / fixed_ai(11434,1337,7000-9000,18789)")
}
