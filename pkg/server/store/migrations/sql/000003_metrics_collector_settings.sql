-- What the metrics collector is, and how much of a Cluster it may take.
--
-- Both belong with the Agent and Cluster Terminal images rather than in the
-- Server's configuration file: they are the same kind of decision, and they
-- have to be changeable without restarting the Server, because collection is
-- enabled per Cluster long after the Server started.
--
-- The image default names a real published image and pins its version. A
-- collector is installed into a Cluster the operator runs; an unpinned tag
-- would let two Clusters install different builds and report metrics that do
-- not compare.
--
-- An empty quantity means "do not set this entry on the container": Kubernetes
-- has no other spelling for "no limit", and a deployment managing the budget
-- with a LimitRange needs to be able to say so.
ALTER TABLE platform_settings
    ADD COLUMN metrics_collector_image text NOT NULL
        DEFAULT 'victoriametrics/vmagent:v1.149.0',
    ADD COLUMN metrics_collector_image_pull_policy text NOT NULL
        DEFAULT 'IfNotPresent',
    ADD COLUMN metrics_collector_cpu_request text NOT NULL DEFAULT '50m',
    ADD COLUMN metrics_collector_memory_request text NOT NULL DEFAULT '128Mi',
    ADD COLUMN metrics_collector_cpu_limit text NOT NULL DEFAULT '500m',
    ADD COLUMN metrics_collector_memory_limit text NOT NULL DEFAULT '512Mi';

ALTER TABLE platform_settings
    ADD CONSTRAINT platform_settings_metrics_collector_image_format CHECK (
        metrics_collector_image = btrim(metrics_collector_image)
        AND octet_length(metrics_collector_image) BETWEEN 1 AND 512
    ),
    ADD CONSTRAINT platform_settings_metrics_collector_pull_policy CHECK (
        metrics_collector_image_pull_policy IN ('Always', 'IfNotPresent', 'Never')
    ),
    ADD CONSTRAINT platform_settings_metrics_collector_quantities CHECK (
        metrics_collector_cpu_request = btrim(metrics_collector_cpu_request)
        AND metrics_collector_memory_request = btrim(metrics_collector_memory_request)
        AND metrics_collector_cpu_limit = btrim(metrics_collector_cpu_limit)
        AND metrics_collector_memory_limit = btrim(metrics_collector_memory_limit)
        AND octet_length(metrics_collector_cpu_request) <= 32
        AND octet_length(metrics_collector_memory_request) <= 32
        AND octet_length(metrics_collector_cpu_limit) <= 32
        AND octet_length(metrics_collector_memory_limit) <= 32
    );
