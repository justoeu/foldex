
const { chromium } = require('playwright');
const fs = require('fs');
(async () => {
  const spec = JSON.parse(fs.readFileSync(process.env.SPEC, 'utf8'));
  const browser = await chromium.launch();
  const context = await browser.newContext({ viewport: { width: 1280, height: 800 } });
  const page = await context.newPage();
  if (spec.block_external !== false) {
    const local = /^(localhost|127\.0\.0\.1|\[::1\])(:\d+)?$/;
    await context.route('**/*', (route) => {
      const h = new URL(route.request().url()).hostname;
      return local.test(h) ? route.continue() : route.abort();
    });
  }
  const consoleErrors = [], networkErrors = [], steps = [], shots = [];
  page.on('console', (m) => {
    if (m.type() === 'error') consoleErrors.push({ text: m.text(), step: steps.length,
      location: typeof m.location === 'function' ? m.location() : null,
      argument_count: typeof m.args === 'function' ? m.args().length : null });
  });
  page.on('pageerror', (e) => consoleErrors.push({ text: String(e), step: steps.length, pageerror: true }));
  page.on('response', (r) => { if (r.status() >= 400) networkErrors.push({ status: r.status(), url: r.url(), step: steps.length }); });
  const shot = async (name) => { if (shots.length >= 20) return null; const f = process.env.SHOT_DIR + '/' + spec.screen + '-' + String(steps.length + 1).padStart(2, '0') + '-' + (name || 'step') + '.png'; await page.screenshot({ path: f, fullPage: true }); shots.push(f); return f; };
  let ok = true;
  try {
    if (spec.auth) {
      await page.goto(spec.base_url + spec.auth.url);
      await page.fill(spec.auth.fields.user, process.env.MMH_UI_USER || '');
      await page.fill(spec.auth.fields.pass, process.env.MMH_UI_PASS || '');
      await page.click(spec.auth.submit);
      await page.waitForLoadState('networkidle').catch(() => {});
      steps.push({ n: 1, action: 'login', ok: true });
    }
  } catch (e) { ok = false; steps.push({ n: 1, action: 'login', ok: false, error: String(e) }); }
  for (const s of (spec.steps || [])) {
    const n = steps.length + 1;
    try {
      if (s.action === 'goto') await page.goto(spec.base_url + s.path);
      else if (s.action === 'click') await page.click(s.selector, { timeout: s.timeout || 5000 });
      else if (s.action === 'fill') await page.fill(s.selector, s.value == null ? '' : String(s.value), { timeout: s.timeout || 5000 });
      else if (s.action === 'assert_visible') { const el = await page.waitForSelector(s.selector, { timeout: s.timeout || 5000 }); if (s.text) { const t = await el.textContent(); if (!String(t || '').includes(s.text)) throw new Error('texto "' + s.text + '" não achado em ' + s.selector); } }
      else if (s.action === 'assert_text') { const t = await page.textContent(s.selector, { timeout: s.timeout || 5000 }); if (!String(t || '').includes(s.text)) throw new Error('esperado "' + s.text + '" em ' + s.selector + ', veio "' + String(t).trim().slice(0, 80) + '"'); }
      else if (s.action === 'wait') await page.waitForTimeout(s.ms || 500);
      else if (s.action === 'screenshot') { const f = await shot(s.name); steps.push({ n, action: 'screenshot', ok: true, file: f }); continue; }
      else throw new Error('ação desconhecida: ' + s.action);
      steps.push({ n, action: s.action, ok: true, ...(s.selector ? { selector: s.selector } : {}), ...(s.path ? { url: s.path } : {}) });
    } catch (e) {
      ok = false;
      const f = await shot('fail').catch(() => null);
      steps.push({ n, action: s.action, ok: false, error: String(e), ...(f ? { file: f } : {}) });
      break;
    }
  }
  if (!shots.length) await shot('final').catch(() => {});
  await browser.close();
  const failedSteps = steps.filter((s) => !s.ok);
  const expectedNetwork = networkErrors.filter(e => (spec.expected_network_errors || []).some(x => x.status === e.status && x.path === new URL(e.url).pathname));
  const unexpectedNetwork = networkErrors.filter(e => !expectedNetwork.includes(e));
  const unexpectedConsole = consoleErrors.filter(e => {
    // Chromium emits a console diagnostic in addition to the HTTP response.
    // Application console.error calls have JS arguments and never qualify.
    const status = e.text.match(/^Failed to load resource: the server responded with a status of (\d{3})(?: \([^)]*\))?$/);
    const expected = !e.pageerror && e.argument_count === 0 && status && e.location?.url
      && expectedNetwork.some(r => r.url === e.location.url && r.status === Number(status[1]));
    if (expected) e.expected_network_error = true;
    return !expected;
  });
  ok = ok && steps.length > 0 && unexpectedConsole.length === 0 && unexpectedNetwork.length === 0;
  const out = {
    execution_id: process.env.EXECUTION_ID, screen: spec.screen, spec_ref: spec.spec_ref || null, mode: 'drive', base_url: spec.base_url,
    steps, console_errors: consoleErrors, unexpected_console_errors:unexpectedConsole,
    network_errors: networkErrors, unexpected_network_errors:unexpectedNetwork, screenshots: shots,
    assertions: { ok: steps.length - failedSteps.length, total: steps.length, failed: failedSteps.map((s) => s.error || s.action) },
    verdict: ok ? 'APPROVE' : 'REJECT', at: new Date().toISOString(),
  };
  fs.writeFileSync(process.env.OUT_JSON, JSON.stringify(out, null, 2));
  process.exit(ok ? 0 : 1);
})().catch((e) => { console.error(e); process.exit(2); });
