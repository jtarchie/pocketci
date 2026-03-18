// k6 load test script for CI server API
// Run with: k6 run benchmarks/api_load.js
// Or with options: k6 run --vus 10 --duration 30s benchmarks/api_load.js

import http from "k6/http";
import { check, group, sleep } from "k6";
import { Counter, Rate, Trend } from "k6/metrics";

// Custom metrics
const pipelineCreateDuration = new Trend("pipeline_create_duration", true);
const pipelineTriggerDuration = new Trend("pipeline_trigger_duration", true);
const pipelineListDuration = new Trend("pipeline_list_duration", true);
const runStatusDuration = new Trend("run_status_duration", true);
const errorRate = new Rate("error_rate");
const pipelinesCreated = new Counter("pipelines_created");

// Configuration
const BASE_URL = __ENV.BASE_URL || "http://localhost:8080";

// Test options
export const options = {
  scenarios: {
    // Smoke test - basic functionality
    smoke: {
      executor: "constant-vus",
      vus: 1,
      duration: "10s",
      startTime: "0s",
      tags: { scenario: "smoke" },
    },
    // Load test - sustained load
    load: {
      executor: "ramping-vus",
      startVUs: 0,
      stages: [
        { duration: "10s", target: 5 },
        { duration: "30s", target: 10 },
        { duration: "10s", target: 0 },
      ],
      startTime: "15s",
      tags: { scenario: "load" },
    },
    // Stress test - find breaking point
    stress: {
      executor: "ramping-vus",
      startVUs: 0,
      stages: [
        { duration: "10s", target: 20 },
        { duration: "20s", target: 50 },
        { duration: "10s", target: 0 },
      ],
      startTime: "70s",
      tags: { scenario: "stress" },
    },
  },
  thresholds: {
    http_req_duration: ["p(95)<2000"], // 95% of requests under 2s
    error_rate: ["rate<0.1"], // Less than 10% error rate
    pipeline_create_duration: ["p(95)<1000"],
    pipeline_trigger_duration: ["p(95)<500"],
  },
};

// Minimal pipeline content for testing
const MINIMAL_PIPELINE = `
const pipeline = async () => {
  await runtime.run({
    name: "bench-task",
    image: "busybox",
    command: { path: "true" },
  });
};
export { pipeline };
`;

// Generate unique name for each pipeline
function uniqueName() {
  return `bench-${Date.now()}-${Math.random().toString(36).substring(7)}`;
}

// Main test function
export default function () {
  let pipelineId = null;
  let runId = null;

  group("Pipeline CRUD", function () {
    // Create pipeline
    group("Create Pipeline", function () {
      const payload = JSON.stringify({
        name: uniqueName(),
        content: MINIMAL_PIPELINE,
        driver: "docker",
      });

      const createRes = http.post(`${BASE_URL}/api/pipelines`, payload, {
        headers: { "Content-Type": "application/json" },
      });

      pipelineCreateDuration.add(createRes.timings.duration);

      const createOk = check(createRes, {
        "create status is 201": (r) => r.status === 201,
        "create returns pipeline id": (r) => {
          const body = r.json();
          pipelineId = body.id;
          return body.id !== undefined;
        },
      });

      if (!createOk) {
        errorRate.add(1);
      } else {
        errorRate.add(0);
        pipelinesCreated.add(1);
      }
    });

    // List pipelines
    group("List Pipelines", function () {
      const listRes = http.get(`${BASE_URL}/api/pipelines`);
      pipelineListDuration.add(listRes.timings.duration);

      const listOk = check(listRes, {
        "list status is 200": (r) => r.status === 200,
        "list returns array": (r) => Array.isArray(r.json()),
      });

      if (!listOk) {
        errorRate.add(1);
      } else {
        errorRate.add(0);
      }
    });

    // Get specific pipeline
    if (pipelineId) {
      group("Get Pipeline", function () {
        const getRes = http.get(`${BASE_URL}/api/pipelines/${pipelineId}`);

        const getOk = check(getRes, {
          "get status is 200": (r) => r.status === 200,
          "get returns correct id": (r) => r.json().id === pipelineId,
        });

        if (!getOk) {
          errorRate.add(1);
        } else {
          errorRate.add(0);
        }
      });
    }
  });

  group("Pipeline Execution", function () {
    if (!pipelineId) {
      return;
    }

    // Trigger pipeline
    group("Trigger Pipeline", function () {
      const triggerRes = http.post(
        `${BASE_URL}/api/pipelines/${pipelineId}/trigger`,
        null,
        {
          headers: { "Content-Type": "application/json" },
        },
      );

      pipelineTriggerDuration.add(triggerRes.timings.duration);

      const triggerOk = check(triggerRes, {
        "trigger status is 202": (r) => r.status === 202,
        "trigger returns run id": (r) => {
          const body = r.json();
          runId = body.run_id;
          return body.run_id !== undefined;
        },
      });

      if (!triggerOk) {
        errorRate.add(1);
      } else {
        errorRate.add(0);
      }
    });

    // Poll run status
    if (runId) {
      group("Poll Run Status", function () {
        const statusRes = http.get(
          `${BASE_URL}/api/pipelines/${pipelineId}/runs/${runId}`,
        );

        runStatusDuration.add(statusRes.timings.duration);

        const statusOk = check(statusRes, {
          "status request succeeds": (r) =>
            r.status === 200 || r.status === 404,
        });

        if (!statusOk) {
          errorRate.add(1);
        } else {
          errorRate.add(0);
        }
      });
    }

    // Cleanup - delete pipeline
    group("Delete Pipeline", function () {
      const deleteRes = http.del(`${BASE_URL}/api/pipelines/${pipelineId}`);

      const deleteOk = check(deleteRes, {
        "delete status is 204": (r) => r.status === 204,
      });

      if (!deleteOk) {
        errorRate.add(1);
      } else {
        errorRate.add(0);
      }
    });
  });

  // Small delay between iterations to prevent overwhelming
  sleep(0.1);
}

// Setup function - runs once before the test
export function setup() {
  // Verify server is healthy
  const healthRes = http.get(`${BASE_URL}/health`);
  check(healthRes, {
    "server is healthy": (r) => r.status === 200,
  });

  if (healthRes.status !== 200) {
    throw new Error(`Server health check failed: ${healthRes.status}`);
  }

  console.log(`Starting load test against ${BASE_URL}`);
  return { startTime: Date.now() };
}

// Teardown function - runs once after the test
export function teardown(data) {
  const duration = (Date.now() - data.startTime) / 1000;
  console.log(`Load test completed in ${duration.toFixed(2)}s`);
}
