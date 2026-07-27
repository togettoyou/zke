import { APP_MANIFESTS } from "@/apps/registry";
import type { AppManifest } from "@/apps/types";
import { useSessionContext } from "@/auth/session-context";
import { cn } from "@/lib/cn";

/**
 * Desktop launcher.
 *
 * Applications the user has no permission for anywhere are hidden; planned
 * applications stay visible with a "规划中" marker so the product shape is clear
 * without implying the capability exists. Planned tiles are deliberately drawn
 * flat and dashed — the difference between "you can use this" and "this is where
 * it will go" has to be legible before the window opens, not after.
 */
export function IconGrid({ onOpen }: { onOpen: (appId: string) => void }) {
  const { permissions } = useSessionContext();

  const visible = APP_MANIFESTS.filter((manifest) => {
    if (manifest.availability.state === "planned" || manifest.requiredPermissions.length === 0) {
      return true;
    }
    return manifest.requiredPermissions.some((permission) => permissions.canAnywhere(permission));
  });

  const available = visible.filter((manifest) => manifest.availability.state === "available");
  const planned = visible.filter((manifest) => manifest.availability.state === "planned");

  return (
    <div className="grid gap-7 p-6">
      <IconSection label="可用" manifests={available} onOpen={onOpen} />
      {planned.length > 0 ? (
        <IconSection label="规划中" manifests={planned} onOpen={onOpen} />
      ) : null}
    </div>
  );
}

function IconSection({
  label,
  manifests,
  onOpen,
}: {
  label: string;
  manifests: AppManifest[];
  onOpen: (appId: string) => void;
}) {
  if (manifests.length === 0) {
    return null;
  }
  return (
    <section>
      <h2 className="text-subtle-foreground mb-2 px-1 text-[11px] font-semibold tracking-[0.14em] uppercase">
        {label}
      </h2>
      <ul
        className="grid grid-cols-[repeat(auto-fill,minmax(96px,1fr))] gap-1"
        aria-label={`平台应用 · ${label}`}
      >
        {manifests.map((manifest) => (
          <li key={manifest.id}>
            <AppIcon manifest={manifest} onOpen={() => onOpen(manifest.id)} />
          </li>
        ))}
      </ul>
    </section>
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
      className="zke-focus group rounded-panel hover:bg-surface-overlay flex w-full flex-col items-center gap-2 border border-transparent px-1.5 py-2.5 text-center transition-colors"
    >
      <span
        className={cn(
          "relative grid size-14 place-items-center rounded-[16px] transition-[transform,box-shadow] duration-200",
          "group-hover:-translate-y-1 group-active:translate-y-0",
          planned
            ? "border-border-strong bg-surface-muted/70 text-subtle-foreground border border-dashed"
            : "zke-tile border-border bg-surface text-primary group-hover:shadow-e3 border",
        )}
      >
        <Icon className="size-6" strokeWidth={1.75} aria-hidden />
      </span>

      <span className="flex min-w-0 flex-col items-center gap-1">
        <span className="text-foreground w-full truncate text-[13px] leading-tight font-medium">
          {manifest.title}
        </span>
        {planned ? (
          <span className="text-subtle-foreground text-[10px] leading-4">
            Phase {availability.phase}
          </span>
        ) : null}
      </span>
    </button>
  );
}
