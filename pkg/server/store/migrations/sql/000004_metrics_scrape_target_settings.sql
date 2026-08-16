-- The two additional scrape targets the collector reads, and how much of a
-- Cluster each may take.
--
-- They sit beside the collector's own settings for the same reason those are
-- here rather than in the Server's configuration file: an operator changes an
-- image or a budget long after the Server started, and a restart to apply it
-- would be a restart of the whole platform.
--
-- kube-state-metrics is what turns usage into something interpretable — it is
-- the only source of a Node's allocatable capacity and of a Pod's requests and
-- limits, so without it a usage curve has no denominator. The node metrics
-- exporter adds disk, filesystem and network, which nothing else in the
-- pipeline reports.
--
-- Both defaults name a real published image and pin its version, for the same
-- reason the collector's does: an unpinned tag lets two Clusters install
-- different builds and report metrics that do not compare.
--
-- An empty quantity means "do not set this entry on the container". The node
-- exporter's budget is deliberately small: it runs on every Node, so its cost
-- is multiplied by the size of the Cluster.
ALTER TABLE platform_settings
    ADD COLUMN kube_state_metrics_image text NOT NULL
        DEFAULT 'registry.k8s.io/kube-state-metrics/kube-state-metrics:v2.19.1',
    ADD COLUMN kube_state_metrics_image_pull_policy text NOT NULL
        DEFAULT 'IfNotPresent',
    ADD COLUMN kube_state_metrics_cpu_request text NOT NULL DEFAULT '20m',
    ADD COLUMN kube_state_metrics_memory_request text NOT NULL DEFAULT '128Mi',
    ADD COLUMN kube_state_metrics_cpu_limit text NOT NULL DEFAULT '500m',
    ADD COLUMN kube_state_metrics_memory_limit text NOT NULL DEFAULT '512Mi',
    ADD COLUMN node_exporter_image text NOT NULL
        DEFAULT 'quay.io/prometheus/node-exporter:v1.12.1',
    ADD COLUMN node_exporter_image_pull_policy text NOT NULL
        DEFAULT 'IfNotPresent',
    ADD COLUMN node_exporter_cpu_request text NOT NULL DEFAULT '10m',
    ADD COLUMN node_exporter_memory_request text NOT NULL DEFAULT '32Mi',
    ADD COLUMN node_exporter_cpu_limit text NOT NULL DEFAULT '200m',
    ADD COLUMN node_exporter_memory_limit text NOT NULL DEFAULT '128Mi';

ALTER TABLE platform_settings
    ADD CONSTRAINT platform_settings_kube_state_metrics_image_format CHECK (
        kube_state_metrics_image = btrim(kube_state_metrics_image)
        AND octet_length(kube_state_metrics_image) BETWEEN 1 AND 512
    ),
    ADD CONSTRAINT platform_settings_kube_state_metrics_pull_policy CHECK (
        kube_state_metrics_image_pull_policy IN ('Always', 'IfNotPresent', 'Never')
    ),
    ADD CONSTRAINT platform_settings_kube_state_metrics_quantities CHECK (
        kube_state_metrics_cpu_request = btrim(kube_state_metrics_cpu_request)
        AND kube_state_metrics_memory_request = btrim(kube_state_metrics_memory_request)
        AND kube_state_metrics_cpu_limit = btrim(kube_state_metrics_cpu_limit)
        AND kube_state_metrics_memory_limit = btrim(kube_state_metrics_memory_limit)
        AND octet_length(kube_state_metrics_cpu_request) <= 32
        AND octet_length(kube_state_metrics_memory_request) <= 32
        AND octet_length(kube_state_metrics_cpu_limit) <= 32
        AND octet_length(kube_state_metrics_memory_limit) <= 32
    ),
    ADD CONSTRAINT platform_settings_node_exporter_image_format CHECK (
        node_exporter_image = btrim(node_exporter_image)
        AND octet_length(node_exporter_image) BETWEEN 1 AND 512
    ),
    ADD CONSTRAINT platform_settings_node_exporter_pull_policy CHECK (
        node_exporter_image_pull_policy IN ('Always', 'IfNotPresent', 'Never')
    ),
    ADD CONSTRAINT platform_settings_node_exporter_quantities CHECK (
        node_exporter_cpu_request = btrim(node_exporter_cpu_request)
        AND node_exporter_memory_request = btrim(node_exporter_memory_request)
        AND node_exporter_cpu_limit = btrim(node_exporter_cpu_limit)
        AND node_exporter_memory_limit = btrim(node_exporter_memory_limit)
        AND octet_length(node_exporter_cpu_request) <= 32
        AND octet_length(node_exporter_memory_request) <= 32
        AND octet_length(node_exporter_cpu_limit) <= 32
        AND octet_length(node_exporter_memory_limit) <= 32
    );
