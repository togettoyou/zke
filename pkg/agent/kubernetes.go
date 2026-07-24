package agent

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	enrollmentSecretName = "zke-agent-enrollment"
	enrollmentTokenKey   = "token"
	trustSecretName      = "zke-agent-trust"
	listenerCAKey        = "agent-listener-ca.crt"
	registrationCAKey    = "registration-ca.crt"
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

func loadEnrollmentToken(
	ctx context.Context,
	client kubernetes.Interface,
	namespace string,
) (string, error) {
	secret, err := client.CoreV1().Secrets(namespace).Get(
		ctx,
		enrollmentSecretName,
		metav1.GetOptions{},
	)
	if err != nil {
		return "", fmt.Errorf("read Agent enrollment Secret: %w", err)
	}
	token, ok := secret.Data[enrollmentTokenKey]
	if !ok {
		return "", errors.New(
			"Agent enrollment Secret does not contain the token key",
		)
	}
	return validateEnrollmentToken(token)
}

func loadTrust(
	ctx context.Context,
	client kubernetes.Interface,
	cfg *Config,
) error {
	if cfg.Connection.CACertificateFile != "" &&
		cfg.Registration.CACertificateFile != "" {
		return nil
	}
	secret, err := client.CoreV1().Secrets(cfg.IdentityNamespace).Get(
		ctx,
		trustSecretName,
		metav1.GetOptions{},
	)
	if apierrors.IsNotFound(err) && cfg.Connection.CACertificateFile != "" {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read Agent trust Secret: %w", err)
	}
	if cfg.Connection.CACertificateFile == "" {
		listenerCA := secret.Data[listenerCAKey]
		if len(listenerCA) == 0 {
			return errors.New(
				"Agent trust Secret does not contain the Agent Listener CA",
			)
		}
		cfg.Connection.CACertificatePEM = append([]byte(nil), listenerCA...)
	}
	if cfg.Registration.CACertificateFile == "" {
		cfg.Registration.CACertificatePEM = append(
			[]byte(nil),
			secret.Data[registrationCAKey]...,
		)
	}
	return nil
}
