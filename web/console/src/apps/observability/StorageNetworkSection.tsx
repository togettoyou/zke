import { ViewedPanels } from "./MetricsViews";
import { STORAGE_VIEWS } from "./metrics-catalog";

/**
 * Disk and network, which the kubelet's resource endpoint does not report: it
 * covers CPU and memory and stops there.
 *
 * Most views here need the node metrics exporter, and a Cluster that refused it
 * says so in the empty panel rather than showing blank axes. 持久卷 is the
 * exception — a PersistentVolumeClaim is only measured by the kubelet that
 * mounted it, so that view answers on the base install alone.
 */
export function StorageNetworkSection() {
  return <ViewedPanels views={STORAGE_VIEWS} label="存储与网络视角" />;
}
