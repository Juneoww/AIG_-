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

package chromium

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/Tencent/AI-Infra-Guard/internal/gologger"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/cdp"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/gorilla/websocket"
	"golang.org/x/sys/execabs"
)

const (
	MaxWidth       = 1280
	MinHeight      = 1024
	DefaultTimeout = 30 * time.Second
	StableWait     = 2 * time.Second
	Quality        = 90
)

// WebScreenShot 网页截图器
type WebScreenShot struct {
	browser        *rod.Browser
	cmd            *exec.Cmd
	exited         chan struct{}
	cancel         context.CancelFunc
	connection     *websocket.Conn
	profile        string
	closeOnce      sync.Once
	cleanupTimeout time.Duration
}

// NewWebScreenShotWithOptions 保留旧入口，整个浏览器生命周期使用默认截止。
func NewWebScreenShotWithOptions() (*WebScreenShot, error) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultTimeout)
	shot, err := NewWebScreenShotWithContext(ctx)
	if err != nil {
		cancel()
		return nil, err
	}
	previous := shot.cancel
	shot.cancel = func() { previous(); cancel() }
	return shot, nil
}

// NewWebScreenShotWithContext 启动、连接及后续 CDP 操作共享调用方取消信号。
func NewWebScreenShotWithContext(ctx context.Context) (*WebScreenShot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cleanupTimeout := 5 * time.Second
	if raw := os.Getenv("AIG_BROWSER_CLEANUP_TIMEOUT"); raw != "" {
		value, err := time.ParseDuration(raw)
		if err != nil || value <= 0 {
			return nil, errors.New("AIG_BROWSER_CLEANUP_TIMEOUT must be a positive duration")
		}
		cleanupTimeout = value
	}
	chromePath := FindExecPath()
	if chromePath == "" {
		return nil, errors.New("未找到Chrome/Chromium浏览器")
	}
	profile, err := os.MkdirTemp("", "aig-chromium-")
	if err != nil {
		return nil, err
	}
	lifetime, cancel := context.WithCancel(ctx)
	shot := &WebScreenShot{cancel: cancel, profile: profile, cleanupTimeout: cleanupTimeout, exited: make(chan struct{})}
	failed := true
	defer func() {
		if failed {
			shot.Close()
		}
	}()
	settings := launcher.New().Leakless(false).UserDataDir(profile).
		Set("disable-gpu", "true").Set("ignore-certificate-errors", "true").
		Set("disable-crash-reporter", "true").Set("disable-notifications", "true").
		Set("hide-scrollbars", "true").Set("window-size", fmt.Sprintf("%d,%d", MaxWidth, MinHeight)).
		Set("mute-audio", "true").Delete("use-mock-keychain").NoSandbox(false).Headless(true)
	parser := launcher.NewURLParser().Context(lifetime)
	cmd := exec.CommandContext(lifetime, chromePath, settings.FormatArgs()...)
	shot.cmd = cmd
	configureBrowserProcess(cmd)
	cmd.Cancel = func() error { return killBrowserProcess(cmd) }
	cmd.WaitDelay = cleanupTimeout
	cmd.Stdout = parser
	cmd.Stderr = parser
	if err = cmd.Start(); err != nil {
		close(shot.exited)
		return nil, fmt.Errorf("启动Chrome失败: %w", err)
	}
	go func() { _ = cmd.Wait(); close(shot.exited) }()
	var endpoint string
	select {
	case <-lifetime.Done():
		return nil, lifetime.Err()
	case <-shot.exited:
		return nil, parser.Err()
	case endpoint = <-parser.URL:
	}
	request, err := http.NewRequestWithContext(lifetime, http.MethodGet, endpoint+"/json/version", nil)
	if err != nil {
		return nil, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	var version struct {
		URL string `json:"webSocketDebuggerUrl"`
	}
	err = json.NewDecoder(response.Body).Decode(&version)
	response.Body.Close()
	if err != nil {
		return nil, err
	}
	conn, _, err := websocket.DefaultDialer.DialContext(lifetime, version.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("连接Chrome失败: %w", err)
	}
	shot.connection = conn
	// 取消时关闭真实 socket，使 CDP 的写入和读取也能及时返回。
	context.AfterFunc(lifetime, func() { _ = conn.Close() })
	client := cdp.New().Start(&browserSocket{connection: conn, ctx: lifetime, writeTimeout: cleanupTimeout})
	shot.browser = rod.New().Context(lifetime).Client(client)
	if err = shot.browser.Connect(); err != nil {
		return nil, fmt.Errorf("连接Chrome失败: %w", err)
	}
	failed = false
	return shot, nil
}

type browserSocket struct {
	connection   *websocket.Conn
	ctx          context.Context
	writeTimeout time.Duration
	mu           sync.Mutex
}

func (s *browserSocket) Send(data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ctx.Err(); err != nil {
		return err
	}
	timeout := s.writeTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	deadline := time.Now().Add(timeout)
	if operationDeadline, ok := s.ctx.Deadline(); ok && operationDeadline.Before(deadline) {
		deadline = operationDeadline
	}
	_ = s.connection.SetWriteDeadline(deadline)
	return s.connection.WriteMessage(websocket.TextMessage, data)
}
func (s *browserSocket) Read() ([]byte, error) {
	_, data, err := s.connection.ReadMessage()
	return data, err
}

// Close 独立清理截止后终止子进程，并等待进程及管道 reader 真实退出。
func (w *WebScreenShot) Close() {
	w.closeOnce.Do(func() {
		cleanupTimeout := w.cleanupTimeout
		if cleanupTimeout <= 0 {
			cleanupTimeout = 5 * time.Second
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		if w.browser != nil {
			if err := w.browser.Context(cleanupCtx).Close(); err != nil {
				gologger.Debugf("关闭浏览器失败: %v", err)
			}
		}
		if w.cancel != nil {
			w.cancel()
		}
		if w.connection != nil {
			_ = w.connection.Close()
		}
		if w.cmd != nil && w.cmd.Process != nil {
			_ = killBrowserProcess(w.cmd)
			<-w.exited
		}
		if w.profile != "" {
			_ = os.RemoveAll(w.profile)
		}
	})
}

func (w *WebScreenShot) Screen(domain string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultTimeout)
	defer cancel()
	return w.ScreenWithContext(ctx, domain)
}

func (w *WebScreenShot) ScreenWithContext(ctx context.Context, domain string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	page, err := w.browser.Context(ctx).Page(proto.TargetCreateTarget{})
	if err != nil {
		return nil, fmt.Errorf("创建页面失败: %w", err)
	}
	defer func() {
		cleanupTimeout := w.cleanupTimeout
		if cleanupTimeout <= 0 {
			cleanupTimeout = 5 * time.Second
		}
		cleanup, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		_ = page.Context(cleanup).Close()
	}()
	_, err = page.EvalOnNewDocument(`window.alert=()=>{};window.confirm=()=>true;window.prompt=()=>'';window.onbeforeunload=null;`)
	if err != nil {
		return nil, fmt.Errorf("页面初始化失败: %w", err)
	}
	if err = page.Navigate(domain); err != nil {
		return nil, fmt.Errorf("导航失败: %w", err)
	}
	if err = page.WaitStable(StableWait); err != nil {
		return nil, fmt.Errorf("等待页面稳定失败: %w", err)
	}
	quality := Quality
	data, err := page.Screenshot(false, &proto.PageCaptureScreenshot{Format: proto.PageCaptureScreenshotFormatJpeg, Quality: &quality})
	if err != nil {
		return nil, fmt.Errorf("截图失败: %w", err)
	}
	return data, nil
}

// FindExecPath 查找Chrome可执行文件路径
func FindExecPath() string {
	var locations []string
	switch runtime.GOOS {
	case "darwin":
		locations = []string{
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		}
	case "windows":
		locations = []string{
			"chrome",
			"chrome.exe",
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			filepath.Join(os.Getenv("USERPROFILE"), `AppData\Local\Google\Chrome\Application\chrome.exe`),
			filepath.Join(os.Getenv("USERPROFILE"), `AppData\Local\Chromium\Application\chrome.exe`),
		}
	default:
		locations = []string{
			"/usr/lib/chromium-browser",
			"chromium-browser",
			"chromium",
			"/snap/bin/chromium",
			"/snap/chromium/current/usr/lib/chromium-browser/chrome",
			"/opt/google/chrome",
			"google-chrome",
			"google-chrome-stable",
			"google-chrome-beta",
			"google-chrome-unstable",
			"/usr/bin/google-chrome",
			"/usr/local/bin/chrome",
			"chrome",
		}
	}

	for _, path := range locations {
		if found, err := execabs.LookPath(path); err == nil {
			return found
		}
	}
	return ""
}
