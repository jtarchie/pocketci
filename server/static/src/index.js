// Import htmx
import "htmx.org";

// Import idiomorph extension for intelligent DOM morphing
import "idiomorph/dist/idiomorph-ext.esm.js";

// Import highlight.js core and languages
import hljs from "highlight.js/lib/core";
import yaml from "highlight.js/lib/languages/yaml";
import typescript from "highlight.js/lib/languages/typescript";
import javascript from "highlight.js/lib/languages/javascript";
import json from "highlight.js/lib/languages/json";

// Register languages
hljs.registerLanguage("yaml", yaml);
hljs.registerLanguage("typescript", typescript);
hljs.registerLanguage("javascript", javascript);
hljs.registerLanguage("json", json);

// Import graph module
import { initGraph } from "./graph.js";

// Import results module (keyboard navigation + expand/collapse only)
import { initResults } from "./results.js";

// ---- Toast notifications (server-driven via HX-Trigger headers) ----

function showToast(message, type) {
  type = type || "info";
  const container = document.getElementById("toast-container");
  if (!container) return;

  const bgColor =
    type === "success"
      ? "bg-green-600"
      : type === "error"
        ? "bg-red-600"
        : "bg-blue-600";

  const toast = document.createElement("div");
  toast.className = `${bgColor} text-white px-4 py-3 rounded-lg shadow-lg flex items-center gap-2 transform transition-all duration-300 translate-x-full`;
  toast.setAttribute("role", "alert");
  toast.setAttribute("aria-live", "assertive");
  toast.setAttribute("aria-atomic", "true");

  const messageSpan = document.createElement("span");
  messageSpan.textContent = message;
  toast.appendChild(messageSpan);

  const closeBtn = document.createElement("button");
  closeBtn.className = "ml-2 hover:opacity-75";
  closeBtn.setAttribute("aria-label", "Close notification");
  closeBtn.innerHTML =
    '<svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24" aria-hidden="true"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"/></svg>';
  closeBtn.addEventListener("click", () => toast.remove());
  toast.appendChild(closeBtn);

  container.appendChild(toast);
  requestAnimationFrame(() => toast.classList.remove("translate-x-full"));
  setTimeout(() => {
    toast.classList.add("translate-x-full");
    setTimeout(() => toast.remove(), 300);
  }, 5000);
}

// Listen for server-driven showToast events via HX-Trigger response header
document.body.addEventListener("showToast", function (event) {
  const detail = event.detail || {};
  showToast(detail.message || "Done", detail.type || "info");
});

// Export for global use
window.PocketCI = { showToast };

// Initialize syntax highlighting
function initSyntaxHighlighting() {
  document.querySelectorAll("pre code").forEach((block) => {
    hljs.highlightElement(block);
  });
}

// Add global HTMx error handling
document.body.addEventListener("htmx:responseError", function (event) {
  const statusCode = event.detail.xhr?.status;
  const message =
    statusCode === 404
      ? "Resource not found"
      : statusCode === 500
        ? "Server error occurred"
        : statusCode === 0
          ? "Network error - please check your connection"
          : "An error occurred";
  showToast(message, "error");
});

// Add loading state management
let htmxInFlight = 0;
const pollBar = document.getElementById("poll-bar");

document.body.addEventListener("htmx:beforeRequest", function (event) {
  if (event.detail.target) {
    event.detail.target.setAttribute("aria-busy", "true");
  }
  htmxInFlight++;
  if (pollBar) pollBar.style.opacity = "1";
});

document.body.addEventListener("htmx:afterSettle", function (event) {
  if (event.detail.target) {
    event.detail.target.removeAttribute("aria-busy");
  }
  htmxInFlight = Math.max(0, htmxInFlight - 1);
  if (htmxInFlight === 0 && pollBar) pollBar.style.opacity = "0";
});

// Preserve <details open> state across idiomorph morphs.
// Idiomorph syncs attributes to match the server HTML, which never includes
// the `open` attribute. We save which <details> elements the user has opened
// before the morph and restore them afterward.
const openDetailsIds = new Set();
const highlightedLineIds = new Set();
document.body.addEventListener("htmx:beforeSwap", function () {
  openDetailsIds.clear();
  document.querySelectorAll("details.task-item[open]").forEach(function (el) {
    if (el.id) openDetailsIds.add(el.id);
  });
  highlightedLineIds.clear();
  document.querySelectorAll(".term-line.highlighted").forEach(function (el) {
    if (el.id) highlightedLineIds.add(el.id);
  });
});
document.body.addEventListener("htmx:afterSwap", function () {
  openDetailsIds.forEach(function (id) {
    const el = document.getElementById(id);
    if (el) el.setAttribute("open", "");
  });
  highlightedLineIds.forEach(function (id) {
    const el = document.getElementById(id);
    if (el) el.classList.add("highlighted");
  });
});

// ---- Trigger dialogs (args / webhook) ----

function triggerPipeline(pipelineId, pipelineName, body) {
  fetch("/api/pipelines/" + pipelineId + "/trigger", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "HX-Request": "true",
    },
    body: JSON.stringify(body),
  })
    .then(function (resp) {
      if (!resp.ok) {
        return resp.text().then(function (t) {
          throw new Error(t || "Server error " + resp.status);
        });
      }
      showToast(pipelineName + " triggered successfully", "success");
    })
    .catch(function (err) {
      showToast(err.message, "error");
    });
}

function initTriggerDialogs() {
  // Args dialog
  const argsSubmit = document.getElementById("trigger-args-submit");
  if (argsSubmit) {
    argsSubmit.addEventListener("click", function () {
      const textarea = document.getElementById("trigger-args-input");
      const args = textarea.value
        .split("\n")
        .map(function (l) {
          return l.trim();
        })
        .filter(function (l) {
          return l.length > 0;
        });

      triggerPipeline(
        argsSubmit.dataset.pipelineId,
        argsSubmit.dataset.pipelineName,
        {
          mode: "args",
          args: args,
        },
      );

      textarea.value = "";
      document.getElementById("trigger-args-dialog").close();
    });
  }

  // Webhook dialog - add header button
  const addHeaderBtn = document.getElementById("trigger-webhook-add-header");
  if (addHeaderBtn) {
    addHeaderBtn.addEventListener("click", function () {
      const container = document.getElementById("trigger-webhook-headers");
      const row = document.createElement("div");
      row.className = "flex gap-2 items-center";
      row.innerHTML =
        '<input type="text" placeholder="Header name" class="flex-1 rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-700 px-2 py-1 text-sm dark:text-white webhook-header-key">' +
        '<input type="text" placeholder="Value" class="flex-1 rounded border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-700 px-2 py-1 text-sm dark:text-white webhook-header-val">' +
        '<button type="button" class="text-red-500 hover:text-red-700 text-sm" aria-label="Remove header">&times;</button>';
      row.querySelector("button").addEventListener("click", function () {
        row.remove();
      });
      container.appendChild(row);
    });
  }

  // Webhook dialog - submit
  const webhookSubmit = document.getElementById("trigger-webhook-submit");
  if (webhookSubmit) {
    webhookSubmit.addEventListener("click", function () {
      const method = document.getElementById("trigger-webhook-method").value;
      const body = document.getElementById("trigger-webhook-body").value;

      const headers = {};
      document
        .querySelectorAll("#trigger-webhook-headers > div")
        .forEach(function (row) {
          const key = row.querySelector(".webhook-header-key").value.trim();
          const val = row.querySelector(".webhook-header-val").value.trim();
          if (key) headers[key] = val;
        });

      triggerPipeline(
        webhookSubmit.dataset.pipelineId,
        webhookSubmit.dataset.pipelineName,
        {
          mode: "webhook",
          webhook: {
            method: method,
            body: body,
            headers: Object.keys(headers).length > 0 ? headers : undefined,
          },
        },
      );

      document.getElementById("trigger-webhook-body").value = "";
      document.getElementById("trigger-webhook-headers").innerHTML = "";
      document.getElementById("trigger-webhook-dialog").close();
    });
  }

  // Webhook body - live JSON syntax highlighting preview
  const webhookBody = document.getElementById("trigger-webhook-body");
  const webhookPreview = document.getElementById("trigger-webhook-preview");
  if (webhookBody && webhookPreview) {
    const codeEl = webhookPreview.querySelector("code");
    webhookBody.addEventListener("input", function () {
      const val = webhookBody.value.trim();
      if (val) {
        codeEl.textContent = val;
        codeEl.removeAttribute("data-highlighted");
        hljs.highlightElement(codeEl);
        webhookPreview.classList.remove("hidden");
      } else {
        webhookPreview.classList.add("hidden");
      }
    });
  }

  // Close dialogs on backdrop click
  document.querySelectorAll("dialog.trigger-dialog").forEach(function (dialog) {
    dialog.addEventListener("click", function (e) {
      if (e.target === dialog) dialog.close();
    });
  });
}

// Initialize when DOM is ready
document.addEventListener("DOMContentLoaded", function () {
  // Initialize graph if we're on the graph page
  const graphDataElement = document.getElementById("graph-data");
  if (graphDataElement) {
    try {
      const graphData = JSON.parse(graphDataElement.textContent);
      const currentPath = graphDataElement.dataset.path || "/";
      initGraph(graphData, currentPath);
    } catch (e) {
      console.error("Failed to initialize graph:", e);
    }
  }

  // Initialize results page if we're on the results page
  const tasksContainer = document.getElementById("tasks-container");
  if (tasksContainer) {
    try {
      initResults();
    } catch (e) {
      console.error("Failed to initialize results:", e);
    }
  }

  // Initialize syntax highlighting
  initSyntaxHighlighting();

  // Initialize trigger dialogs if on pipeline detail page
  initTriggerDialogs();
});
