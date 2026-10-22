#!/usr/bin/env node
/**
 * 功能：验证实际 Web 页面中的端口选项、保存状态和任务请求参数。
 * 实现：通过真实浏览器点击配置和发送按钮，在网络层截获扫描创建请求，不执行扫描。
 * 输入：--url 本机受控 Web、--evidence 新证据目录；可选 --browser Chromium 路径。
 * 输出：两种模式截图及 browser-results.json；失败以非零状态退出。
 * 依赖：Node.js、Playwright 与本机 Chromium；不联网安装，外站请求一律拦截。
 */
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {parseArgs} = require('node:util');
const {chromium} = require(process.env.AIG_PLAYWRIGHT_MODULE || 'playwright');

async function main() {
  const {values} = parseArgs({options: {url:{type:'string'}, evidence:{type:'string'}, browser:{type:'string'}}});
  assert(values.url && values.evidence, '需要 --url 和 --evidence');
  const origin = new URL(values.url).origin;
  assert(['127.0.0.1','localhost','[::1]'].includes(new URL(origin).hostname), '仅对本机受控验收地址运行');
  const root = path.resolve(values.evidence);
  fs.mkdirSync(root, {recursive:false});
  const browser = await chromium.launch({headless:true, ...(values.browser ? {executablePath:values.browser} : {})});
  const report = {origin, external_requests_blocked:true, scan_requests_intercepted:true, modes:[]};
  try {
    for (const mode of ['full_tcp','fixed_ai']) {
      const context = await browser.newContext({locale:'zh-CN', viewport:{width:1440,height:1000}});
      const page = await context.newPage();
      await page.addInitScript(() => {
        localStorage.setItem('i18nextLng','zh');
        for (const key of ['hasSeenTour_v2','hasSeenAiInfraScanTour','hasSeenMcpScanTour','hasSeenSkillScanTour','hasSeenModelJailbreakTour','hasSeenAgentScanTour']) localStorage.setItem(key,'true');
      });
      let captured;
      await page.route('**/*', async route => {
        const request = route.request();
        if (new URL(request.url()).origin !== origin) return route.abort();
        if (request.method() === 'POST' && new URL(request.url()).pathname === '/api/v1/app/tasks') {
          captured = request.postDataJSON();
          return route.fulfill({status:200, contentType:'application/json', body:JSON.stringify({status:0,data:{}})});
        }
        return route.continue();
      });
      await page.goto(origin, {waitUntil:'networkidle',timeout:60000});
      const close = page.getByRole('button',{name:'关闭',exact:true});
      if (await close.isVisible()) await close.click();
      await page.locator('[data-joyride="ai-infra-scan"]').click();
      const config = page.locator('[data-joyride="ai-infra-scan-config-button"]');
      await config.click();
      const full = page.locator('input[name="scan-port-mode"][value="full_tcp"]');
      const fixed = page.locator('input[name="scan-port-mode"][value="fixed_ai"]');
      assert(await full.isChecked(), '新建任务默认应为全量 TCP');
      assert(await fixed.isVisible());
      assert((await page.getByRole('dialog').innerText()).includes('1–65535'));
      await page.locator(`input[name="scan-port-mode"][value="${mode}"]`).check();
      await page.screenshot({path:path.join(root,mode+'.png')});
      await page.getByRole('button',{name:'确认并保存',exact:true}).click();
      await config.click();
      assert(await page.locator(`input[name="scan-port-mode"][value="${mode}"]`).isChecked(), '保存后重新打开应保持模式');
      await page.getByRole('button',{name:'取消',exact:true}).click();
      await page.locator('[data-joyride="ai-infra-scan-textarea"] textarea').fill('192.0.2.5');
      const requestPromise = page.waitForRequest(r => r.method()==='POST' && new URL(r.url()).pathname==='/api/v1/app/tasks');
      await page.locator('[data-joyride="ai-infra-scan-submit-button"]').click();
      const request = await requestPromise;
      const payload = request.postDataJSON();
      assert.equal(payload.taskType,'AI-Infra-Scan');
      assert.equal(payload.params.port_mode,mode);
      assert.equal(captured.params.port_mode,mode);
      report.modes.push({mode,default_full_tcp:true,saved_selection:true,request_parameter:payload.params.port_mode,target:payload.content,scan_executed:false});
      await context.close();
    }
    report.passed = true;
    fs.writeFileSync(path.join(root,'browser-results.json'), JSON.stringify(report,null,2)+'\n');
    console.log(JSON.stringify(report,null,2));
  } finally {
    await browser.close();
  }
}
main().catch(error => {console.error(error); process.exitCode=1;});
