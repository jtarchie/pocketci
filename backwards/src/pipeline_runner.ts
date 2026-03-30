/// <reference path="../../packages/pocketci/src/global.d.ts" />

import { JobRunner } from "./job_runner.ts";
import { extractJobDependencies, getBuildID } from "./utils.ts";

export class PipelineRunner {
  private jobResults: Map<string, boolean> = new Map();
  private executedJobs: string[] = [];

  constructor(private config: PipelineConfig) {
    this.addBuiltInResourceTypes();
    this.validatePipelineConfig();
    this.initializeNotifications();
  }

  private addBuiltInResourceTypes(): void {
    // Only registry-image is built-in as it's required for image_resource in tasks
    const registryImageType: ResourceType = {
      name: "registry-image",
      type: "registry-image",
      source: { repository: "concourse/registry-image-resource" },
    };

    const exists = this.config.resource_types.some(
      (type) => type.name === "registry-image",
    );

    if (!exists) {
      this.config.resource_types.push(registryImageType);
    }
  }

  private initializeNotifications(): void {
    // Set up notification configs if present
    if (this.config.notifications) {
      notify.setConfigs(this.config.notifications);
    }

    // Use pipelineContext.runID if available (from server), otherwise fall back to timestamp
    const buildID = getBuildID();

    // Initialize notify context with pipeline info
    notify.setContext({
      pipelineName: this.config.jobs[0]?.name || "unknown",
      jobName: "",
      buildID: buildID,
      status: "pending",
      startTime: new Date().toISOString(),
      endTime: "",
      duration: "",
      environment: {},
      taskResults: {},
    });
  }

  private validatePipelineConfig(): void {
    assert.truthy(
      this.config.jobs.length > 0,
      "Pipeline must have at least one job",
    );

    assert.truthy(
      this.config.jobs.every((job) => job.plan.length > 0),
      "Every job must have at least one step",
    );

    // Ensure job names are unique
    const jobNames = this.config.jobs.map((job) => job.name);
    assert.equal(
      jobNames.length,
      new Set(jobNames).size,
      "Job names must be unique",
    );

    // Validate that all passed constraints reference existing jobs
    if (this.config.jobs.length > 1) {
      this.validateJobDependencies();
    }

    if (this.config.resources.length > 0) {
      this.validateResources();
    }
  }

  private validateJobDependencies(): void {
    const jobNames = new Set(this.config.jobs.map((job) => job.name));

    // Check that all passed constraints reference existing jobs
    assert.truthy(
      this.config.jobs.every((job) =>
        job.plan.every((step) => {
          if ("get" in step && step.passed) {
            return step.passed.every((passedJob) => jobNames.has(passedJob));
          }
          return true;
        })
      ),
      "All passed constraints must reference existing jobs",
    );

    // Check for circular dependencies
    this.detectCircularDependencies();
  }

  private detectCircularDependencies(): void {
    // Build job dependency graph
    const graph: Record<string, string[]> = {};

    // Initialize empty adjacency lists
    for (const job of this.config.jobs) {
      graph[job.name] = [];
    }

    // Populate adjacency lists
    for (const job of this.config.jobs) {
      for (const step of job.plan) {
        if ("get" in step && step.passed) {
          for (const dependency of step.passed) {
            graph[dependency].push(job.name);
          }
        }
      }
    }

    // Check for cycles using DFS
    const visited = new Set<string>();
    const recStack = new Set<string>();

    const hasCycle = (node: string): boolean => {
      if (!visited.has(node)) {
        visited.add(node);
        recStack.add(node);

        for (const neighbor of graph[node]) {
          if (!visited.has(neighbor) && hasCycle(neighbor)) {
            return true;
          } else if (recStack.has(neighbor)) {
            return true;
          }
        }
      }

      recStack.delete(node);
      return false;
    };

    for (const job of this.config.jobs) {
      if (!visited.has(job.name) && hasCycle(job.name)) {
        assert.truthy(false, "Pipeline contains circular job dependencies");
      }
    }
  }

  private validateResources(): void {
    assert.truthy(
      this.config.resources.every((resource) =>
        this.config.resource_types.some((type) => type.name === resource.type)
      ),
      "Every resource must have a valid resource type",
    );

    assert.truthy(
      this.config.jobs.every((job) =>
        job.plan.every((step) => {
          if ("get" in step) {
            return this.config.resources.some((resource) =>
              resource.name === step.get
            );
          }
          return true; // not a resource step, ignore lookup
        })
      ),
      "Every get must have a resource reference",
    );
  }

  async run(): Promise<void> {
    // Pre-write all jobs as pending for graph visualization
    this.writeAllJobsAsPending();

    const targetJobs: string[] = pipelineContext?.targetJobs ?? [];

    if (targetJobs.length > 0) {
      // Targeted execution: run only specified jobs, then cascade forward
      await this.runTargetedJobs(targetJobs);
    } else {
      // Full execution: run all root jobs in dependency order
      const jobsWithNoDeps = this.findJobsWithNoDependencies();

      for (const job of jobsWithNoDeps) {
        await this.runJob(job);
      }
    }

    if (this.config.assert?.execution) {
      // this assures that the outputs are in the same order as the job
      assert.equal(this.executedJobs, this.config.assert.execution);
    }
  }

  private async runTargetedJobs(targetJobNames: string[]): Promise<void> {
    const jobsByName = new Map(
      this.config.jobs.map((job) => [job.name, job]),
    );

    for (const jobName of targetJobNames) {
      const job = jobsByName.get(jobName);
      if (!job) {
        throw new Error(`Target job "${jobName}" not found in pipeline`);
      }

      // Check passed constraints against prior run results (cross-run)
      if (!this.canJobRun(job)) {
        const buildID = getBuildID();
        const storageKey = `/pipeline/${buildID}/jobs/${job.name}`;
        storage.set(storageKey, {
          status: "skipped",
          reason: "passed constraints not satisfied",
        });
        continue;
      }

      // Run the target job; runJob handles cascading to dependents
      await this.runJob(job);
    }

    // Mark remaining non-executed jobs as skipped
    for (const job of this.config.jobs) {
      if (!this.jobResults.has(job.name)) {
        const buildID = getBuildID();
        const storageKey = `/pipeline/${buildID}/jobs/${job.name}`;
        storage.set(storageKey, { status: "skipped" });
      }
    }
  }

  private writeAllJobsAsPending(): void {
    const buildID = getBuildID();
    for (const job of this.config.jobs) {
      const dependsOn = extractJobDependencies(job.plan);
      const storageKey = `/pipeline/${buildID}/jobs/${job.name}`;
      const blockedBy = this.getBlockedByInfo(job);
      storage.set(storageKey, {
        status: "pending",
        dependsOn,
        ...(blockedBy.length > 0 && { blockedBy }),
      });
    }
  }

  private getBlockedByInfo(
    job: Job,
  ): Array<{ job: string; lastStatus: string }> {
    const blocked: Array<{ job: string; lastStatus: string }> = [];
    for (const step of job.plan) {
      if ("get" in step && step.passed?.length) {
        for (const dep of step.passed) {
          if (!this.isJobPassedSatisfied(dep)) {
            const pid = pipelineContext?.pipelineID ?? "";
            const lastStatus = storage.getMostRecentJobStatus(pid, dep) ??
              "never-run";
            blocked.push({ job: dep, lastStatus });
          }
        }
      }
    }
    return blocked;
  }

  private findJobsWithNoDependencies(): Job[] {
    return this.config.jobs.filter((job) => {
      const hasPassedConstraints = job.plan.some((s) =>
        "get" in s && s.passed?.length
      );
      if (!hasPassedConstraints) return true;
      // Include jobs whose cross-run passed constraints are already satisfied
      return this.canJobRun(job);
    });
  }

  private async runJob(job: Job): Promise<void> {
    // Guard against double-execution when a job's cross-run deps are satisfied
    // and it appears in both findJobsWithNoDependencies and runDependentJobs.
    if (this.jobResults.has(job.name)) return;

    this.executedJobs.push(job.name);

    try {
      // Wait for gate approval before executing the job's plan.
      if (job.gate) {
        await pipeline.gate(job.name, {
          message: job.gate.message,
          timeout: job.gate.timeout,
        });
      }

      const jobRunner = new JobRunner(
        job,
        this.config.resources,
        this.config.resource_types,
        this.config.max_in_flight,
      );
      await jobRunner.run();

      // Mark job as successful
      this.jobResults.set(job.name, true);

      // Find and run jobs that depend on this job
      await this.runDependentJobs(job.name);
    } catch (error) {
      // Mark job as failed
      this.jobResults.set(job.name, false);
      throw error;
    }
  }

  private async runDependentJobs(completedJobName: string): Promise<void> {
    const dependentJobs = this.findDependentJobs(completedJobName);

    for (const job of dependentJobs) {
      // Check if all dependencies are satisfied
      const canRun = this.canJobRun(job);
      if (canRun) {
        await this.runJob(job);
      }
    }
  }

  private findDependentJobs(jobName: string): Job[] {
    return this.config.jobs.filter((job) => {
      // Check if this job has a get step with a passed constraint including jobName
      return job.plan.some((step) => {
        if ("get" in step && step.passed && step.passed.includes(jobName)) {
          return true;
        }
        return false;
      });
    });
  }

  private canJobRun(job: Job): boolean {
    // Check if all passed constraints are satisfied (current run or cross-run)
    for (const step of job.plan) {
      if ("get" in step && step.passed && step.passed.length > 0) {
        const allDependenciesMet = step.passed.every((depJobName) =>
          this.isJobPassedSatisfied(depJobName)
        );

        if (!allDependenciesMet) {
          return false;
        }
      }
    }

    return true;
  }

  private isJobPassedSatisfied(depJobName: string): boolean {
    // Current run (in-memory) takes priority
    if (this.jobResults.has(depJobName)) {
      return this.jobResults.get(depJobName) === true;
    }
    // Cross-run: check most recent prior execution
    const pipelineID = pipelineContext?.pipelineID ?? "";
    const status = storage.getMostRecentJobStatus(pipelineID, depJobName);
    return status === "success";
  }
}
