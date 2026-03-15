package k8s

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/jtarchie/pocketci/orchestra/cache"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

const cacheHelperImage = "busybox:latest"

// CopyToVolume implements cache.VolumeDataAccessor.
// Creates a temporary pod to extract tar data into the PVC.
func (k *K8s) CopyToVolume(ctx context.Context, volumeName string, reader io.Reader) error {
	pvcName := sanitizeName(fmt.Sprintf("%s-%s", k.namespace, volumeName))
	podName := sanitizeName(fmt.Sprintf("cache-helper-%s-%d", volumeName, time.Now().UnixNano()))

	// Create a temporary pod with the PVC mounted
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: podName,
			Labels: map[string]string{
				"orchestra.namespace": sanitizeLabel(k.namespace),
				"orchestra.role":      "cache-helper",
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:    "helper",
					Image:   cacheHelperImage,
					Command: []string{"sh", "-c", "tar xf - -C /volume && sleep infinity"},
					Stdin:   true,
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "cache-volume",
							MountPath: "/volume",
						},
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "cache-volume",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: pvcName,
						},
					},
				},
			},
		},
	}

	createdPod, err := k.clientset.CoreV1().Pods(k.k8sNamespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create cache helper pod: %w", err)
	}

	defer func() {
		_ = k.clientset.CoreV1().Pods(k.k8sNamespace).Delete(ctx, createdPod.Name, metav1.DeleteOptions{})
	}()

	// Wait for pod to be running
	if err := k.waitForPodRunning(ctx, createdPod.Name); err != nil {
		return fmt.Errorf("failed to wait for cache helper pod: %w", err)
	}

	// Stream tar data to the pod via stdin
	req := k.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(createdPod.Name).
		Namespace(k.k8sNamespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: "helper",
			Command:   []string{"tar", "xf", "-", "-C", "/volume"},
			Stdin:     true,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(k.config, "POST", req.URL())
	if err != nil {
		return fmt.Errorf("failed to create executor: %w", err)
	}

	var stdout, stderr bytes.Buffer

	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:  reader,
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		return fmt.Errorf("failed to stream to pod: %w (stderr: %s)", err, stderr.String())
	}

	return nil
}

// CopyFromVolume implements cache.VolumeDataAccessor.
// Creates a temporary pod to read tar data from the PVC.
func (k *K8s) CopyFromVolume(ctx context.Context, volumeName string) (io.ReadCloser, error) {
	pvcName := sanitizeName(fmt.Sprintf("%s-%s", k.namespace, volumeName))
	podName := sanitizeName(fmt.Sprintf("cache-helper-%s-%d", volumeName, time.Now().UnixNano()))

	// Create a temporary pod with the PVC mounted
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: podName,
			Labels: map[string]string{
				"orchestra.namespace": sanitizeLabel(k.namespace),
				"orchestra.role":      "cache-helper",
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:    "helper",
					Image:   cacheHelperImage,
					Command: []string{"sleep", "infinity"},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "cache-volume",
							MountPath: "/volume",
						},
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "cache-volume",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: pvcName,
						},
					},
				},
			},
		},
	}

	createdPod, err := k.clientset.CoreV1().Pods(k.k8sNamespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to create cache helper pod: %w", err)
	}

	// Wait for pod to be running
	if err := k.waitForPodRunning(ctx, createdPod.Name); err != nil {
		_ = k.clientset.CoreV1().Pods(k.k8sNamespace).Delete(ctx, createdPod.Name, metav1.DeleteOptions{})

		return nil, fmt.Errorf("failed to wait for cache helper pod: %w", err)
	}

	// Stream tar data from the pod
	req := k.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(createdPod.Name).
		Namespace(k.k8sNamespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: "helper",
			Command:   []string{"tar", "cf", "-", "-C", "/volume", "."},
			Stdin:     false,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(k.config, "POST", req.URL())
	if err != nil {
		_ = k.clientset.CoreV1().Pods(k.k8sNamespace).Delete(ctx, createdPod.Name, metav1.DeleteOptions{})

		return nil, fmt.Errorf("failed to create executor: %w", err)
	}

	// Create a pipe to stream the output
	pr, pw := io.Pipe()

	go func() {
		var stderr bytes.Buffer

		err := executor.StreamWithContext(ctx, remotecommand.StreamOptions{
			Stdout: pw,
			Stderr: &stderr,
		})
		if err != nil {
			pw.CloseWithError(fmt.Errorf("failed to stream from pod: %w (stderr: %s)", err, stderr.String()))
		} else {
			_ = pw.Close()
		}

		// Clean up the pod after streaming is done
		_ = k.clientset.CoreV1().Pods(k.k8sNamespace).Delete(ctx, createdPod.Name, metav1.DeleteOptions{})
	}()

	// Docker's CopyFromContainer returns a tar that includes the directory itself,
	// so we need to strip the leading "./volume/" from paths for consistency
	return newTarPathStripper(pr, "volume/"), nil
}

func (k *K8s) waitForPodRunning(ctx context.Context, podName string) error {
	for {
		pod, err := k.clientset.CoreV1().Pods(k.k8sNamespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				time.Sleep(100 * time.Millisecond)

				continue
			}

			return fmt.Errorf("failed to get pod: %w", err)
		}

		switch pod.Status.Phase {
		case corev1.PodRunning:
			return nil
		case corev1.PodFailed, corev1.PodSucceeded:
			return fmt.Errorf("pod finished unexpectedly with phase: %s", pod.Status.Phase)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// tarPathStripper wraps a tar reader to strip a prefix from file paths.
type tarPathStripper struct {
	reader *tar.Reader
	writer *tar.Writer
	pr     *io.PipeReader
	pw     *io.PipeWriter
	prefix string
}

func newTarPathStripper(src io.Reader, prefix string) io.ReadCloser {
	pr, pw := io.Pipe()

	stripper := &tarPathStripper{
		reader: tar.NewReader(src),
		writer: tar.NewWriter(pw),
		pr:     pr,
		pw:     pw,
		prefix: prefix,
	}

	go stripper.run()

	return pr
}

func (t *tarPathStripper) run() {
	defer func() {
		_ = t.pw.Close()
	}()
	defer func() {
		_ = t.writer.Close()
	}()

	for {
		header, err := t.reader.Next()
		if err == io.EOF {
			break
		}

		if err != nil {
			t.pw.CloseWithError(err)

			return
		}

		// Strip the prefix from the path
		header.Name = filepath.Clean(header.Name)
		if len(t.prefix) > 0 && len(header.Name) > len(t.prefix) {
			header.Name = header.Name[len(t.prefix):]
		}

		// Skip empty paths (the stripped directory itself)
		if header.Name == "" || header.Name == "." {
			continue
		}

		if err := t.writer.WriteHeader(header); err != nil {
			t.pw.CloseWithError(err)

			return
		}

		if header.Size > 0 {
			if _, err := io.CopyN(t.writer, t.reader, header.Size); err != nil {
				t.pw.CloseWithError(err)

				return
			}
		}
	}
}

// ReadFilesFromVolume implements cache.VolumeDataAccessor.
// Creates a temporary pod and execs tar to stream specific files from the PVC.
func (k *K8s) ReadFilesFromVolume(ctx context.Context, volumeName string, filePaths ...string) (io.ReadCloser, error) {
	pvcName := sanitizeName(fmt.Sprintf("%s-%s", k.namespace, volumeName))
	podName := sanitizeName(fmt.Sprintf("cache-helper-%s-%d", volumeName, time.Now().UnixNano()))

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: podName,
			Labels: map[string]string{
				"orchestra.namespace": sanitizeLabel(k.namespace),
				"orchestra.role":      "cache-helper",
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:    "helper",
					Image:   cacheHelperImage,
					Command: []string{"sleep", "infinity"},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "cache-volume",
							MountPath: "/volume",
						},
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "cache-volume",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: pvcName,
						},
					},
				},
			},
		},
	}

	createdPod, err := k.clientset.CoreV1().Pods(k.k8sNamespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to create cache helper pod: %w", err)
	}

	if err := k.waitForPodRunning(ctx, createdPod.Name); err != nil {
		_ = k.clientset.CoreV1().Pods(k.k8sNamespace).Delete(ctx, createdPod.Name, metav1.DeleteOptions{})

		return nil, fmt.Errorf("failed to wait for cache helper pod: %w", err)
	}

	// Build command: tar cf - -C /volume path1 path2 ...
	tarCmd := append([]string{"tar", "cf", "-", "-C", "/volume"}, filePaths...)

	req := k.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(createdPod.Name).
		Namespace(k.k8sNamespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: "helper",
			Command:   tarCmd,
			Stdin:     false,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(k.config, "POST", req.URL())
	if err != nil {
		_ = k.clientset.CoreV1().Pods(k.k8sNamespace).Delete(ctx, createdPod.Name, metav1.DeleteOptions{})

		return nil, fmt.Errorf("failed to create executor: %w", err)
	}

	pr, pw := io.Pipe()

	go func() {
		var stderr bytes.Buffer

		err := executor.StreamWithContext(ctx, remotecommand.StreamOptions{
			Stdout: pw,
			Stderr: &stderr,
		})
		if err != nil {
			pw.CloseWithError(fmt.Errorf("failed to stream from pod: %w (stderr: %s)", err, stderr.String()))
		} else {
			_ = pw.Close()
		}

		_ = k.clientset.CoreV1().Pods(k.k8sNamespace).Delete(ctx, createdPod.Name, metav1.DeleteOptions{})
	}()

	return pr, nil
}

var _ cache.VolumeDataAccessor = (*K8s)(nil)
