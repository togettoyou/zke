/**
 * Where the query language is documented.
 *
 * The storage behind ZKE metrics is VictoriaMetrics, and what it answers is
 * MetricsQL — a superset of PromQL. The distinction matters to whoever is
 * typing: `1.5h`, `[5i]`, `{a="1" or b="2"}`, `keep_metric_names` and `WITH`
 * templates all work here and none of them are PromQL, so pointing an operator
 * at the Prometheus documentation would be pointing them at a smaller language
 * than the one in front of them.
 */
export const METRICSQL_DOCUMENTATION_URL =
  "https://docs.victoriametrics.com/victoriametrics/metricsql/";

/**
 * The scope label the Server writes into every selector.
 *
 * Named here only so the Console can explain what it does. The Console never
 * sends it: an expression carrying one has it replaced, which is the whole
 * reason an operator may type anything they like into the box.
 */
export const CLUSTER_SCOPE_LABEL = "zke_cluster_id";

/** How many expressions may be run together, matching the Server's ceiling. */
export const MAX_EXPRESSIONS = 5;

/** The reference letters rows are addressed by, in the order they are handed out. */
export const EXPRESSION_REFS = ["A", "B", "C", "D", "E"] as const;
