package agent

import (
	"errors"
	"fmt"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func loadKubernetesConfig(kubeconfigFile string) (*rest.Config, error) {
	if kubeconfigFile != "" {
		config, err := clientcmd.BuildConfigFromFlags("", kubeconfigFile)
		if err != nil {
			return nil, fmt.Errorf("load explicit kubeconfig: %w", err)
		}
		return config, nil
	}

	config, err := rest.InClusterConfig()
	if err == nil {
		return config, nil
	}
	if !errors.Is(err, rest.ErrNotInCluster) {
		return nil, fmt.Errorf("load in-cluster Kubernetes configuration: %w", err)
	}

	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	deferred := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules,
		&clientcmd.ConfigOverrides{},
	)
	config, err = deferred.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load default kubeconfig: %w", err)
	}
	return config, nil
}
