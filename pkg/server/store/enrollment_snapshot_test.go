package store_test

import "github.com/togettoyou/zke/pkg/server/store"

func testEnrollmentSnapshot() store.EnrollmentConfigurationSnapshot {
	return store.EnrollmentConfigurationSnapshot{
		EndpointProfileID:       "00000000-0000-0000-0000-000000000010",
		EndpointProfileRevision: 1,
		RegistrationURL:         "http://127.0.0.1:8080",
		QUICAddress:             "127.0.0.1:8443",
		AgentWorkload: store.WorkloadSettings{
			Image: "ghcr.io/togettoyou/zke-agent:test", ImagePullPolicy: "IfNotPresent",
			CPURequest: "50m", MemoryLimit: "512Mi",
		},
		AgentNamespace: "zke-system",
	}
}
