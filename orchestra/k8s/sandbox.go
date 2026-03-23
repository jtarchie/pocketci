package k8s

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/jtarchie/pocketci/orchestra"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	k8sexec "k8s.io/client-go/util/exec"
)

// K8sSandbox keeps a Pod alive with "tail -f /dev/null" and dispatches
// sequential exec calls via the pod /exec subresource.
type K8sSandbox struct {
	podName      string
	k8sNamespace string
	clientset    *kubernetes.Clientset
	config       *rest.Config
}

var _ orchestra.Sandbox = (*K8sSandbox)(nil)

// ID returns the pod name as the sandbox identifier.
func (s *K8sSandbox) ID() string {
	return s.podName
}

// Exec runs cmd inside the sandbox pod and streams output to stdout/stderr.
// env and workDir apply only to this invocation.
func (s *K8sSandbox) Exec(
	ctx context.Context,
	cmd []string,
	env map[string]string,
	workDir string,
	stdin io.Reader,
	stdout, stderr io.Writer,
) (orchestra.ContainerStatus, error) {
	// Wrap command with env exports and workDir when needed.
	if len(env) > 0 || workDir != "" {
		var shell []string

		shell = append(shell, "env")

		for k, v := range env {
			shell = append(shell, k+"="+v)
		}

		if workDir != "" {
			shell = append(shell, "/bin/sh", "-c", "cd "+workDir+" && exec "+joinArgs(cmd))
		} else {
			shell = append(shell, cmd...)
		}

		cmd = shell
	}

	req := s.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(s.podName).
		Namespace(s.k8sNamespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: "sandbox",
			Command:   cmd,
			Stdin:     stdin != nil,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(s.config, "POST", req.URL())
	if err != nil {
		return nil, fmt.Errorf("sandbox exec: failed to create executor: %w", err)
	}

	var stdinReader io.Reader
	if stdin != nil {
		stdinReader = stdin
	}

	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:  stdinReader,
		Stdout: stdout,
		Stderr: stderr,
	})

	exitCode := 0

	if err != nil {
		var exitErr k8sexec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitStatus()
		} else {
			return nil, fmt.Errorf("sandbox exec: stream failed: %w", err)
		}
	}

	return &ContainerStatus{
		terminated: true,
		exitCode:   int32(exitCode),
	}, nil
}

// Cleanup deletes the sandbox pod.
func (s *K8sSandbox) Cleanup(ctx context.Context) error {
	err := s.clientset.CoreV1().Pods(s.k8sNamespace).Delete(ctx, s.podName, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("sandbox cleanup: failed to delete pod: %w", err)
	}

	return nil
}

// joinArgs joins a command slice into a shell-quoted string for exec wrapping.
func joinArgs(cmd []string) string {
	return strings.Join(cmd, " ")
}

// buildSandboxVolumes creates PVCs and returns the volume + mount specs for a sandbox pod.
func (k *K8s) buildSandboxVolumes(ctx context.Context, podName string, mounts []orchestra.Mount) ([]corev1.Volume, []corev1.VolumeMount, error) {
	volumes := []corev1.Volume{}
	volumeMounts := []corev1.VolumeMount{}

	for _, taskMount := range mounts {
		vol, err := k.CreateVolume(ctx, taskMount.Name, 0)
		if err != nil {
			return nil, nil, fmt.Errorf("sandbox: failed to create volume: %w", err)
		}

		k8sVol, _ := vol.(*Volume)
		sanitizedName := sanitizeName(taskMount.Name)

		volumes = append(volumes, corev1.Volume{
			Name: sanitizedName,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: k8sVol.pvcName,
				},
			},
		})

		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      sanitizedName,
			MountPath: filepath.Join("/tmp", podName, taskMount.Path),
		})
	}

	return volumes, volumeMounts, nil
}

// StartSandbox implements orchestra.SandboxDriver.
// It creates a Pod running "tail -f /dev/null", waits for it to be Running,
// then returns a K8sSandbox handle backed by the pod /exec subresource.
func (k *K8s) StartSandbox(ctx context.Context, task orchestra.Task) (orchestra.Sandbox, error) {
	logger := k.logger.With("taskID", task.ID)

	podName := sanitizeName(fmt.Sprintf("%s-%s-sandbox", k.namespace, task.ID))

	labels := map[string]string{
		"orchestra.namespace": sanitizeLabel(k.namespace),
		"orchestra.task":      sanitizeLabel(task.ID),
		"orchestra.sandbox":   "true",
	}

	// Build volumes and mounts.
	volumes, volumeMounts, err := k.buildSandboxVolumes(ctx, podName, task.Mounts)
	if err != nil {
		return nil, err
	}

	env := []corev1.EnvVar{}
	for k, v := range task.Env {
		env = append(env, corev1.EnvVar{Name: k, Value: v})
	}

	workDir := task.WorkDir
	if workDir == "" {
		workDir = filepath.Join("/tmp", podName)
	}

	resources := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    *resource.NewMilliQuantity(100, resource.DecimalSI),
			corev1.ResourceMemory: *resource.NewQuantity(128*1024*1024, resource.BinarySI),
		},
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: k.k8sNamespace,
			Labels:    labels,
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:         "sandbox",
					Image:        task.Image,
					Command:      []string{"tail", "-f", "/dev/null"},
					Env:          env,
					WorkingDir:   workDir,
					VolumeMounts: volumeMounts,
					Resources:    resources,
				},
			},
			Volumes: volumes,
		},
	}

	if task.Privileged {
		priv := true
		pod.Spec.Containers[0].SecurityContext = &corev1.SecurityContext{
			Privileged: &priv,
		}
	}

	_, err = k.clientset.CoreV1().Pods(k.k8sNamespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("sandbox: failed to create pod: %w", err)
	}

	// Wait for the pod to reach Running state.
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	for {
		p, err := k.clientset.CoreV1().Pods(k.k8sNamespace).Get(waitCtx, podName, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("sandbox: failed to get pod: %w", err)
		}

		if p.Status.Phase == corev1.PodRunning {
			for _, cs := range p.Status.ContainerStatuses {
				if cs.State.Running != nil {
					logger.Debug("sandbox.pod.running", "pod", podName)

					return &K8sSandbox{
						podName:      podName,
						k8sNamespace: k.k8sNamespace,
						clientset:    k.clientset,
						config:       k.config,
					}, nil
				}
			}
		}

		if p.Status.Phase == corev1.PodFailed || p.Status.Phase == corev1.PodSucceeded {
			return nil, fmt.Errorf("sandbox: pod %s terminated unexpectedly with phase %s", podName, p.Status.Phase)
		}

		select {
		case <-waitCtx.Done():
			return nil, fmt.Errorf("sandbox: timed out waiting for pod %s to start", podName)
		case <-time.After(200 * time.Millisecond):
		}
	}
}
