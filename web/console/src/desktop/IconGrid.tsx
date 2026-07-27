import { APP_MANIFESTS } from "@/apps/registry";
import type { AppManifest } from "@/apps/types";
import { useSessionContext } from "@/auth/session-context";
import { cn } from "@/lib/cn";

/**
 * Desktop launcher.
 *
 * Applications the user has no permission for anywhere are hidden; planned
 * applications stay visible with a "规划中" marker so the product shape is
 * clear without implying the capability exists.
 */
export function IconGrid({ onOpen }: { onOpen: (appId: string) => void }) {
  const { permissions } = useSessionContext();

  const visible = APP_MANIFESTS.filter((manifest) => {
    if (manifest.availability.state === "planned" || manifest.requiredPermissions.length === 0) {
      return true;
    }
    return manifest.requiredPermissions.some((permission) => permissions.canAnywhere(permission));
  });

  return (
    <ul
      className="grid w-full grid-cols-[repeat(auto-fill,minmax(104px,1fr))] gap-1 p-4"
      aria-label="平台应用"
    >
      {visible.map((manifest) => (
        <li key={manifest.id}>
          <AppIcon manifest={manifest} onOpen={() => onOpen(manifest.id)} />
        </li>
      ))}
    </ul>
  );
}

function AppIcon({ manifest, onOpen }: { manifest: AppManifest; onOpen: () => void }) {
  const Icon = manifest.icon;
  const availability = manifest.availability;
  const planned = availability.state === "planned";

  return (
    <button
      type="button"
      onClick={onOpen}
      title={manifest.description}
      className={cn(
        "group flex w-full flex-col items-center gap-2 rounded-xl px-2 py-3 text-center transition-colors",
        "hover:bg-surface-overlay focus-visible:bg-surface-overlay",
      )}
    >
      <span
        className={cn(
          "relative flex size-14 items-center justify-center rounded-2xl border shadow-sm transition-transform group-hover:-translate-y-0.5",
          planned
            ? "border-border-strong bg-surface-muted text-subtle-foreground border-dashed"
            : "border-border bg-surface text-primary",
        )}
      >
        <Icon className="size-6" aria-hidden />
      </span>
      <span className="flex flex-col items-center gap-0.5">
        <span className="text-foreground text-[13px] leading-tight font-medium">
          {manifest.title}
        </span>
        {availability.state === "planned" ? (
          <span className="bg-info-surface text-info rounded-full px-1.5 text-[10px] leading-4">
            Phase {availability.phase} 规划中
          </span>
        ) : null}
      </span>
    </button>
  );
}
