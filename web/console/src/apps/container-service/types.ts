/** What every container service section needs: one target Cluster, and the
 *  Project scope its permissions are evaluated in. */
export type ClusterSectionProps = {
  clusterId: string;
  clusterName: string;
  /**
   * The Namespace that Cluster's Agent runs in, which decides which name the
   * protected-namespace permission applies to. It travels with `clusterId`
   * because it is fixed per Cluster and differs between them.
   */
  agentNamespace: string;
  tenantId: string | null;
  projectId: string | null;
};
