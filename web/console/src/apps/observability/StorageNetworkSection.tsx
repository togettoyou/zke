import { ViewedPanels } from "./MetricsViews";
import { STORAGE_VIEWS } from "./metrics-catalog";

/**
 * Disk and network, which nothing else in the pipeline reports: the kubelet's
 * resource endpoint covers CPU and memory and stops there. Every view here
 * needs the node metrics exporter, and a Cluster that refused it says so in the
 * empty panel rather than showing blank axes.
 */
export function StorageNetworkSection() {
  return <ViewedPanels views={STORAGE_VIEWS} label="存储与网络视角" />;
}
