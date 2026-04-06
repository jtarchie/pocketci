package k8s

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jtarchie/pocketci/orchestra"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/utils/ptr"
)

// sanitizeName converts a string to a valid Kubernetes resource name (DNS-1123 subdomain)
// Must consist of lowercase alphanumeric characters, '-' or '.', and must start and end with an alphanumeric character
func sanitizeName(name string) string {
	// Convert to lowercase
	name = strings.ToLower(name)

	// Replace underscores and other invalid characters with hyphens
	reg := regexp.MustCompile(`[^a-z0-9.-]+`)
	name = reg.ReplaceAllString(name, "-")

	// Ensure it starts with an alphanumeric character
	name = strings.TrimLeft(name, "-.")

	// Ensure it ends with an alphanumeric character
	name = strings.TrimRight(name, "-.")

	// Kubernetes resource names have a max length of 253 characters
	if len(name) > 253 {
		name = name[:253]
		// Re-trim end in case we cut in the middle of invalid characters
		name = strings.TrimRight(name, "-.")
	}

	return name
}

// sanitizeLabel converts a string to a valid Kubernetes label value
// Must be an empty string or consist of alphanumeric characters, '-', '_' or '.', and must start and end with an alphanumeric character
func sanitizeLabel(label string) string {
	if label == "" {
		return label
	}

	// Replace invalid characters with hyphens
	reg := regexp.MustCompile(`[^a-zA-Z0-9._-]+`)
	label = reg.ReplaceAllString(label, "-")

	// Ensure it starts with an alphanumeric character
	label = strings.TrimLeft(label, "-._")

	// Ensure it ends with an alphanumeric character
	label = strings.TrimRight(label, "-._")

	// Kubernetes labels have a max length of 63 characters
	if len(label) > 63 {
		label = label[:63]
		// Re-trim end in case we cut in the middle of invalid characters
		label = strings.TrimRight(label, "-._")
	}

	return label
}

type Container struct {
	clientset    *kubernetes.Clientset
	config       *rest.Config
	jobName      string
	podName      string
	k8sNamespace string
	task         orchestra.Task
	logger       *slog.Logger
}

// ID returns the Kubernetes job name as the container identifier.
func (c *Container) ID() string {
	return c.jobName
}

type ContainerStatus struct {
	phase      corev1.PodPhase
	exitCode   int32
	terminated bool
}

// resolvePodName returns the pod name for this job, looking it up and caching
// the result if not already known.
func (c *Container) resolvePodName(ctx context.Context) string {
	if c.podName != "" {
		return c.podName
	}

	pods, err := c.clientset.CoreV1().Pods(c.k8sNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: "job-name=" + c.jobName,
	})
	if err == nil && len(pods.Items) > 0 {
		c.podName = pods.Items[0].Name
	}

	return c.podName
}

func (c *Container) Status(ctx context.Context) (orchestra.ContainerStatus, error) {
	// Get job status for completion tracking
	job, err := c.clientset.BatchV1().Jobs(c.k8sNamespace).Get(ctx, c.jobName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get job: %w", err)
	}

	status := &ContainerStatus{}

	// Check job completion status
	if job.Status.Succeeded > 0 {
		status.phase = corev1.PodSucceeded
		status.terminated = true
		status.exitCode = 0
		return status, nil
	}

	if job.Status.Failed > 0 {
		status.phase = corev1.PodFailed
		status.terminated = true
		status.exitCode = 1 // Default failure code

		// Try to get actual exit code from pod
		podName := c.resolvePodName(ctx)

		if podName == "" {
			return status, nil
		}

		pod, err := c.clientset.CoreV1().Pods(c.k8sNamespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil || len(pod.Status.ContainerStatuses) == 0 {
			return status, nil
		}

		containerStatus := pod.Status.ContainerStatuses[0]
		if containerStatus.State.Terminated != nil {
			status.exitCode = containerStatus.State.Terminated.ExitCode
		}

		return status, nil
	}

	// Job still running, get pod status for phase
	podName := c.resolvePodName(ctx)

	if podName != "" {
		pod, err := c.clientset.CoreV1().Pods(c.k8sNamespace).Get(ctx, podName, metav1.GetOptions{})
		if err == nil {
			status.phase = pod.Status.Phase
			if len(pod.Status.ContainerStatuses) > 0 {
				containerStatus := pod.Status.ContainerStatuses[0]
				if containerStatus.State.Terminated != nil {
					status.terminated = true
					status.exitCode = containerStatus.State.Terminated.ExitCode
				}
			}
		}
	}

	return status, nil
}

// Logs retrieves container logs. When follow is false, returns all logs up to now.
// When follow is true, streams logs in real-time until the context is cancelled.
func (c *Container) Logs(ctx context.Context, stdout, stderr io.Writer, follow bool) error {
	if follow {
		return c.streamLogs(ctx, stdout)
	}

	// Kubernetes 1.32+ supports separate stdout/stderr streams via the PodLogsQuerySplitStreams feature gate
	// If the feature gate is not enabled, this will fall back to interleaved logs

	// Helper function to fetch a specific stream
	fetchStream := func(streamName *string, writer io.Writer) error {
		if writer == nil {
			return nil
		}

		req := c.clientset.CoreV1().Pods(c.k8sNamespace).GetLogs(c.podName, &corev1.PodLogOptions{
			Container: "task",
			Stream:    streamName,
		})

		podLogs, err := req.Stream(ctx)
		if err != nil {
			return fmt.Errorf("failed to get pod logs for stream %v: %w", streamName, err)
		}
		defer func() {
			closeErr := podLogs.Close()
			if closeErr != nil {
				c.logger.Warn("failed to close pod logs stream", "stream", streamName, "err", closeErr)
			}
		}()

		_, err = io.Copy(writer, podLogs)
		if err != nil {
			return fmt.Errorf("failed to copy logs for stream %v: %w", streamName, err)
		}

		return nil
	}

	// Fetch stdout stream
	streamStdout := "Stdout"
	err := fetchStream(&streamStdout, stdout)
	if err != nil {
		// If split streams are not supported, fall back to getting all logs
		c.logger.Debug("split streams not supported, falling back to combined logs", "err", err)

		req := c.clientset.CoreV1().Pods(c.k8sNamespace).GetLogs(c.podName, &corev1.PodLogOptions{
			Container: "task",
		})

		podLogs, err := req.Stream(ctx)
		if err != nil {
			return fmt.Errorf("failed to get pod logs: %w", err)
		}
		defer func() {
			closeErr := podLogs.Close()
			if closeErr != nil {
				c.logger.Warn("failed to close pod logs stream", "err", closeErr)
			}
		}()

		_, err = io.Copy(stdout, podLogs)
		if err != nil {
			return fmt.Errorf("failed to copy logs: %w", err)
		}

		return nil
	}

	// Fetch stderr stream
	streamStderr := "Stderr"
	err = fetchStream(&streamStderr, stderr)
	if err != nil {
		return err
	}

	return nil
}

// streamLogs follows container logs in real-time until the context is cancelled.
func (c *Container) streamLogs(ctx context.Context, stdout io.Writer) error {
	req := c.clientset.CoreV1().Pods(c.k8sNamespace).GetLogs(c.podName, &corev1.PodLogOptions{
		Container: "task",
		Follow:    true,
	})

	podLogs, err := req.Stream(ctx)
	if err != nil {
		return fmt.Errorf("failed to get pod logs stream: %w", err)
	}
	defer func() {
		closeErr := podLogs.Close()
		if closeErr != nil && ctx.Err() == nil {
			c.logger.Warn("failed to close pod logs stream", "err", closeErr)
		}
	}()

	_, err = io.Copy(stdout, podLogs)
	if err != nil && ctx.Err() == nil {
		return fmt.Errorf("failed to copy logs: %w", err)
	}

	return nil
}

func (c *Container) Cleanup(ctx context.Context) error {
	deletePolicy := metav1.DeletePropagationForeground
	// Delete the job (which will cascade delete the pod)
	err := c.clientset.BatchV1().Jobs(c.k8sNamespace).Delete(ctx, c.jobName, metav1.DeleteOptions{
		PropagationPolicy: &deletePolicy,
	})
	if err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete job: %w", err)
	}

	return nil
}

func (s *ContainerStatus) IsDone() bool {
	return s.terminated || s.phase == corev1.PodSucceeded || s.phase == corev1.PodFailed
}

func (s *ContainerStatus) ExitCode() int {
	return int(s.exitCode)
}

func (k *K8s) RunContainer(ctx context.Context, task orchestra.Task) (orchestra.Container, error) {
	logger := k.logger.With("taskID", task.ID)

	// Sanitize job name to comply with k8s naming (lowercase alphanumeric + hyphens/dots)
	jobName := sanitizeName(fmt.Sprintf("%s-%s", k.namespace, task.ID))

	// Check if job already exists
	existingJob, err := k.clientset.BatchV1().Jobs(k.k8sNamespace).Get(ctx, jobName, metav1.GetOptions{})
	if err == nil {
		logger.Debug("job.exists", "name", jobName)

		// Find the pod created by this job
		podName := ""
		pods, err := k.clientset.CoreV1().Pods(k.k8sNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: "job-name=" + jobName,
		})
		if err == nil && len(pods.Items) > 0 {
			podName = pods.Items[0].Name
		}

		return &Container{
			clientset:    k.clientset,
			config:       k.config,
			jobName:      existingJob.Name,
			podName:      podName,
			k8sNamespace: k.k8sNamespace,
			task:         task,
			logger:       logger,
		}, nil
	}

	volumes, volumeMounts, err := k.buildK8sVolumes(ctx, task, jobName, logger)
	if err != nil {
		return nil, err
	}

	env := buildK8sEnvVars(task)
	resources := buildResourceRequirements(task)

	// Build the pod template spec
	enabledStdin := task.Stdin != nil

	labels := map[string]string{
		"orchestra.namespace": sanitizeLabel(k.namespace),
		"orchestra.task":      sanitizeLabel(task.ID),
	}

	workDir := task.WorkDir
	if workDir == "" {
		workDir = filepath.Join("/tmp", jobName)
	}

	podTemplateSpec := corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels: labels,
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:         "task",
					Image:        task.Image,
					Command:      task.Command,
					Env:          env,
					VolumeMounts: volumeMounts,
					WorkingDir:   workDir,
					Resources:    resources,
					Stdin:        enabledStdin,
					StdinOnce:    enabledStdin,
				},
			},
			Volumes: volumes,
		},
	}

	applySecurityContext(&podTemplateSpec, task, logger)

	// Create the Job (wraps the pod)
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:   jobName,
			Labels: labels,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: ptr.To(int32(0)), // No retries (match current pod behavior)
			Template:     podTemplateSpec,
		},
	}

	_, err = k.clientset.BatchV1().Jobs(k.k8sNamespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		logger.Error("job.create", "name", jobName, "err", err)
		return nil, fmt.Errorf("failed to create job: %w", err)
	}

	// Wait for the job to create its pod and get the pod name
	var podName string
	if !enabledStdin || task.Stdin == nil {
		return &Container{
			clientset:    k.clientset,
			config:       k.config,
			jobName:      jobName,
			podName:      podName,
			k8sNamespace: k.k8sNamespace,
			task:         task,
			logger:       logger,
		}, nil
	}

	// Attach stdin to the running pod
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	podName, err = k.waitForPodCreation(waitCtx, jobName, logger)
	if err != nil {
		return nil, err
	}

	err = k.watchPodUntilRunning(waitCtx, podName, logger)
	if err != nil {
		return nil, err
	}

	err = k.attachStdinToPod(ctx, podName, task, logger)
	if err != nil {
		return nil, err
	}

	return &Container{
		clientset:    k.clientset,
		config:       k.config,
		jobName:      jobName,
		podName:      podName,
		k8sNamespace: k.k8sNamespace,
		task:         task,
		logger:       logger,
	}, nil
}

func (k *K8s) buildK8sVolumes(ctx context.Context, task orchestra.Task, jobName string, logger *slog.Logger) ([]corev1.Volume, []corev1.VolumeMount, error) {
	var volumes []corev1.Volume
	var volumeMounts []corev1.VolumeMount

	for _, taskMount := range task.Mounts {
		volume, err := k.CreateVolume(ctx, taskMount.Name, 0)
		if err != nil {
			logger.Error("volume.create.k8s.error", "name", taskMount.Name, "err", err)
			return nil, nil, fmt.Errorf("failed to create volume: %w", err)
		}

		k8sVolume, _ := volume.(*Volume)
		sanitizedVolumeName := sanitizeName(taskMount.Name)

		volumes = append(volumes, corev1.Volume{
			Name: sanitizedVolumeName,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: k8sVolume.pvcName,
				},
			},
		})

		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      sanitizedVolumeName,
			MountPath: filepath.Join("/tmp", jobName, taskMount.Path),
		})
	}

	return volumes, volumeMounts, nil
}

func buildK8sEnvVars(task orchestra.Task) []corev1.EnvVar {
	env := make([]corev1.EnvVar, 0, len(task.Env))
	for k, v := range task.Env {
		env = append(env, corev1.EnvVar{
			Name:  k,
			Value: v,
		})
	}

	return env
}

func buildResourceRequirements(task orchestra.Task) corev1.ResourceRequirements {
	resources := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    *resource.NewMilliQuantity(100, resource.DecimalSI),     // 0.1 CPU
			corev1.ResourceMemory: *resource.NewQuantity(128*1024*1024, resource.BinarySI), // 128Mi
		},
	}

	if task.ContainerLimits.CPU > 0 || task.ContainerLimits.Memory > 0 {
		resources.Limits = corev1.ResourceList{}

		if task.ContainerLimits.CPU > 0 {
			millicores := (task.ContainerLimits.CPU * 1000) / 1024
			resources.Limits[corev1.ResourceCPU] = *resource.NewMilliQuantity(millicores, resource.DecimalSI)
			resources.Requests[corev1.ResourceCPU] = *resource.NewMilliQuantity(millicores/2, resource.DecimalSI)
		}

		if task.ContainerLimits.Memory > 0 {
			resources.Limits[corev1.ResourceMemory] = *resource.NewQuantity(task.ContainerLimits.Memory, resource.BinarySI)
			resources.Requests[corev1.ResourceMemory] = *resource.NewQuantity(task.ContainerLimits.Memory/2, resource.BinarySI)
		}
	}

	return resources
}

func parseUserUID(user string) (*int64, error) {
	if user == "" {
		return nil, nil //nolint:nilnil // nil pointer signals no UID constraint; not an error
	}

	var uid int64

	switch user {
	case "root":
		uid = 0
	default:
		_, err := fmt.Sscanf(user, "%d", &uid)
		if err != nil {
			return nil, fmt.Errorf("cannot parse user %q as UID: %w", user, err)
		}
	}

	return &uid, nil
}

func applySecurityContext(podTemplateSpec *corev1.PodTemplateSpec, task orchestra.Task, logger *slog.Logger) {
	uid, err := parseUserUID(task.User)
	if err != nil {
		logger.Warn("user.parse", "user", task.User, "err", err, "msg", "using default user")
	}

	if uid != nil {
		podTemplateSpec.Spec.Containers[0].SecurityContext = &corev1.SecurityContext{
			RunAsUser: uid,
		}
	}

	if task.Privileged {
		if podTemplateSpec.Spec.Containers[0].SecurityContext == nil {
			podTemplateSpec.Spec.Containers[0].SecurityContext = &corev1.SecurityContext{}
		}

		privileged := true
		podTemplateSpec.Spec.Containers[0].SecurityContext.Privileged = &privileged
	}
}

func isPodContainerRunning(p *corev1.Pod) bool {
	if p.Status.Phase != corev1.PodRunning {
		return false
	}

	for _, cs := range p.Status.ContainerStatuses {
		if cs.State.Running != nil {
			return true
		}
	}

	return false
}

func (k *K8s) waitForPodCreation(ctx context.Context, jobName string, logger *slog.Logger) (string, error) {
	logger.Debug("job.waiting.for.pod", "name", jobName)

	for {
		pods, err := k.clientset.CoreV1().Pods(k.k8sNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: "job-name=" + jobName,
		})
		if err != nil {
			return "", fmt.Errorf("failed to list pods for job: %w", err)
		}

		if len(pods.Items) > 0 {
			podName := pods.Items[0].Name
			logger.Debug("job.pod.found", "job", jobName, "pod", podName)

			return podName, nil
		}

		select {
		case <-ctx.Done():
			return "", errors.New("timeout waiting for job to create pod")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (k *K8s) watchPodUntilRunning(ctx context.Context, podName string, logger *slog.Logger) error {
	logger.Debug("pod.stdin", "name", podName)

	watcher, err := k.clientset.CoreV1().Pods(k.k8sNamespace).Watch(ctx, metav1.ListOptions{
		FieldSelector: "metadata.name=" + podName,
	})
	if err != nil {
		logger.Error("pod.watch.failed", "name", podName, "err", err)

		return fmt.Errorf("failed to watch pod: %w", err)
	}
	defer watcher.Stop()

	for event := range watcher.ResultChan() {
		if event.Type != watch.Modified && event.Type != watch.Added {
			select {
			case <-ctx.Done():
				return errors.New("timeout waiting for pod to reach running state")
			default:
			}

			continue
		}

		p, ok := event.Object.(*corev1.Pod)
		if !ok {
			continue
		}

		logger.Debug("pod.status.update", "name", podName, "phase", p.Status.Phase, "containers", len(p.Status.ContainerStatuses))

		if isPodContainerRunning(p) || p.Status.Phase == corev1.PodSucceeded {
			if p.Status.Phase == corev1.PodSucceeded {
				logger.Debug("pod.completed", "name", podName, "state", "quickly")
			}

			return nil
		}

		if p.Status.Phase == corev1.PodFailed {
			return fmt.Errorf("pod failed to start: %s", p.Status.Message)
		}

		select {
		case <-ctx.Done():
			return errors.New("timeout waiting for pod to reach running state")
		default:
		}
	}

	return errors.New("pod did not reach running state")
}

func (k *K8s) attachStdinToPod(ctx context.Context, podName string, task orchestra.Task, logger *slog.Logger) error {
	logger.Debug("pod.running", "name", podName)

	req := k.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(k.k8sNamespace).
		SubResource("attach").
		VersionedParams(&corev1.PodAttachOptions{
			Stdin:     true,
			Container: "task",
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(k.config, "POST", req.URL())
	if err != nil {
		logger.Error("pod.attach.executor.errored", "name", podName, "err", err)

		return fmt.Errorf("failed to create attach executor: %w", err)
	}

	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin: task.Stdin,
	})
	if err != nil {
		logger.Error("pod.attach.stream.errored", "name", podName, "err", err)

		return fmt.Errorf("failed to stream stdin: %w", err)
	}

	logger.Debug("pod.stdin.complete", "name", podName)

	return nil
}
