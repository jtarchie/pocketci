/**
 * Results page functionality
 * - Expand/collapse all
 * - Keyboard navigation
 * - Help panel
 */

function initTerminalLineLinks() {
  let lastClickedLineId = null;

  function parseLineHash(hash) {
    if (!hash || hash.length < 2) return null;
    const fragment = hash.substring(1);
    // Match patterns like "termid-L5" or "termid-L5-L10"
    const rangeMatch = fragment.match(/^(.+)-L(\d+)-L(\d+)$/);
    if (rangeMatch) {
      return {
        terminalID: rangeMatch[1],
        startLine: parseInt(rangeMatch[2], 10),
        endLine: parseInt(rangeMatch[3], 10),
      };
    }
    const singleMatch = fragment.match(/^(.+)-L(\d+)$/);
    if (singleMatch) {
      return {
        terminalID: singleMatch[1],
        startLine: parseInt(singleMatch[2], 10),
        endLine: parseInt(singleMatch[2], 10),
      };
    }
    return null;
  }

  function clearHighlights() {
    document.querySelectorAll(".term-line.highlighted").forEach(function (el) {
      el.classList.remove("highlighted");
    });
  }

  function highlightRange(terminalID, startLine, endLine) {
    clearHighlights();
    const lo = Math.min(startLine, endLine);
    const hi = Math.max(startLine, endLine);
    let firstEl = null;
    for (let i = lo; i <= hi; i++) {
      const el = document.getElementById(terminalID + "-L" + i);
      if (el) {
        el.classList.add("highlighted");
        if (!firstEl) firstEl = el;
      }
    }
    return firstEl;
  }

  function applyHash() {
    const parsed = parseLineHash(window.location.hash);
    if (!parsed) return;
    // Open parent <details> if closed
    const firstLineEl = document.getElementById(
      parsed.terminalID + "-L" + parsed.startLine,
    );
    if (firstLineEl) {
      const details = firstLineEl.closest("details.task-item");
      if (details && !details.hasAttribute("open")) {
        details.setAttribute("open", "");
      }
    }
    const firstHighlighted = highlightRange(
      parsed.terminalID,
      parsed.startLine,
      parsed.endLine,
    );
    if (firstHighlighted) {
      firstHighlighted.scrollIntoView({ behavior: "smooth", block: "center" });
    }
  }

  globalThis.addEventListener("hashchange", applyHash);
  applyHash();

  // Delegated click handler for line number links
  document.addEventListener("click", function (e) {
    const link = e.target.closest(".term-line-num");
    if (!link) return;
    e.preventDefault();
    const lineDiv = link.closest(".term-line");
    if (!lineDiv) return;
    const lineId = lineDiv.id;

    if (e.shiftKey && lastClickedLineId) {
      // Range selection
      const lastParsed = parseLineHash("#" + lastClickedLineId);
      const currentParsed = parseLineHash("#" + lineId);
      if (
        lastParsed && currentParsed &&
        lastParsed.terminalID === currentParsed.terminalID
      ) {
        const lo = Math.min(lastParsed.startLine, currentParsed.startLine);
        const hi = Math.max(lastParsed.startLine, currentParsed.startLine);
        const rangeHash = "#" + lastParsed.terminalID + "-L" + lo + "-L" + hi;
        history.replaceState(null, "", rangeHash);
        highlightRange(lastParsed.terminalID, lo, hi);
        return;
      }
    }

    lastClickedLineId = lineId;
    history.replaceState(null, "", "#" + lineId);
    clearHighlights();
    lineDiv.classList.add("highlighted");
  });
}

export function initResults() {
  const searchInput = document.getElementById("task-search");
  const expandAllBtn = document.getElementById("expand-all");
  const collapseAllBtn = document.getElementById("collapse-all");
  const helpToggle = document.getElementById("help-toggle");
  const helpPanel = document.getElementById("help-panel");

  function getTasksContainer() {
    return document.getElementById("tasks-container");
  }

  if (!getTasksContainer()) return;

  initTerminalLineLinks();

  // Help panel toggle
  if (helpToggle && helpPanel) {
    helpToggle.addEventListener("click", function () {
      const isHidden = helpPanel.classList.contains("hidden");
      helpPanel.classList.toggle("hidden");
      helpToggle.setAttribute("aria-expanded", isHidden);
      if (isHidden) helpPanel.focus();
    });

    document.addEventListener("click", function (e) {
      if (!helpToggle.contains(e.target) && !helpPanel.contains(e.target)) {
        helpPanel.classList.add("hidden");
        helpToggle.setAttribute("aria-expanded", "false");
      }
    });

    document.addEventListener("keydown", function (e) {
      if (e.key === "Escape" && !helpPanel.classList.contains("hidden")) {
        helpPanel.classList.add("hidden");
        helpToggle.setAttribute("aria-expanded", "false");
        helpToggle.focus();
      }
    });
  }

  function getAllTasks() {
    const container = getTasksContainer();
    return container
      ? Array.from(container.querySelectorAll(".task-item"))
      : [];
  }

  // Expand all
  if (expandAllBtn) {
    expandAllBtn.addEventListener("click", function () {
      getAllTasks().forEach((task) => task.setAttribute("open", ""));
    });
  }

  // Collapse all
  if (collapseAllBtn) {
    collapseAllBtn.addEventListener("click", function () {
      getAllTasks().forEach((task) => task.removeAttribute("open"));
    });
  }

  // Keyboard navigation
  let currentTaskIndex = -1;
  document.addEventListener("keydown", function (e) {
    if (searchInput && e.target === searchInput) {
      if (e.key === "Escape") {
        searchInput.value = "";
        searchInput.dispatchEvent(new Event("search"));
        searchInput.blur();
      }
      return;
    }

    const tasks = getAllTasks();
    if (tasks.length === 0) return;

    // Don't intercept shortcuts when modifier keys are held (e.g. Cmd+C to copy).
    if (e.metaKey || e.ctrlKey || e.altKey) return;

    switch (e.key) {
      case "j":
      case "ArrowDown":
        if (e.target.tagName !== "INPUT") {
          e.preventDefault();
          currentTaskIndex = Math.min(currentTaskIndex + 1, tasks.length - 1);
          tasks[currentTaskIndex].scrollIntoView({
            behavior: "smooth",
            block: "center",
          });
          tasks[currentTaskIndex].focus();
        }
        break;
      case "k":
      case "ArrowUp":
        if (e.target.tagName !== "INPUT") {
          e.preventDefault();
          currentTaskIndex = Math.max(currentTaskIndex - 1, 0);
          tasks[currentTaskIndex].scrollIntoView({
            behavior: "smooth",
            block: "center",
          });
          tasks[currentTaskIndex].focus();
        }
        break;
      case "Enter":
      case " ":
        if (e.target.classList.contains("task-item")) {
          e.preventDefault();
          e.target.toggleAttribute("open");
        }
        break;
      case "/":
        if (searchInput) {
          e.preventDefault();
          searchInput.focus();
        }
        break;
      case "e":
        if (e.target.tagName !== "INPUT" && expandAllBtn) expandAllBtn.click();
        break;
      case "c":
        if (e.target.tagName !== "INPUT" && collapseAllBtn) {
          collapseAllBtn.click();
        }
        break;
      case "f":
        if (e.target.tagName !== "INPUT") {
          const container = getTasksContainer();
          const firstFailure = container
            ? container.querySelector(
              ".task-item.bg-red-100, .task-item.dark\\:bg-red-900\\/30",
            )
            : null;
          if (firstFailure) {
            firstFailure.scrollIntoView({
              behavior: "smooth",
              block: "center",
            });
            firstFailure.focus();
            firstFailure.setAttribute("open", "");
          }
        }
        break;
    }
  });
}
