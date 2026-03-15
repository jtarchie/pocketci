// Pipeline Graph Visualization Module

// Graph configuration
const NODE_WIDTH = 140;
const NODE_HEIGHT = 40;
const NODE_MARGIN_X = 60;
const NODE_MARGIN_Y = 30;
const PADDING = 50;

/**
 * Initialize the pipeline graph visualization
 * @param {Object} graphData - The tree data from the server
 * @param {string} currentPath - The current path in the pipeline
 */
export function initGraph(graphData, _currentPath) {
  // State
  let scale = 1;
  let translateX = 0;
  let translateY = 0;
  let isDragging = false;
  let dragStartX = 0;
  let dragStartY = 0;
  let lastTranslateX = 0;
  let lastTranslateY = 0;

  // Edge display mode: 'tree' (parent->children) or 'flow' (sequential execution order)
  let edgeMode = localStorage.getItem("graphEdgeMode") || "tree";

  // DOM elements
  const container = document.getElementById("graph-container");
  const viewport = document.getElementById("graph-viewport");
  const nodesLayer = document.getElementById("nodes-layer");
  const edgesLayer = document.getElementById("edges-layer");
  const zoomInfo = document.getElementById("zoom-info");
  const helpToggle = document.getElementById("help-toggle");
  const helpPanel = document.getElementById("help-panel");
  const tooltip = document.getElementById("graph-tooltip");
  const tooltipName = document.getElementById("tooltip-name");
  const tooltipStatus = document.getElementById("tooltip-status");
  const tooltipType = document.getElementById("tooltip-type");
  const tooltipDurationLabel = document.getElementById(
    "tooltip-duration-label",
  );
  const tooltipDuration = document.getElementById("tooltip-duration");
  const searchInput = document.getElementById("search-input");
  const minimap = document.getElementById("minimap");
  const minimapNodes = document.getElementById("minimap-nodes");
  const minimapViewport = document.getElementById("minimap-viewport");

  // Check if we're on the graph page
  if (!container) {
    return;
  }

  // Store nodes for search
  let allNodes = [];
  let isInitialRender = true;
  let graphBounds = { minX: 0, minY: 0, maxX: 0, maxY: 0 };

  // Layout algorithm - tree-based hierarchical layout
  // This ensures children are grouped near their parent and parents are centered
  function layoutGraph(tree) {
    const nodes = [];
    const edges = [];
    const nodeMap = new Map(); // id -> node object

    // First pass: build node objects and collect all nodes
    function buildNodes(treeNode, depth, parentId) {
      if (!treeNode || !treeNode.name) return null;

      const id = treeNode.full_path || `node-${nodes.length}`;
      const isGroup = treeNode.children && treeNode.children.length > 0;
      const status = treeNode.value?.status || (isGroup ? "group" : "pending");
      const duration = treeNode.value?.duration || null;
      const startTime = treeNode.value?.start_time || null;
      const endTime = treeNode.value?.end_time || null;
      const dependsOn = treeNode.value?.dependsOn || [];

      const node = {
        id,
        name: treeNode.name,
        status,
        isGroup,
        depth, // Tree depth (will be recalculated for flow mode)
        flowDepth: 0, // Flow depth (sequential position)
        x: 0,
        y: 0,
        fullPath: treeNode.full_path || "",
        childIds: [],
        parentId,
        subtreeHeight: 0, // Will be calculated
        duration,
        startTime,
        endTime,
        order: nodes.length + 1, // Execution order
        dependsOn,
      };

      nodes.push(node);
      nodeMap.set(id, node);

      // In tree mode: parent connects to all children
      // In flow mode: we'll build edges after all nodes are created
      if (edgeMode === "tree" && parentId) {
        edges.push({ from: parentId, to: id });
      }

      // Process children and track their IDs
      if (treeNode.children) {
        treeNode.children.forEach((child) => {
          const childNode = buildNodes(child, depth + 1, id);
          if (childNode) {
            node.childIds.push(childNode.id);
          }
        });
      }

      return node;
    }

    // Handle root node specially
    const rootNodes = [];
    if (tree.children && tree.children.length > 0) {
      tree.children.forEach((child) => {
        const node = buildNodes(child, 0, null);
        if (node) rootNodes.push(node);
      });
    } else if (tree.name) {
      const node = buildNodes(tree, 0, null);
      if (node) rootNodes.push(node);
    }

    // Calculate flow depths for flow mode
    // In flow mode, siblings execute sequentially: P at col 0, C1 at col 1, C2 at col 2, etc.
    // Root nodes always start at column 0
    function calculateFlowDepth(node, currentFlowDepth) {
      node.flowDepth = currentFlowDepth;

      const childNodes = node.childIds.map((id) => nodeMap.get(id));
      // Children execute sequentially, so each gets the next column
      let nextDepth = currentFlowDepth + 1;
      childNodes.forEach((child) => {
        calculateFlowDepth(child, nextDepth);
        nextDepth++; // Each sibling goes to the next column
      });
    }

    // Only calculate flow depths in flow mode
    if (edgeMode === "flow") {
      // All root nodes start at column 0
      rootNodes.forEach((rootNode) => {
        calculateFlowDepth(rootNode, 0);
      });
    }

    // Second pass: calculate subtree heights (bottom-up)
    // Subtree height = sum of children's subtree heights, or 1 if leaf
    function calculateSubtreeHeight(node) {
      if (node.childIds.length === 0) {
        node.subtreeHeight = 1;
        return 1;
      }

      let totalHeight = 0;
      node.childIds.forEach((childId) => {
        const child = nodeMap.get(childId);
        totalHeight += calculateSubtreeHeight(child);
      });
      node.subtreeHeight = totalHeight;
      return totalHeight;
    }

    rootNodes.forEach((node) => calculateSubtreeHeight(node));

    // Third pass: assign positions
    if (edgeMode === "tree") {
      // Tree mode: use tree depth for X, subtree-based Y positioning
      const assignTreePositions = (node, yOffset) => {
        node.x = PADDING + node.depth * (NODE_WIDTH + NODE_MARGIN_X);

        if (node.childIds.length === 0) {
          node.y = PADDING + yOffset * (NODE_HEIGHT + NODE_MARGIN_Y);
          return;
        }

        let currentOffset = yOffset;
        const childNodes = node.childIds.map((id) => nodeMap.get(id));

        childNodes.forEach((child) => {
          assignTreePositions(child, currentOffset);
          currentOffset += child.subtreeHeight;
        });

        const firstChild = childNodes[0];
        const lastChild = childNodes[childNodes.length - 1];
        node.y = (firstChild.y + lastChild.y) / 2;
      };

      let globalOffset = 0;
      rootNodes.forEach((rootNode) => {
        assignTreePositions(rootNode, globalOffset);
        globalOffset += rootNode.subtreeHeight;
      });
    } else {
      // Flow mode: use flow depth for X, assign Y based on row
      // Track rows used at each flowDepth to avoid collisions
      const rowsByDepth = new Map();

      const assignFlowPositions = (node, preferredRow) => {
        const depth = node.flowDepth;
        node.x = PADDING + depth * (NODE_WIDTH + NODE_MARGIN_X);

        // Find an available row at this depth
        if (!rowsByDepth.has(depth)) {
          rowsByDepth.set(depth, new Set());
        }
        const usedRows = rowsByDepth.get(depth);

        let row = preferredRow;
        while (usedRows.has(row)) {
          row++;
        }
        usedRows.add(row);

        node.y = PADDING + row * (NODE_HEIGHT + NODE_MARGIN_Y);

        // Process children - each child tries to stay on same row as its flow predecessor
        const childNodes = node.childIds.map((id) => nodeMap.get(id));
        let nextRow = row;
        childNodes.forEach((child, index) => {
          if (index === 0) {
            // First child tries to stay on same row as parent
            assignFlowPositions(child, row);
          } else {
            // Subsequent children go to next available row
            nextRow++;
            assignFlowPositions(child, nextRow);
          }
        });
      };

      let globalRow = 0;
      rootNodes.forEach((rootNode) => {
        assignFlowPositions(rootNode, globalRow);
        // Find max row used and start next root after it
        let maxRow = globalRow;
        nodes.forEach((n) => {
          const nodeRow = Math.floor(
            (n.y - PADDING) / (NODE_HEIGHT + NODE_MARGIN_Y),
          );
          if (nodeRow > maxRow) maxRow = nodeRow;
        });
        globalRow = maxRow + 1;
      });
    }

    // In flow mode: build sequential edges based on execution order
    // Parent -> first child, then child1 -> child2 -> child3, etc.
    if (edgeMode === "flow") {
      const buildFlowEdges = (node) => {
        const childNodes = node.childIds.map((id) => nodeMap.get(id));
        if (childNodes.length > 0) {
          // Parent connects to first child only
          edges.push({ from: node.id, to: childNodes[0].id });

          // Each child connects to the next sibling (execution order chain)
          for (let i = 0; i < childNodes.length - 1; i++) {
            edges.push({ from: childNodes[i].id, to: childNodes[i + 1].id });
          }

          // Recursively process children
          childNodes.forEach((child) => buildFlowEdges(child));
        }
      };

      rootNodes.forEach((rootNode) => buildFlowEdges(rootNode));
    }

    return { nodes, edges };
  }

  // Render the graph from data
  function renderGraphFromData(data) {
    const { nodes, edges } = layoutGraph(data);
    allNodes = nodes; // Store for search

    // Clear existing content
    nodesLayer.innerHTML = "";
    edgesLayer.innerHTML = "";

    // Create a map for quick node lookup
    const nodeMap = new Map(nodes.map((n) => [n.id, n]));

    // Render edges first (so they appear behind nodes)
    edges.forEach((edge) => {
      const fromNode = nodeMap.get(edge.from);
      const toNode = nodeMap.get(edge.to);
      if (!fromNode || !toNode) return;

      const path = document.createElementNS(
        "http://www.w3.org/2000/svg",
        "path",
      );

      const x1 = fromNode.x + NODE_WIDTH;
      const y1 = fromNode.y + NODE_HEIGHT / 2;
      const x2 = toNode.x;
      const y2 = toNode.y + NODE_HEIGHT / 2;

      // Bezier curve for smooth edges
      const midX = (x1 + x2) / 2;
      const d = `M ${x1} ${y1} C ${midX} ${y1}, ${midX} ${y2}, ${x2} ${y2}`;

      path.setAttribute("d", d);
      path.setAttribute("class", "edge-line");
      path.setAttribute("marker-end", "url(#arrowhead)");

      edgesLayer.appendChild(path);
    });

    // Render dependency edges (job-to-job dependencies from "passed" constraints)
    // These are rendered with dashed lines to distinguish from parent-child edges
    // Build a map of job names to their nodes for dependency lookup
    // Key format: "pipelinePrefix/jobName" to avoid conflicts between pipeline runs
    const jobNodes = new Map();
    nodes.forEach((node) => {
      // Job nodes are at the "jobs" level - look for paths like /pipeline/.../jobs/jobname
      if (node.fullPath && node.fullPath.includes("/jobs/")) {
        // Extract the pipeline prefix (everything before /jobs/) and job name
        const jobsIndex = node.fullPath.indexOf("/jobs/");
        const pipelinePrefix = node.fullPath.substring(0, jobsIndex);
        const afterJobs = node.fullPath.substring(jobsIndex + 6); // skip "/jobs/"
        // Job name is the first segment after /jobs/
        const slashIndex = afterJobs.indexOf("/");
        const jobName = slashIndex === -1
          ? afterJobs
          : afterJobs.substring(0, slashIndex);
        // Only register if this is the actual job node (not a child like tasks/compile)
        if (slashIndex === -1) {
          const key = `${pipelinePrefix}/${jobName}`;
          jobNodes.set(key, node);
        }
      }
    });

    // Draw dependency edges
    nodes.forEach((node) => {
      if (node.dependsOn && node.dependsOn.length > 0) {
        // Extract pipeline prefix for this node
        const jobsIndex = node.fullPath ? node.fullPath.indexOf("/jobs/") : -1;
        if (jobsIndex === -1) return;
        const pipelinePrefix = node.fullPath.substring(0, jobsIndex);

        node.dependsOn.forEach((depJobName) => {
          const key = `${pipelinePrefix}/${depJobName}`;
          const depNode = jobNodes.get(key);
          if (!depNode) return;

          const path = document.createElementNS(
            "http://www.w3.org/2000/svg",
            "path",
          );

          // Draw from dependency job to this job
          const x1 = depNode.x + NODE_WIDTH;
          const y1 = depNode.y + NODE_HEIGHT / 2;
          const x2 = node.x;
          const y2 = node.y + NODE_HEIGHT / 2;

          // Use a curved path with offset to avoid overlapping with parent-child edges
          const midX = (x1 + x2) / 2;
          const offsetY = 15; // Slight vertical offset for visual separation
          const d = `M ${x1} ${y1} C ${midX} ${y1 + offsetY}, ${midX} ${
            y2 - offsetY
          }, ${x2} ${y2}`;

          path.setAttribute("d", d);
          path.setAttribute("class", "edge-line dependency-edge");
          path.setAttribute("marker-end", "url(#arrowhead-dependency)");

          edgesLayer.appendChild(path);
        });
      }
    });

    // Render nodes
    nodes.forEach((node, index) => {
      const g = document.createElementNS("http://www.w3.org/2000/svg", "g");
      g.setAttribute("class", "node-group");
      g.setAttribute("transform", `translate(${node.x}, ${node.y})`);
      g.setAttribute("tabindex", "0");
      g.setAttribute("role", "button");
      g.setAttribute(
        "aria-label",
        `${node.name}, status: ${node.status}${
          node.isGroup ? ", click to expand" : ""
        }`,
      );
      g.dataset.nodeId = node.id;
      g.dataset.index = index;

      // Node rectangle
      const rect = document.createElementNS(
        "http://www.w3.org/2000/svg",
        "rect",
      );
      rect.setAttribute("width", NODE_WIDTH);
      rect.setAttribute("height", NODE_HEIGHT);
      rect.setAttribute("class", `node-rect ${node.status}`);
      g.appendChild(rect);

      // Node text
      const text = document.createElementNS(
        "http://www.w3.org/2000/svg",
        "text",
      );
      text.setAttribute("x", NODE_WIDTH / 2);
      text.setAttribute("y", NODE_HEIGHT / 2);
      text.setAttribute("text-anchor", "middle");
      text.setAttribute("dominant-baseline", "central");
      text.setAttribute("class", "node-text");

      // Truncate text if too long
      let displayName = node.name;
      if (displayName.length > 16) {
        displayName = displayName.substring(0, 14) + "…";
      }
      text.textContent = displayName;
      g.appendChild(text);

      // Status indicator for groups
      if (node.isGroup) {
        const indicator = document.createElementNS(
          "http://www.w3.org/2000/svg",
          "text",
        );
        indicator.setAttribute("x", NODE_WIDTH - 10);
        indicator.setAttribute("y", NODE_HEIGHT / 2);
        indicator.setAttribute("text-anchor", "middle");
        indicator.setAttribute("dominant-baseline", "central");
        indicator.setAttribute("class", "node-text");
        indicator.setAttribute("font-size", "14");
        indicator.textContent = "›";
        g.appendChild(indicator);
      }

      // Order badge (top-left corner)
      const badgeRadius = 10;
      const badge = document.createElementNS(
        "http://www.w3.org/2000/svg",
        "circle",
      );
      badge.setAttribute("cx", -4);
      badge.setAttribute("cy", -4);
      badge.setAttribute("r", badgeRadius);
      badge.setAttribute("class", "order-badge");
      g.appendChild(badge);

      const badgeText = document.createElementNS(
        "http://www.w3.org/2000/svg",
        "text",
      );
      badgeText.setAttribute("x", -4);
      badgeText.setAttribute("y", -4);
      badgeText.setAttribute("text-anchor", "middle");
      badgeText.setAttribute("dominant-baseline", "central");
      badgeText.setAttribute("class", "order-badge-text");
      badgeText.textContent = node.order;
      g.appendChild(badgeText);

      // Tooltip handlers
      g.addEventListener("mouseenter", (e) => showTooltip(e, node));
      g.addEventListener("mouseleave", hideTooltip);
      g.addEventListener("focus", (e) => showTooltip(e, node));
      g.addEventListener("blur", hideTooltip);

      // Click handler
      g.addEventListener("click", () => handleNodeClick(node));
      g.addEventListener("keydown", (e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          handleNodeClick(node);
        }
      });

      nodesLayer.appendChild(g);
    });

    // Auto-fit only on initial render; preserve pan/zoom on updates
    if (nodes.length > 0) {
      if (isInitialRender) {
        autoFit(nodes);
        isInitialRender = false;
      } else {
        updateTransform();
      }
      renderMinimap(nodes);
    }
  }

  function handleNodeClick(node) {
    if (node.isGroup) {
      // Navigate to group
      window.location.href = `/graph${node.fullPath}/`;
    } else {
      // Navigate to task details
      window.location.href = `/tasks${node.fullPath}/`;
    }
  }

  function autoFit(nodes) {
    if (nodes.length === 0) return;

    const containerRect = container.getBoundingClientRect();

    // Calculate bounds
    let minX = Infinity,
      minY = Infinity,
      maxX = -Infinity,
      maxY = -Infinity;
    nodes.forEach((node) => {
      minX = Math.min(minX, node.x);
      minY = Math.min(minY, node.y);
      maxX = Math.max(maxX, node.x + NODE_WIDTH);
      maxY = Math.max(maxY, node.y + NODE_HEIGHT);
    });

    const graphWidth = maxX - minX + PADDING * 2;
    const graphHeight = maxY - minY + PADDING * 2;

    // Calculate scale to fit
    const scaleX = containerRect.width / graphWidth;
    const scaleY = containerRect.height / graphHeight;
    scale = Math.min(scaleX, scaleY, 1.5); // Max scale of 1.5
    scale = Math.max(scale, 0.1); // Min scale of 0.1

    // Center the graph
    translateX = (containerRect.width - graphWidth * scale) / 2 -
      minX * scale +
      PADDING * scale;
    translateY = (containerRect.height - graphHeight * scale) / 2 -
      minY * scale +
      PADDING * scale;

    updateTransform();
  }

  function updateTransform() {
    viewport.setAttribute(
      "transform",
      `translate(${translateX}, ${translateY}) scale(${scale})`,
    );
    zoomInfo.textContent = `Zoom: ${Math.round(scale * 100)}%`;
    updateMinimapViewport();
  }

  // Tooltip functions
  function showTooltip(event, node) {
    tooltipName.textContent = node.name;
    tooltipStatus.textContent = node.status.charAt(0).toUpperCase() +
      node.status.slice(1);
    tooltipType.textContent = node.isGroup ? "Group" : "Task";

    // Show duration if available
    if (node.duration) {
      tooltipDurationLabel.style.display = "";
      tooltipDuration.style.display = "";
      tooltipDuration.textContent = formatDuration(node.duration);
    } else if (node.startTime && node.endTime) {
      tooltipDurationLabel.style.display = "";
      tooltipDuration.style.display = "";
      const duration = new Date(node.endTime) - new Date(node.startTime);
      tooltipDuration.textContent = formatDuration(duration);
    } else {
      tooltipDurationLabel.style.display = "none";
      tooltipDuration.style.display = "none";
    }

    // Position tooltip near cursor
    const containerRect = container.getBoundingClientRect();
    let x = event.clientX - containerRect.left + 15;
    let y = event.clientY - containerRect.top + 15;

    // Keep tooltip within container bounds
    const tooltipRect = tooltip.getBoundingClientRect();
    if (x + tooltipRect.width > containerRect.width) {
      x = event.clientX - containerRect.left - tooltipRect.width - 15;
    }
    if (y + tooltipRect.height > containerRect.height) {
      y = event.clientY - containerRect.top - tooltipRect.height - 15;
    }

    tooltip.style.left = `${x}px`;
    tooltip.style.top = `${y}px`;
    tooltip.classList.add("visible");
  }

  function hideTooltip() {
    tooltip.classList.remove("visible");
  }

  function formatDuration(ms) {
    if (typeof ms === "string") {
      // Parse Go duration string (e.g., "1.5s", "2m30s")
      return ms;
    }
    if (ms < 1000) return `${ms}ms`;
    if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`;
    const mins = Math.floor(ms / 60000);
    const secs = ((ms % 60000) / 1000).toFixed(0);
    return `${mins}m ${secs}s`;
  }

  // Minimap functions
  function renderMinimap(nodes) {
    if (nodes.length === 0) return;

    minimapNodes.innerHTML = "";

    // Calculate bounds
    let minX = Infinity,
      minY = Infinity,
      maxX = -Infinity,
      maxY = -Infinity;
    nodes.forEach((node) => {
      minX = Math.min(minX, node.x);
      minY = Math.min(minY, node.y);
      maxX = Math.max(maxX, node.x + NODE_WIDTH);
      maxY = Math.max(maxY, node.y + NODE_HEIGHT);
    });

    graphBounds = { minX, minY, maxX, maxY };

    const graphWidth = maxX - minX;
    const graphHeight = maxY - minY;

    const minimapRect = minimap.getBoundingClientRect();
    const minimapScale = Math.min(
      (minimapRect.width - 10) / graphWidth,
      (minimapRect.height - 10) / graphHeight,
    );

    // Render mini nodes
    nodes.forEach((node) => {
      const rect = document.createElementNS(
        "http://www.w3.org/2000/svg",
        "rect",
      );
      rect.setAttribute("x", 5 + (node.x - minX) * minimapScale);
      rect.setAttribute("y", 5 + (node.y - minY) * minimapScale);
      rect.setAttribute("width", Math.max(NODE_WIDTH * minimapScale, 3));
      rect.setAttribute("height", Math.max(NODE_HEIGHT * minimapScale, 2));
      rect.setAttribute("class", `minimap-node node-rect ${node.status}`);
      rect.dataset.nodeId = node.id;
      minimapNodes.appendChild(rect);
    });

    updateMinimapViewport();
  }

  function updateMinimapViewport() {
    if (!graphBounds.maxX) return;

    const containerRect = container.getBoundingClientRect();
    const minimapRect = minimap.getBoundingClientRect();

    const graphWidth = graphBounds.maxX - graphBounds.minX;
    const graphHeight = graphBounds.maxY - graphBounds.minY;

    const minimapScale = Math.min(
      (minimapRect.width - 10) / graphWidth,
      (minimapRect.height - 10) / graphHeight,
    );

    // Calculate visible area in graph coordinates
    const visibleX = -translateX / scale;
    const visibleY = -translateY / scale;
    const visibleWidth = containerRect.width / scale;
    const visibleHeight = containerRect.height / scale;

    // Convert to minimap coordinates
    minimapViewport.setAttribute(
      "x",
      5 + (visibleX - graphBounds.minX) * minimapScale,
    );
    minimapViewport.setAttribute(
      "y",
      5 + (visibleY - graphBounds.minY) * minimapScale,
    );
    minimapViewport.setAttribute("width", visibleWidth * minimapScale);
    minimapViewport.setAttribute("height", visibleHeight * minimapScale);
  }

  // Search functionality
  function handleSearch(query) {
    const nodeElements = nodesLayer.querySelectorAll(".node-group");

    if (!query.trim()) {
      // Clear search - show all nodes normally
      nodeElements.forEach((el) => {
        el.classList.remove("search-match", "search-dim");
      });
      return;
    }

    const lowerQuery = query.toLowerCase();
    let hasMatch = false;

    nodeElements.forEach((el, index) => {
      const node = allNodes[index];
      if (node && node.name.toLowerCase().includes(lowerQuery)) {
        el.classList.add("search-match");
        el.classList.remove("search-dim");
        hasMatch = true;
      } else {
        el.classList.remove("search-match");
        el.classList.add("search-dim");
      }
    });

    // If there's a match, pan to the first one
    if (hasMatch) {
      const matchIndex = allNodes.findIndex((n) =>
        n.name.toLowerCase().includes(lowerQuery)
      );
      if (matchIndex >= 0) {
        const node = allNodes[matchIndex];
        panToNode(node);
      }
    }
  }

  function panToNode(node) {
    const containerRect = container.getBoundingClientRect();
    // Center the node in the viewport
    translateX = containerRect.width / 2 - (node.x + NODE_WIDTH / 2) * scale;
    translateY = containerRect.height / 2 - (node.y + NODE_HEIGHT / 2) * scale;
    updateTransform();
  }

  // Search input handler
  let searchTimeout;
  searchInput.addEventListener("input", (e) => {
    clearTimeout(searchTimeout);
    searchTimeout = setTimeout(() => handleSearch(e.target.value), 150);
  });

  searchInput.addEventListener("keydown", (e) => {
    if (e.key === "Escape") {
      searchInput.value = "";
      handleSearch("");
      searchInput.blur();
    }
  });

  function zoom(delta, centerX, centerY) {
    const oldScale = scale;
    scale = Math.max(0.1, Math.min(3, scale * (1 + delta)));

    // Zoom towards the center point
    if (centerX !== undefined && centerY !== undefined) {
      translateX = centerX - (centerX - translateX) * (scale / oldScale);
      translateY = centerY - (centerY - translateY) * (scale / oldScale);
    }

    updateTransform();
  }

  function pan(dx, dy) {
    translateX += dx;
    translateY += dy;
    updateTransform();
  }

  function resetView() {
    scale = 1;
    translateX = 0;
    translateY = 0;
    isInitialRender = true;
    renderGraphFromData(graphData); // This will auto-fit
  }

  // Event handlers
  container.addEventListener(
    "wheel",
    (e) => {
      e.preventDefault();
      const rect = container.getBoundingClientRect();
      const centerX = e.clientX - rect.left;
      const centerY = e.clientY - rect.top;
      zoom(e.deltaY > 0 ? -0.1 : 0.1, centerX, centerY);
    },
    { passive: false },
  );

  container.addEventListener("mousedown", (e) => {
    if (e.target.closest(".node-group")) return;
    isDragging = true;
    dragStartX = e.clientX;
    dragStartY = e.clientY;
    lastTranslateX = translateX;
    lastTranslateY = translateY;
    container.style.cursor = "grabbing";
  });

  document.addEventListener("mousemove", (e) => {
    if (!isDragging) return;
    translateX = lastTranslateX + (e.clientX - dragStartX);
    translateY = lastTranslateY + (e.clientY - dragStartY);
    updateTransform();
  });

  document.addEventListener("mouseup", () => {
    isDragging = false;
    container.style.cursor = "grab";
  });

  // Touch support
  let lastTouchDistance = 0;
  let lastTouchCenter = { x: 0, y: 0 };

  container.addEventListener(
    "touchstart",
    (e) => {
      if (e.touches.length === 1) {
        isDragging = true;
        dragStartX = e.touches[0].clientX;
        dragStartY = e.touches[0].clientY;
        lastTranslateX = translateX;
        lastTranslateY = translateY;
      } else if (e.touches.length === 2) {
        const dx = e.touches[0].clientX - e.touches[1].clientX;
        const dy = e.touches[0].clientY - e.touches[1].clientY;
        lastTouchDistance = Math.sqrt(dx * dx + dy * dy);
        lastTouchCenter = {
          x: (e.touches[0].clientX + e.touches[1].clientX) / 2,
          y: (e.touches[0].clientY + e.touches[1].clientY) / 2,
        };
      }
    },
    { passive: true },
  );

  container.addEventListener(
    "touchmove",
    (e) => {
      e.preventDefault();
      if (e.touches.length === 1 && isDragging) {
        translateX = lastTranslateX + (e.touches[0].clientX - dragStartX);
        translateY = lastTranslateY + (e.touches[0].clientY - dragStartY);
        updateTransform();
      } else if (e.touches.length === 2) {
        const dx = e.touches[0].clientX - e.touches[1].clientX;
        const dy = e.touches[0].clientY - e.touches[1].clientY;
        const distance = Math.sqrt(dx * dx + dy * dy);
        const delta = (distance - lastTouchDistance) / lastTouchDistance;

        const rect = container.getBoundingClientRect();
        const centerX = lastTouchCenter.x - rect.left;
        const centerY = lastTouchCenter.y - rect.top;

        zoom(delta * 0.5, centerX, centerY);
        lastTouchDistance = distance;
      }
    },
    { passive: false },
  );

  container.addEventListener("touchend", () => {
    isDragging = false;
    lastTouchDistance = 0;
  });

  // Keyboard navigation
  container.addEventListener("keydown", (e) => {
    const PAN_AMOUNT = 50;

    switch (e.key) {
      case "+":
      case "=":
        e.preventDefault();
        zoom(0.2);
        break;
      case "-":
        e.preventDefault();
        zoom(-0.2);
        break;
      case "0":
        e.preventDefault();
        resetView();
        break;
      case "ArrowUp":
        e.preventDefault();
        pan(0, PAN_AMOUNT);
        break;
      case "ArrowDown":
        e.preventDefault();
        pan(0, -PAN_AMOUNT);
        break;
      case "ArrowLeft":
        e.preventDefault();
        pan(PAN_AMOUNT, 0);
        break;
      case "ArrowRight":
        e.preventDefault();
        pan(-PAN_AMOUNT, 0);
        break;
    }
  });

  // Zoom buttons
  document.getElementById("zoom-in").addEventListener("click", () => zoom(0.2));
  document
    .getElementById("zoom-out")
    .addEventListener("click", () => zoom(-0.2));
  document.getElementById("zoom-reset").addEventListener("click", resetView);

  // Edge mode toggle
  const edgeModeTree = document.getElementById("edge-mode-tree");
  const edgeModeFlow = document.getElementById("edge-mode-flow");

  function updateEdgeModeButtons() {
    if (edgeMode === "tree") {
      edgeModeTree.classList.remove(
        "bg-gray-200",
        "dark:bg-gray-700",
        "text-gray-700",
        "dark:text-gray-300",
        "hover:bg-gray-300",
        "dark:hover:bg-gray-600",
      );
      edgeModeTree.classList.add("bg-blue-600", "text-white");
      edgeModeTree.setAttribute("aria-checked", "true");

      edgeModeFlow.classList.remove("bg-blue-600", "text-white");
      edgeModeFlow.classList.add(
        "bg-gray-200",
        "dark:bg-gray-700",
        "text-gray-700",
        "dark:text-gray-300",
        "hover:bg-gray-300",
        "dark:hover:bg-gray-600",
      );
      edgeModeFlow.setAttribute("aria-checked", "false");
    } else {
      edgeModeFlow.classList.remove(
        "bg-gray-200",
        "dark:bg-gray-700",
        "text-gray-700",
        "dark:text-gray-300",
        "hover:bg-gray-300",
        "dark:hover:bg-gray-600",
      );
      edgeModeFlow.classList.add("bg-blue-600", "text-white");
      edgeModeFlow.setAttribute("aria-checked", "true");

      edgeModeTree.classList.remove("bg-blue-600", "text-white");
      edgeModeTree.classList.add(
        "bg-gray-200",
        "dark:bg-gray-700",
        "text-gray-700",
        "dark:text-gray-300",
        "hover:bg-gray-300",
        "dark:hover:bg-gray-600",
      );
      edgeModeTree.setAttribute("aria-checked", "false");
    }
  }

  if (edgeModeTree && edgeModeFlow) {
    // Initialize button states
    updateEdgeModeButtons();

    edgeModeTree.addEventListener("click", () => {
      if (edgeMode !== "tree") {
        edgeMode = "tree";
        localStorage.setItem("graphEdgeMode", "tree");
        updateEdgeModeButtons();
        isInitialRender = true;
        renderGraphFromData(graphData);
      }
    });

    edgeModeFlow.addEventListener("click", () => {
      if (edgeMode !== "flow") {
        edgeMode = "flow";
        localStorage.setItem("graphEdgeMode", "flow");
        updateEdgeModeButtons();
        isInitialRender = true;
        renderGraphFromData(graphData);
      }
    });
  }

  // Help panel toggle
  helpToggle.addEventListener("click", () => {
    const isVisible = helpPanel.classList.toggle("visible");
    helpToggle.setAttribute("aria-expanded", isVisible);
  });

  // Close help panel when clicking outside
  document.addEventListener("click", (e) => {
    if (!helpPanel.contains(e.target) && e.target !== helpToggle) {
      helpPanel.classList.remove("visible");
      helpToggle.setAttribute("aria-expanded", "false");
    }
  });

  // Initialize
  renderGraphFromData(graphData);

  // Announce for screen readers
  const announcer = document.createElement("div");
  announcer.setAttribute("role", "status");
  announcer.setAttribute("aria-live", "polite");
  announcer.classList.add("sr-only");
  announcer.textContent =
    `Pipeline graph loaded with ${nodesLayer.children.length} nodes. Use Tab to navigate between nodes, arrow keys to pan, and plus/minus to zoom.`;
  document.body.appendChild(announcer);

  const STATUS_CLASSES = [
    "success",
    "failure",
    "error",
    "pending",
    "running",
    "abort",
    "skipped",
    "group",
  ];

  // Update only node status colors without full re-render (preserves pan/zoom)
  function updateNodeStatuses(newNodes) {
    const statusById = new Map(newNodes.map((node) => [node.id, node]));

    const nodeGroups = nodesLayer.querySelectorAll(".node-group");
    nodeGroups.forEach((group) => {
      const nodeId = group.dataset.nodeId;
      const node = statusById.get(nodeId);
      if (!node) return;

      const rect = group.querySelector(".node-rect");
      if (rect) {
        STATUS_CLASSES.forEach((name) => rect.classList.remove(name));
        rect.classList.add(node.status);
      }

      group.setAttribute(
        "aria-label",
        `${node.name}, status: ${node.status}${
          node.isGroup ? ", click to expand" : ""
        }`,
      );
    });

    // Update minimap
    const minimapRects = minimapNodes.querySelectorAll(".minimap-node");
    minimapRects.forEach((rect) => {
      const nodeId = rect.dataset.nodeId;
      const node = statusById.get(nodeId);
      if (!node) return;
      STATUS_CLASSES.forEach((name) => rect.classList.remove(name));
      rect.classList.add(node.status);
    });
  }

  // Check if graph structure changed (ignoring status)
  function structureChanged(oldNodes, newNodes) {
    if (oldNodes.length !== newNodes.length) return true;
    for (let i = 0; i < oldNodes.length; i++) {
      if (oldNodes[i].id !== newNodes[i].id) return true;
    }
    return false;
  }

  // Listen for htmx swaps on the graph data element for live updates
  document.body.addEventListener("htmx:afterSwap", (event) => {
    const target = event.detail.target;
    if (!target || target.id !== "graph-data") return;

    try {
      const newData = JSON.parse(target.textContent);
      const { nodes: newNodes } = layoutGraph(newData);

      if (!structureChanged(allNodes, newNodes)) {
        // Structure same — just update status colors in-place
        allNodes = newNodes;
        updateNodeStatuses(newNodes);
      } else {
        // Structure changed — full re-render (preserves current pan/zoom)
        graphData = newData;
        renderGraphFromData(newData);
      }
    } catch (e) {
      console.error("Failed to update graph from htmx swap:", e);
    }
  });
}
