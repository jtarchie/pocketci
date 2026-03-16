/**
 * PocketCI Pipeline API
 *
 * A pipeline is a TypeScript (or JavaScript) module that exports a single
 * `async` function named `pipeline`. The runtime calls it once per run.
 *
 * Minimal pipeline:
 * ```typescript
 * const pipeline = async () => {
 *   const result = await runtime.run({
 *     name: "hello",
 *     image: "busybox",
 *     command: { path: "echo", args: ["Hello, World!"] },
 *   });
 *   console.log(result.stdout);
 * };
 * export { pipeline };
 * ```
 *
 * All globals documented below (`runtime`, `notify`, `assert`, `storage`,
 * `http`, `YAML`, `pipelineContext`) are injected by the runtime — do NOT
 * import them.
 */
// types for the pipeline
declare global {
  // Common base types
  type SourceConfig = { [key: string]: string };
  type EnvVars = { [key: string]: string };
  type ParamsConfig = { [key: string]: string };

  interface CommandConfig {
    path: string;
    args?: string[];
    user?: string;
  }

  interface ContainerLimits {
    cpu?: number;
    memory?: number;
  }

  interface AssertionBase {
    execution?: string[];
  }

  interface TaskAssertion {
    stdout?: string;
    stderr?: string;
    code?: number | null;
  }

  /**
   * Callback invoked with streaming output chunks as a container runs.
   * `stream` is `"stdout"` or `"stderr"`; `data` is a raw string chunk.
   */
  type OutputCallback = (stream: "stdout" | "stderr", data: string) => void;

  // Callback invoked when cumulative agent usage counters change.
  type AgentUsageCallback = (usage: AgentUsage) => void;

  // Callback invoked when a new agent audit event is emitted.
  type AgentAuditEventCallback = (event: AuditEvent) => void;

  // ---------------------------------------------------------------------------
  // Runtime types
  // ---------------------------------------------------------------------------

  /**
   * Configuration for a single container task run via `runtime.run()`.
   *
   * **Mounts**: The keys of `mounts` are the directory names used *inside*
   * the container, resolved relative to the container's working directory
   * (or absolute if the key starts with `/`). Each value is a `VolumeResult`
   * returned by `runtime.createVolume()`. Volumes persist data between tasks.
   *
   * **Commands**: `command.path` is the executable; `command.args` are its
   * arguments (each as a separate string, not shell-split).
   *
   * @example
   * ```typescript
   * // Share data between two tasks via a named volume
   * const src = await runtime.createVolume();
   *
   * await runtime.run({
   *   name: "git-clone",
   *   image: "alpine/git",
   *   command: { path: "git", args: ["clone", "https://github.com/my/repo", "src"] },
   *   mounts: { src }, // volume mounted at ./src inside the container
   * });
   *
   * const result = await runtime.run({
   *   name: "run-tests",
   *   image: "golang:1.24",
   *   command: { path: "go", args: ["test", "./..."] },
   *   mounts: { src },     // same volume — sees the cloned repo
   *   work_dir: src.path,  // absolute path to the volume on the host
   * });
   * ```
   */
  interface RunTaskConfig {
    command: CommandConfig;
    container_limits?: ContainerLimits;
    env?: EnvVars;
    image: string;
    mounts?: KnownMounts;
    name: string;
    privileged?: boolean;
    stdin?: string;
    work_dir?: string;
    // Callback invoked with streaming output chunks as the container runs
    onOutput?: OutputCallback;
    timeout?: string;
    // When set, overrides the auto-generated storage path used by the runtime
    // so the caller's own storage entry is the single source of truth.
    storage_key?: string;
  }

  interface RunTaskResult {
    code: number;
    stderr: string;
    stdout: string;
    status: "complete" | "abort";
    message: string;
  }

  interface VolumeConfig {
    name?: string;
    size?: number;
  }

  interface VolumeResult {
    error: string;
    name: string;
    path: string; // Absolute path to the volume directory
  }

  type KnownMounts = Record<string, VolumeResult>;

  // Pipeline context provided by the runtime
  interface PipelineContext {
    runID?: string;
    pipelineID?: string;
    driverName?: string;
    triggeredBy?: string;
    /** Arguments passed from `ci run <name> [args...]` */
    args: string[];
  }
  /**
   * Metadata about the current run, injected by the runtime.
   * `args` contains any extra arguments passed via `ci run <name> [args...]`.
   *
   * @example
   * ```typescript
   * console.log(`run ${pipelineContext.runID} on ${pipelineContext.driverName}`);
   * const branch = pipelineContext.args[0] ?? "main";
   * ```
   */
  const pipelineContext: PipelineContext;

  // HTTP / Webhook types
  interface HttpRequest {
    method: string;
    url: string;
    headers: Record<string, string>;
    body: string;
    query: Record<string, string>;
    /** The detected webhook provider, e.g. "github", "slack". */
    provider: string;
    /** The provider-specific event type, e.g. "pull_request", "push". */
    eventType: string;
  }

  interface HttpResponse {
    status: number;
    body?: string;
    headers?: Record<string, string>;
  }

  /**
   * Webhook / HTTP integration. Only populated when the pipeline is triggered
   * via an HTTP POST to its webhook endpoint.
   *
   * @example
   * ```typescript
   * const req = http.request();
   * if (req) {
   *   const payload = JSON.parse(req.body);
   *   // respond immediately so the caller isn't blocked
   *   http.respond({ status: 200, body: "accepted" });
   *   // ... continue pipeline work ...
   * }
   * ```
   */
  namespace http {
    /**
     * Returns the incoming HTTP request data when triggered via webhook.
     * Returns undefined when the pipeline was not triggered via webhook.
     */
    function request(): HttpRequest | undefined;

    /**
     * Sends an HTTP response back to the webhook caller.
     * This is a one-shot operation — subsequent calls are silently ignored.
     * If not triggered via webhook, this is a no-op.
     * The pipeline continues executing after the response is sent.
     */
    function respond(response: HttpResponse): void;
  }

  /**
   * Persistent key-value store scoped to this pipeline run. Values are
   * JSON-serialisable. Use it to pass results between pipeline restarts or
   * to track resource versions across runs.
   *
   * @example
   * ```typescript
   * await storage.set("last-sha", result.stdout.trim());
   * const prev = storage.get("last-sha") as string | null;
   * ```
   */
  namespace storage {
    function set(key: string, value: unknown): Promise<void>;
    function get(key: string): unknown;
  }

  // Cumulative token counts and request stats.
  interface AgentUsage {
    promptTokens: number;
    completionTokens: number;
    totalTokens: number;
    llmRequests: number;
    toolCallCount: number;
  }

  // Result returned by runtime.agent().
  interface AgentResult {
    text: string;
    status: "success" | "failure" | "limit_exceeded";
    usage: AgentUsage;
    auditLog: AuditEvent[];
  }

  // Token counts for a single LLM event.
  interface AuditUsage {
    promptTokens: number;
    completionTokens: number;
    totalTokens: number;
  }

  // A single entry in the agent audit log.
  // type values: "pre_context" | "user_message" | "tool_call" | "tool_response" | "model_text" | "model_final"
  interface AuditEvent {
    timestamp?: string;
    invocationId?: string;
    author?: string;
    type: string;
    text?: string;
    toolName?: string;
    toolCallId?: string;
    toolArgs?: { [key: string]: unknown };
    toolResult?: { [key: string]: unknown };
    usage?: AuditUsage;
  }

  // Language model generation parameters for agent steps.
  interface AgentLLMConfig {
    temperature?: number;
    max_tokens?: number;
  }

  // Extended thinking configuration for supported models.
  // budget is the maximum thinking tokens (>= 1024).
  // level is Gemini-specific: low | medium | high | minimal.
  interface AgentThinkingConfig {
    budget: number;
    level?: "low" | "medium" | "high" | "minimal";
  }

  // Context window management configuration.
  interface AgentContextGuardConfig {
    strategy: "threshold" | "sliding_window";
    max_turns?: number; // sliding_window: compact after N turns
    max_tokens?: number; // threshold: manual context window override
  }

  // Output validation via an Expr boolean expression.
  // Evaluated after the agent completes; if false, a follow-up prompt is sent.
  interface AgentValidationConfig {
    /** Expr boolean expression evaluated with {text, status} environment. */
    expr: string;
    /** Custom follow-up prompt when validation fails. */
    prompt?: string;
  }

  // Hard limits that stop agent execution.
  interface AgentLimitsConfig {
    /** Stop after N LLM responses. Default: 50. */
    max_turns?: number;
    /** Stop when cumulative tokens reach this total. 0 = unlimited. */
    max_total_tokens?: number;
  }

  // Specifies a prior task whose output is pre-fetched into the agent's session
  // as a synthetic tool result before the first turn, saving orientation calls.
  interface AgentContextTask {
    /** Partial or full task name; closest match is used. */
    name: string;
    /** Which output field to inject: "stdout" | "stderr" | "both" (default). */
    field?: "stdout" | "stderr" | "both";
  }

  // Specifies a volume file whose contents are pre-read into the agent's session
  // history before the first turn, saving a read_file tool call.
  interface AgentContextFile {
    /** Path as "mountname/relative/path", e.g. "diff/pr.diff". */
    path: string;
    /** Per-file byte limit. Falls back to AgentContext.max_bytes (default 4096). */
    max_bytes?: number;
  }

  // Configures pre-fetched task outputs and file contents to inject before the
  // agent's first turn, eliminating orientation tool calls.
  interface AgentContext {
    tasks?: AgentContextTask[];
    /** Volume files to pre-inject as synthetic read_file results. */
    files?: AgentContextFile[];
    /** Maximum bytes per stdout/stderr/file field. Defaults to 4096. */
    max_bytes?: number;
  }

  // Input to runtime.agent().
  interface AgentRunConfig {
    name: string;
    prompt: string;
    model: string;
    image: string;
    mounts?: KnownMounts;
    outputVolumePath?: string;
    onOutput?: OutputCallback;
    onUsage?: AgentUsageCallback;
    onAuditEvent?: AgentAuditEventCallback;
    llm?: AgentLLMConfig;
    thinking?: AgentThinkingConfig;
    safety?: { [key: string]: string };
    context_guard?: AgentContextGuardConfig;
    limits?: AgentLimitsConfig;
    context?: AgentContext;
    validation?: AgentValidationConfig;
  }

  /**
   * Core container execution API. All functions are async and must be
   * `await`-ed. Volumes created here persist for the lifetime of the run.
   */
  namespace runtime {
    /**
     * Creates an ephemeral volume that can be shared between tasks via `mounts`.
     * Returns a `VolumeResult` with a `path` (absolute host path) and `name`.
     *
     * @example
     * ```typescript
     * const output = await runtime.createVolume();
     * // pass output as a mount key to runtime.run() or runtime.agent()
     * ```
     */
    function createVolume(volume?: VolumeConfig): Promise<VolumeResult>;

    /**
     * Runs a container task to completion and returns its stdout, stderr, and
     * exit code. Throws if the container cannot be started.
     *
     * The task `name` is used for display in the UI and must be unique within
     * a pipeline run.
     *
     * @example
     * ```typescript
     * const result = await runtime.run({
     *   name: "build",
     *   image: "golang:1.24",
     *   command: { path: "go", args: ["build", "./..."] },
     *   env: { CGO_ENABLED: "0" },
     * });
     * if (result.code !== 0) throw new Error(result.stderr);
     * ```
     */
    function run(task: RunTaskConfig): Promise<RunTaskResult>;

    /**
     * Starts a long-lived sandbox container kept alive with "tail -f /dev/null".
     * Requires an image that has `tail` available (not distroless/scratch).
     * Call `close()` on the returned handle when done to remove the container.
     */
    function startSandbox(config: SandboxConfig): Promise<SandboxHandle>;

    /**
     * Runs an LLM agent that can execute shell commands inside a sandbox
     * container. The agent iterates tool calls until it produces a final text
     * response. Use `mounts` to give the agent read/write access to volumes
     * populated by earlier tasks.
     *
     * `model` format: `"provider/model-name"` — see OpenRouter or the
     * configured LLM gateway for available model IDs.
     *
     * @example
     * ```typescript
     * const repo = await runtime.createVolume();
     * await runtime.run({
     *   name: "clone",
     *   image: "alpine/git",
     *   command: { path: "git", args: ["clone", "https://github.com/my/repo", "repo"] },
     *   mounts: { repo },
     * });
     *
     * const review = await runtime.agent({
     *   name: "code-review",
     *   prompt: "Review the last commit and summarise any issues.",
     *   model: "openrouter/google/gemini-2.5-flash",
     *   image: "alpine/git",
     *   mounts: { repo },
     * });
     * console.log(review.text);
     * ```
     */
    function agent(config: AgentRunConfig): Promise<AgentResult>;

    /**
     * Reads specific files from a named volume without spawning a container.
     * Returns a map of relative path to file content string.
     *
     * @param volumeName - The name of the volume to read from.
     * @param filePaths - One or more paths relative to the volume root.
     */
    function readFilesFromVolume(
      volumeName: string,
      ...filePaths: string[]
    ): Promise<Record<string, string>>;
  }

  /** Configuration for creating a sandbox. */
  interface SandboxConfig {
    image: string;
    name: string;
    env?: EnvVars;
    mounts?: KnownMounts;
    work_dir?: string;
    privileged?: boolean;
  }

  /** Configuration for a single exec call inside a sandbox. */
  interface SandboxExecConfig {
    command: CommandConfig;
    env?: EnvVars;
    work_dir?: string;
    stdin?: string;
    timeout?: string;
    /** Callback invoked with streaming output chunks as the command runs. */
    onOutput?: OutputCallback;
  }

  /** Handle to a running sandbox container. */
  interface SandboxHandle {
    /** Driver-specific identifier for the sandbox container. */
    id: string;
    /**
     * Runs a single command inside the sandbox.
     * env and work_dir apply only to this invocation — they do not persist.
     */
    exec(config: SandboxExecConfig): Promise<RunTaskResult>;
    /** Stops and removes the sandbox container. */
    close(): Promise<void>;
  }

  /**
   * Lightweight assertion helpers. A failing assertion throws an error that
   * aborts the pipeline run and marks it as failed.
   */
  namespace assert {
    function containsElement<T>(
      element: T,
      array: T[],
      message?: string,
    ): void;
    function containsString(
      substr: string,
      str: string,
      message?: string,
    ): void;
    function equal<T>(expected: T, actual: T, message?: string): void;
    function eventuallyContainsString(
      getter: () => unknown,
      substr: string,
      timeoutMs?: number,
      intervalMs?: number,
      message?: string,
    ): void;
    function notEqual<T>(expected: T, actual: T, message?: string): void;
    function truthy(value: unknown, message?: string): void;
  }

  /** Serialise/deserialise YAML inside pipeline scripts. */
  namespace YAML {
    function parse(text: string): object;
    function stringify(obj: object): string;
  }

  // Notification types
  interface NotifyConfig {
    type: "slack" | "teams" | "http";
    token?: string; // For Slack
    webhook?: string; // For Teams
    url?: string; // For HTTP
    channels?: string[]; // For Slack
    headers?: Record<string, string>; // For HTTP
    method?: string; // For HTTP (defaults to POST)
    recipients?: string[]; // Generic recipients
  }

  interface NotifyContext {
    pipelineName: string;
    jobName: string;
    buildID: string;
    status:
      | "pending"
      | "running"
      | "success"
      | "failure"
      | "error"
      | "limit_exceeded";
    startTime: string;
    endTime: string;
    duration: string;
    environment: Record<string, string>;
    taskResults: Record<string, unknown>;
  }

  interface NotifyInput {
    name: string; // Config name
    message: string; // Template message (Go template with Sprig)
    async?: boolean; // Fire-and-forget mode (default: false)
  }

  interface NotifyResult {
    success: boolean;
    error?: string;
  }

  /**
   * Notification API. Configure backends once with `setConfigs`, then call
   * `send` or `sendMultiple` at any point in the pipeline.
   *
   * Message templates use Go's `text/template` syntax with Sprig helpers.
   * Available template variables mirror `NotifyContext` fields (e.g.
   * `{{ .Status }}`, `{{ .PipelineName }}`).
   *
   * @example
   * ```typescript
   * notify.setConfigs({
   *   slack: {
   *     type: "slack",
   *     token: "xoxb-...",
   *     channels: ["#builds"],
   *   },
   * });
   * notify.setContext({
   *   pipelineName: "my-pipeline", jobName: "build", buildID: "1",
   *   status: "running", startTime: new Date().toISOString(),
   *   endTime: "", duration: "", environment: {}, taskResults: {},
   * });
   *
   * // ... run tasks ...
   *
   * notify.updateStatus("success");
   * await notify.send({
   *   name: "slack",
   *   message: "Build {{ .JobName }} finished: {{ .Status }}",
   * });
   * ```
   */
  namespace notify {
    /** Register one or more named notification backends. Call once at pipeline start. */
    function setConfigs(configs: Record<string, NotifyConfig>): void;
    /** Set the context object used to render notification message templates. */
    function setContext(ctx: NotifyContext): void;
    /** Update the `status` field of the current context (e.g. after tasks complete). */
    function updateStatus(status: string): void;
    /** Update the `jobName` field of the current context. */
    function updateJobName(jobName: string): void;
    /** Send a notification via the named backend configuration. */
    function send(input: NotifyInput): Promise<NotifyResult>;
    /** Send the same message to multiple named backends in one call. */
    function sendMultiple(
      names: string[],
      message: string,
      async?: boolean,
    ): Promise<NotifyResult>;
  }

  // Native Resources types
  interface ResourceVersion {
    [key: string]: string;
  }

  interface ResourceMetadataField {
    name: string;
    value: string;
  }

  interface ResourceCheckInput {
    type: string;
    source: { [key: string]: unknown };
    version?: ResourceVersion;
  }

  interface ResourceCheckResult {
    versions: ResourceVersion[];
  }

  interface ResourceFetchInput {
    type: string;
    source: { [key: string]: unknown };
    version: ResourceVersion;
    params?: { [key: string]: unknown };
    destDir: string;
  }

  interface ResourceFetchResult {
    version: ResourceVersion;
    metadata: ResourceMetadataField[];
  }

  interface ResourcePushInput {
    type: string;
    source: { [key: string]: unknown };
    params?: { [key: string]: unknown };
    srcDir: string;
  }

  interface ResourcePushResult {
    version: ResourceVersion;
    metadata: ResourceMetadataField[];
  }

  namespace nativeResources {
    function check(input: ResourceCheckInput): ResourceCheckResult;
    function fetch(input: ResourceFetchInput): ResourceFetchResult;
    function push(input: ResourcePushInput): ResourcePushResult;
    function isNative(resourceType: string): boolean;
    function listNativeResources(): string[];
  }
}

// types for backwards compatibility
declare global {
  // Common hook interfaces
  interface StepHooks {
    ensure?: Step;
    on_abort?: Step;
    on_error?: Step;
    on_success?: Step;
    on_failure?: Step;
    timeout?: string;
  }

  // Across modifier type
  interface AcrossVar {
    var: string;
    values: string[];
    max_in_flight?: number;
  }

  // Resource related interfaces
  interface ResourceBase {
    name: string;
    type: string;
    source: SourceConfig;
  }

  interface ImageResource {
    type: string;
    source: SourceConfig;
  }

  interface CacheConfig {
    path: string;
  }

  interface TaskConfig {
    caches?: CacheConfig[];
    container_limits?: ContainerLimits;
    env?: EnvVars;
    platform?: string;
    image_resource: ImageResource;
    inputs?: { name: string }[];
    outputs?: { name: string }[];
    run?: CommandConfig;
    params?: ParamsConfig;
  }

  // Step interfaces
  interface Task extends StepHooks {
    task: string;
    parallelism?: number;
    config: TaskConfig;
    container_limits?: ContainerLimits;
    file?: string;
    image?: string;
    privileged?: boolean;
    assert?: TaskAssertion;
    attempts?: number;
    across?: AcrossVar[];
    fail_fast?: boolean;
  }

  interface Get extends StepHooks {
    get: string;
    resource: string;
    params: ParamsConfig;
    trigger: boolean;
    version: string | ResourceVersion; // "latest" | "every" | {key: "value"} for pinned
    passed?: string[];
    attempts?: number;
    across?: AcrossVar[];
    fail_fast?: boolean;
  }

  interface Put extends StepHooks {
    put: string;
    resource: string;
    params: ParamsConfig;
    attempts?: number;
    across?: AcrossVar[];
    fail_fast?: boolean;
  }

  interface Do extends StepHooks {
    do: Step[];
    attempts?: number;
    across?: AcrossVar[];
    fail_fast?: boolean;
  }

  interface InParallel extends StepHooks {
    in_parallel: {
      steps: Step[];
      limit?: number;
      fail_fast?: boolean;
    };
    attempts?: number;
    across?: AcrossVar[];
  }

  interface Try extends StepHooks {
    try: Step[];
    attempts?: number;
    across?: AcrossVar[];
    fail_fast?: boolean;
  }

  // Notify step for sending notifications
  interface NotifyStep extends StepHooks {
    notify: string | string[]; // Config name(s)
    message: string; // Go template message with Sprig functions
    async?: boolean; // Fire-and-forget mode (default: false)
    attempts?: number;
    across?: AcrossVar[];
    fail_fast?: boolean;
  }

  // Agent step: runs an LLM agent with a run_command tool in a sandbox container.
  interface AgentStep extends StepHooks {
    agent: string; // Agent name (used as step identifier)
    prompt: string; // Instruction sent to the model
    model: string; // "provider/model-name", e.g. "openrouter/google/gemini-3"
    config?: TaskConfig; // image_resource, inputs, outputs
    file?: string; // Load full agent config from a YAML file on a volume
    prompt_file?: string; // Load prompt text from a file on a volume
    llm?: AgentLLMConfig;
    thinking?: AgentThinkingConfig;
    safety?: { [key: string]: string };
    context_guard?: AgentContextGuardConfig;
    limits?: AgentLimitsConfig;
    context?: AgentContext;
    validation?: AgentValidationConfig;
    attempts?: number;
    across?: AcrossVar[];
    fail_fast?: boolean;
  }

  type Step = Task | Get | Put | Do | Try | InParallel | NotifyStep | AgentStep;

  // Pipeline configuration
  interface Job extends StepHooks {
    name: string;
    max_in_flight?: number;
    plan: Step[];
    assert: AssertionBase;
  }

  interface JobConfig {
    name: string;
    max_in_flight?: number;
    plan: Step[];
    on_success?: Step;
    on_failure?: Step;
    on_error?: Step;
    on_abort?: Step;
    ensure?: Step;
    assert?: {
      execution?: string[];
    };
    /**
     * Structured trigger configuration. Namespaces trigger types under a
     * single field. Currently supports `webhook` triggers.
     *
     * @example
     * triggers:
     *   webhook:
     *     filter: 'provider == "github" && eventType == "pull_request"'
     *     params:
     *       PR_NUMBER: 'string(payload.number)'
     *       PR_REPO: "'https://github.com/' + payload.pull_request.head.repo.full_name + '.git'"
     */
    triggers?: {
      webhook?: {
        /** Boolean expr-lang expression. Same variables as `webhook_trigger`. */
        filter?: string;
        /**
         * Map of env var name → string expr-lang expression evaluated against
         * webhook metadata. Results are injected as env vars into all task
         * steps of the job.
         */
        params?: Record<string, string>;
      };
    };
    /**
     * An expr-lang boolean expression evaluated against webhook metadata.
     * When set, the job only runs if the expression returns true.
     * Ignored for manual (non-webhook) triggers.
     *
     * Deprecated: prefer `triggers.webhook.filter` for new pipelines.
     *
     * Available variables: provider, eventType, method, headers, query, body, payload
     *
     * @example
     * webhook_trigger: 'provider == "github" && eventType == "push"'
     */
    webhook_trigger?: string;
  }

  /**
   * Evaluates an expr-lang boolean expression against the current webhook
   * metadata. Returns true when the pipeline was not triggered by a webhook
   * (manual run), ensuring jobs always run in that case.
   *
   * @param expression - An expr-lang boolean expression. Available variables:
   *   provider, eventType, method, headers, query, body, payload
   */
  function webhookTrigger(expression: string): boolean;

  /**
   * Evaluates a map of expr-lang string expressions against the current webhook
   * metadata and returns a map of resolved string values. Returns an empty map
   * when the pipeline was not triggered by a webhook.
   *
   * Used internally by `triggers.webhook.params` to extract env vars from the
   * webhook payload.
   *
   * @param params - Map of env var name → expr-lang expression string.
   * @returns Map of env var name → resolved string value.
   */
  function webhookParams(
    params: Record<string, string>,
  ): Record<string, string>;

  type Resource = ResourceBase;
  type ResourceType = ResourceBase;

  interface PipelineConfig {
    assert: AssertionBase;
    max_in_flight?: number;
    jobs: Job[];
    notifications?: Record<string, NotifyConfig>; // Top-level notification configs
    resource_types: ResourceType[];
    resources: Resource[];
  }
}

export {};
