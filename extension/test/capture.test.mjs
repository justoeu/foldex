import { describe, test } from "node:test";
import assert from "node:assert/strict";

import { axisSteps, capturePlan } from "../capture.js";

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
