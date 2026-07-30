/** What every container service section needs: one target Cluster, and the
 *  Project scope its permissions are evaluated in. */
export type ClusterSectionProps = {
  clusterId: string;
  clusterName: string;
  tenantId: string | null;
  projectId: string | null;
};
