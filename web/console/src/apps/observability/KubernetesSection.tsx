import { ViewedPanels } from "./MetricsViews";
import { KUBERNETES_VIEWS } from "./metrics-catalog";

/**
 * What the Cluster is doing with the objects it was given.
 *
 * The container service already answers all of this for "now". Only a series
 * can say when a Pod started failing, whether a Node has been flapping, or
 * whether a workload has been short of replicas since the last rollout — and
 * those are the questions somebody asks after the fact, when the live view has
 * already gone green again.
 */
export function KubernetesSection({ initialQuery }: { initialQuery?: string }) {
  return (
    <ViewedPanels
      views={KUBERNETES_VIEWS}
      label="Kubernetes 资源视角"
      initialQuery={initialQuery}
    />
  );
}
