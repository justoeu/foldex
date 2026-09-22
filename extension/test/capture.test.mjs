import { describe, test } from "node:test";
import assert from "node:assert/strict";

import { axisSteps, captureFullPage, capturePlan, captureVisible } from "../capture.js";

describe("axisSteps", () => {
  test("exact multiples never duplicate the final offset", () => {
    assert.deepEqual(axisSteps(2000, 1000), [0, 1000]);
  });

  test("non-multiples align the last step to the edge instead of overshooting", () => {
    assert.deepEqual(axisSteps(2500, 1000), [0, 1000, 1500]);
  });

  test("pages smaller than the viewport collapse to a single step", () => {
    assert.deepEqual(axisSteps(900, 1000), [0]);
  });
});

describe("capturePlan", () => {
  test("grid covers both axes, scales the canvas by dpr, rounds fractional dpr", () => {
    const plan = capturePlan({
      scrollWidth: 2500,
      scrollHeight: 1800,
      viewportWidth: 1000,
      viewportHeight: 900,
      devicePixelRatio: 2,
    });

    assert.deepEqual(plan, {
      steps: [
        { x: 0, y: 0 },
        { x: 1000, y: 0 },
        { x: 1500, y: 0 },
        { x: 0, y: 900 },
        { x: 1000, y: 900 },
        { x: 1500, y: 900 },
      ],
      cols: 3,
      rows: 2,
      canvasWidth: 5000,
      canvasHeight: 3600,
      dpr: 2,
    });
  });

  test("single-viewport page is one step and a fractional dpr rounds the canvas", () => {
    const plan = capturePlan({
      scrollWidth: 1280,
      scrollHeight: 720,
      viewportWidth: 1280,
      viewportHeight: 720,
      devicePixelRatio: 1.25,
    });

    assert.deepEqual(plan.steps, [{ x: 0, y: 0 }]);
    assert.equal(plan.canvasWidth, 1600);
    assert.equal(plan.canvasHeight, 900);
  });
});

describe("chrome-driven capture glue", () => {
  function captureChrome(log) {
    return {
      runtime: {},
      scripting: {
        executeScript({ func, args }, callback) {
          if (func.name === "PAGE_METRICS") {
            callback([
              {
                result: {
                  scrollWidth: 2000,
                  scrollHeight: 1000,
                  viewportWidth: 1000,
                  viewportHeight: 500,
                  devicePixelRatio: 2,
                },
              },
            ]);
          } else {
            log.push({ scroll: args });
            callback([{ result: null }]);
          }
        },
      },
      tabs: {
        captureVisibleTab(windowId, options, callback) {
          log.push({ captured: options });
          callback("data:image/png;base64,AAA");
        },
      },
    };
  }

  test("captureFullPage scrolls every plan step, captures per step, and returns the plan", async () => {
    const log = [];
    const { plan, images } = await captureFullPage(7, captureChrome(log));

    assert.deepEqual(plan.steps, [
      { x: 0, y: 0 },
      { x: 1000, y: 0 },
      { x: 0, y: 500 },
      { x: 1000, y: 500 },
    ]);
    assert.equal(images.length, 4);
    assert.deepEqual(
      log.map((entry) => entry.scroll ?? entry.captured),
      [
        [0, 0],
        { format: "png" },
        [1000, 0],
        { format: "png" },
        [0, 500],
        { format: "png" },
        [1000, 500],
        { format: "png" },
      ],
    );
  });

  test("captureVisible asks for a png dataUrl", async () => {
    const log = [];
    const dataUrl = await captureVisible(captureChrome(log));

    assert.equal(dataUrl, "data:image/png;base64,AAA");
    assert.deepEqual(log, [{ captured: { format: "png" } }]);
  });
});
