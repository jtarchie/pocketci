package k8s

import (
	"context"
	"log/slog"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// IsAvailable checks if a Kubernetes cluster is available and reachable.
// This is a lightweight check that only verifies API server connectivity.
func IsAvailable() bool {
	// Try to create a client
	client, err := New(context.Background(), Config{Namespace: "availability-check"}, slog.Default())
	if err != nil {
		return false
	}
	defer func() { _ = client.Close() }()

	// Simple API call to verify connectivity - just check if we can list namespaces
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	k8sClient, ok := client.(*K8s)
	if !ok {
		return false
	}

	_, err = k8sClient.clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{Limit: 1})
	return err == nil
}
