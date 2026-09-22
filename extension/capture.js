// Pure stitch math — no chrome dependency, fully covered by node --test.

// Viewport-sized steps over one axis. The last step aligns with the edge so
// pages that are not an exact multiple never overshoot (and sticky headers
// repeat at most once per axis edge).
export function axisSteps(total, viewport) {
  const last = Math.max(0, total - viewport);
  const steps = [];
  for (let offset = 0; offset < last; offset += viewport) {
    steps.push(offset);
  }
  steps.push(last);
  return steps;
}

export function capturePlan({
  scrollWidth,
  scrollHeight,
  viewportWidth,
  viewportHeight,
  devicePixelRatio,
}) {
  const xs = axisSteps(scrollWidth, viewportWidth);
  const ys = axisSteps(scrollHeight, viewportHeight);
  const steps = [];
  for (const y of ys) {
    for (const x of xs) {
      steps.push({ x, y });
    }
  }
  return {
    steps,
    cols: xs.length,
    rows: ys.length,
    canvasWidth: Math.round(scrollWidth * devicePixelRatio),
    canvasHeight: Math.round(scrollHeight * devicePixelRatio),
    dpr: devicePixelRatio,
  };
}

// Chrome-dependent capture, thin and injected for tests.

function callChrome(chromeApi, target, method, ...args) {
  return new Promise((resolve, reject) => {
    target[method](...args, (result) => {
      const lastError = chromeApi.runtime && chromeApi.runtime.lastError;
      if (lastError) {
        reject(new Error(lastError.message));
        return;
      }
      resolve(result);
    });
  });
}

const PAGE_METRICS = () => ({
  scrollWidth: document.documentElement.scrollWidth,
  scrollHeight: document.documentElement.scrollHeight,
  viewportWidth: window.innerWidth,
  viewportHeight: window.innerHeight,
  devicePixelRatio: window.devicePixelRatio,
});

// Double rAF inside the page: scrollTo only schedules, the compositor needs
// two frames before captureVisibleTab sees the new position.
const SCROLL_AND_SETTLE = (x, y) =>
  new Promise((resolve) => {
    window.scrollTo(x, y);
    requestAnimationFrame(() => requestAnimationFrame(resolve));
  });

export function getPageMetrics(tabId, chromeApi = chrome) {
  return callChrome(chromeApi, chromeApi.scripting, "executeScript", {
    target: { tabId },
    func: PAGE_METRICS,
  }).then((results) => results[0].result);
}

export function scrollToOffset(tabId, { x, y }, chromeApi = chrome) {
  return callChrome(chromeApi, chromeApi.scripting, "executeScript", {
    target: { tabId },
    func: SCROLL_AND_SETTLE,
    args: [x, y],
  });
}

export function captureVisible(chromeApi = chrome) {
  return callChrome(chromeApi, chromeApi.tabs, "captureVisibleTab", null, {
    format: "png",
  });
}

export async function captureFullPage(tabId, chromeApi = chrome) {
  const metrics = await getPageMetrics(tabId, chromeApi);
  const plan = capturePlan(metrics);
  const images = [];
  for (const step of plan.steps) {
    await scrollToOffset(tabId, step, chromeApi);
    images.push(await captureVisible(chromeApi));
  }
  return { plan, images };
}

// Offscreen-canvas compose, runs in the popup (the only extension surface
// with DOM). Math (placement) is derived from the plan; this stays thin.
export async function composeStitch({ plan, images }, { ImageImpl = Image } = {}) {
  const canvas = document.createElement("canvas");
  canvas.width = plan.canvasWidth;
  canvas.height = plan.canvasHeight;
  const ctx = canvas.getContext("2d");

  const loaded = await Promise.all(
    images.map(
      (dataUrl) =>
        new Promise((resolve, reject) => {
          const img = new ImageImpl();
          img.onload = () => resolve(img);
          img.onerror = () => reject(new Error("capture tile failed to decode"));
          img.src = dataUrl;
        }),
    ),
  );

  plan.steps.forEach((step, i) => {
    ctx.drawImage(loaded[i], Math.round(step.x * plan.dpr), Math.round(step.y * plan.dpr));
  });

  return canvas;
}
