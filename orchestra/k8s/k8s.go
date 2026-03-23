package k8s

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jtarchie/pocketci/orchestra"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// ServerConfig holds server-level configuration for the Kubernetes driver.
type ServerConfig struct {
	Kubeconfig   string `json:"kubeconfig,omitempty"`    // Path to kubeconfig file; empty uses in-cluster config
	K8sNamespace string `json:"k8s_namespace,omitempty"` // Kubernetes namespace for jobs (default: "default")
}

// DriverName implements orchestra.DriverConfig.
func (ServerConfig) DriverName() string { return "k8s" }

// Config holds the full configuration for the Kubernetes driver.
type Config struct {
	ServerConfig
	Namespace string // Per-execution namespace identifier
}

type K8s struct {
	clientset    *kubernetes.Clientset
	config       *rest.Config
	logger       *slog.Logger
	namespace    string // Orchestra namespace (for labeling)
	k8sNamespace string // Kubernetes namespace (for resource placement)
}

// Close implements orchestra.Driver.
func (k *K8s) Close() error {
	ctx := context.Background()

	// Delete all jobs in the namespace with our label (pods will be cascade deleted)
	labelSelector := "orchestra.namespace=" + sanitizeLabel(k.namespace)

	deletePolicy := metav1.DeletePropagationForeground
	err := k.clientset.BatchV1().Jobs(k.k8sNamespace).DeleteCollection(
		ctx,
		metav1.DeleteOptions{
			PropagationPolicy: &deletePolicy,
		},
		metav1.ListOptions{
			LabelSelector: labelSelector,
		},
	)
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to delete jobs: %w", err)
	}

	// Delete all PVCs in the namespace with our label
	err = k.clientset.CoreV1().PersistentVolumeClaims(k.k8sNamespace).DeleteCollection(
		ctx,
		metav1.DeleteOptions{
			PropagationPolicy: &deletePolicy,
		},
		metav1.ListOptions{
			LabelSelector: labelSelector,
		},
	)
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to delete PVCs: %w", err)
	}

	return nil
}

func New(_ context.Context, cfg Config, logger *slog.Logger) (orchestra.Driver, error) {
	// Try to get in-cluster config first (for running inside k8s)
	config, err := rest.InClusterConfig()
	if err != nil {
		// Fall back to kubeconfig (for local development)
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()

		if cfg.Kubeconfig != "" {
			loadingRules.ExplicitPath = cfg.Kubeconfig
		}

		configOverrides := &clientcmd.ConfigOverrides{}
		kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)
		config, err = kubeConfig.ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("failed to get kubernetes config: %w", err)
		}
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	k8sNamespace := cfg.K8sNamespace
	if k8sNamespace == "" {
		k8sNamespace = "default"
	}

	logger.Info("k8s.config", "k8sNamespace", k8sNamespace, "orchestraNamespace", cfg.Namespace)

	return &K8s{
		clientset:    clientset,
		config:       config,
		logger:       logger,
		namespace:    cfg.Namespace,
		k8sNamespace: k8sNamespace,
	}, nil
}

func (k *K8s) Name() string {
	return "k8s"
}

// GetContainer attempts to find and return an existing container (job) by its name.
// Returns ErrContainerNotFound if the container does not exist.
func (k *K8s) GetContainer(ctx context.Context, containerID string) (orchestra.Container, error) {
	job, err := k.clientset.BatchV1().Jobs(k.k8sNamespace).Get(ctx, containerID, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil, orchestra.ErrContainerNotFound
		}
		return nil, fmt.Errorf("failed to get job: %w", err)
	}

	// Get pod name from job
	podName := ""
	pods, err := k.clientset.CoreV1().Pods(k.k8sNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: "job-name=" + job.Name,
	})
	if err == nil && len(pods.Items) > 0 {
		podName = pods.Items[0].Name
	}

	return &Container{
		clientset:    k.clientset,
		config:       k.config,
		jobName:      job.Name,
		podName:      podName,
		k8sNamespace: k.k8sNamespace,
		logger:       k.logger,
	}, nil
}

var (
	_ orchestra.Driver          = &K8s{}
	_ orchestra.Container       = &Container{}
	_ orchestra.ContainerStatus = &ContainerStatus{}
	_ orchestra.Volume          = &Volume{}
)
