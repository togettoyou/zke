-- The three workloads an install of metrics collection puts into a Cluster,
-- and how much of that Cluster each may take.
--
-- They belong with the Agent and Cluster Terminal images rather than in the
-- Server's configuration file: they are the same kind of decision, and they
-- have to be changeable without restarting the Server, because collection is
-- enabled per Cluster long after the Server started.
--
-- Rows rather than columns, so adding them is this file and nothing else — see
-- the platform_workload_settings comment in the foundation migration.
--
-- The collector scrapes the kubelet and the other two. kube-state-metrics is
-- what turns usage into something interpretable — it is the only source of a
-- Node's allocatable capacity and of a Pod's requests and limits, so without it
-- a usage curve has no denominator. The node metrics exporter adds disk,
-- filesystem and network, which nothing else in the pipeline reports.
--
-- Every image names a real published image and pins its version. A collector is
-- installed into a Cluster the operator runs; an unpinned tag would let two
-- Clusters install different builds and report metrics that do not compare.
--
-- The node exporter's budget is deliberately the smallest: it runs on every
-- Node, so its cost is multiplied by the size of the Cluster.
INSERT INTO platform_workload_settings (
    component,
    image,
    image_pull_policy,
    cpu_request,
    memory_request,
    cpu_limit,
    memory_limit
) VALUES (
    'collector',
    'victoriametrics/vmagent:v1.149.0',
    'IfNotPresent',
    '50m',
    '128Mi',
    '500m',
    '512Mi'
), (
    'kube-state-metrics',
    'registry.k8s.io/kube-state-metrics/kube-state-metrics:v2.19.1',
    'IfNotPresent',
    '20m',
    '128Mi',
    '500m',
    '512Mi'
), (
    'node-exporter',
    'quay.io/prometheus/node-exporter:v1.12.1',
    'IfNotPresent',
    '10m',
    '32Mi',
    '200m',
    '128Mi'
);
