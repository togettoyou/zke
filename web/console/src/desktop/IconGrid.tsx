import { memo } from "react";

import { useAITools } from "@/api/queries/aiops";
import { appFaceClass, appHoverClass } from "@/apps/accent";
import { APP_MANIFESTS } from "@/apps/registry";
import type { AppManifest } from "@/apps/types";
import { useSessionContext } from "@/auth/session-context";
import { cn } from "@/lib/cn";

/**
 * Desktop launcher.
 *
 * Applications the user has no permission for anywhere are hidden, and so is
 * the one application the deployment itself can switch off: AIOps without a
 * model endpoint has nothing behind its icon, and an icon that opens onto "not
 * configured" is worse than no icon. It stays hidden while the answer is still
 * loading — appearing a moment late is a smaller lie than appearing and then
 * being taken away.
 *
 * Planned ones stay visible so the product shape is legible, and each says so on its own
 * face — the marker belongs to the tile, not to a section heading above a block
 * of them. A desktop does not sort its icons under administrative captions, and
 * per-tile marking states the case more directly anyway: it survives however the
 * grid reflows, and it is impossible to read one icon without reading its own
 * status.
 *
 * The difference between "you can use this" and "this is where it will go" has
 * to be legible before the window opens, so a planned tile is drawn without the
 * lit face and the raised edge that make an available one look pressable.
 *
 * Memoized for the same reason the windows are: the Cluster event stream lands
 * on the Desktop above, and the launcher has nothing to do with it.
 */
export const IconGrid = memo(function IconGrid({ onOpen }: { onOpen: (appId: string) => void }) {
  const { permissions } = useSessionContext();
  const aiRuntime = useAITools({ enabled: permissions.canAnywhere("ai.run") });

  const visible = APP_MANIFESTS.filter((manifest) => {
    if (manifest.requiresGlobalAdmin && !permissions.isGlobalAdmin) {
      return false;
    }
    if (manifest.requiresPlatformFeature === "aiops" && !aiRuntime.data?.enabled) {
      return false;
    }
    if (manifest.availability.state === "planned" || manifest.requiredPermissions.length === 0) {
      return true;
    }
    return manifest.requiredPermissions.some((permission) => permissions.canAnywhere(permission));
  });

  // Available first, then planned: what can be used should be reachable without
  // reading past what cannot.
  const ordered = [
    ...visible.filter((manifest) => manifest.availability.state === "available"),
    ...visible.filter((manifest) => manifest.availability.state === "planned"),
  ];

  return (
    <ul
      className="grid grid-cols-[repeat(auto-fill,minmax(124px,1fr))] gap-x-2 gap-y-6 p-6"
      aria-label="平台应用"
    >
      {ordered.map((manifest) => (
        <li key={manifest.id}>
          <AppIcon manifest={manifest} onOpen={() => onOpen(manifest.id)} />
        </li>
      ))}
    </ul>
  );
});

function AppIcon({ manifest, onOpen }: { manifest: AppManifest; onOpen: () => void }) {
  const Icon = manifest.icon;
  const planned = manifest.availability.state === "planned";

  return (
    <button
      type="button"
      onClick={onOpen}
      title={manifest.description}
      className="zke-focus group rounded-panel flex w-full flex-col items-center gap-2 px-1.5 py-2.5 text-center"
    >
      <span
        className={cn(
          "grid size-16 place-items-center rounded-[20px]",
          appFaceClass(manifest),
          appHoverClass(manifest),
          !planned && "zke-tile",
        )}
      >
        <Icon className="size-7" strokeWidth={1.75} aria-hidden />
      </span>

      <span className="flex w-full min-w-0 flex-col items-center gap-0.5">
        <span
          className={cn(
            "w-full truncate text-[13px] leading-tight font-medium",
            planned ? "text-muted-foreground" : "text-foreground",
          )}
        >
          {manifest.title}
        </span>
        {/* The phase belongs in the window, not under every unlit tile: ten of
            these captions is a wall of small grey type, and the unlit face has
            already said the thing that matters. */}
        {planned ? (
          <span className="text-subtle-foreground text-[11px] leading-4">规划中</span>
        ) : null}
      </span>
    </button>
  );
}
