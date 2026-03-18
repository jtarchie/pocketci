// Import htmx
import "htmx.org";

// Import idiomorph extension for intelligent DOM morphing
import "idiomorph/dist/idiomorph-ext.esm.js";

// Import highlight.js core and languages
import hljs from "highlight.js/lib/core";
import yaml from "highlight.js/lib/languages/yaml";
import typescript from "highlight.js/lib/languages/typescript";
import javascript from "highlight.js/lib/languages/javascript";

// Register languages
hljs.registerLanguage("yaml", yaml);
hljs.registerLanguage("typescript", typescript);
hljs.registerLanguage("javascript", javascript);

// Import graph module
import { initGraph } from "./graph.js";

// Import results module (keyboard navigation + expand/collapse only)
import { initResults } from "./results.js";

// ---- Toast notifications (server-driven via HX-Trigger headers) ----

function showToast(message, type) {
  type = type || "info";
  const container = document.getElementById("toast-container");
  if (!container) return;

  const bgColor = type === "success"
    ? "bg-green-600"
    : type === "error"
    ? "bg-red-600"
    : "bg-blue-600";

  const toast = document.createElement("div");
  toast.className =
    `${bgColor} text-white px-4 py-3 rounded-lg shadow-lg flex items-center gap-2 transform transition-all duration-300 translate-x-full`;
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
  const message = statusCode === 404
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
});
