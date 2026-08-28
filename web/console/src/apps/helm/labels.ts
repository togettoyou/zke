import type { HelmReleaseOperation } from "@/api/types";

/**
 * What the four release changes are called.
 *
 * Its own module rather than a constant beside the component that draws it: the
 * release list names an operation it is offering to reattach to, and the
 * operation view names the same one in its title. Two copies of this map is two
 * places for 回滚 to become 回退.
 */
export const HELM_ACTION_LABELS: Record<HelmReleaseOperation["action"], string> = {
  install: "安装",
  upgrade: "升级",
  rollback: "回滚",
  uninstall: "卸载",
};
