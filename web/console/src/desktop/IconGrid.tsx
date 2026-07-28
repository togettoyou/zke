import { APP_MANIFESTS } from "@/apps/registry";
import type { AppManifest } from "@/apps/types";
import { useSessionContext } from "@/auth/session-context";
import { cn } from "@/lib/cn";

/**
 * Desktop launcher.
 *
 * Applications the user has no permission for anywhere are hidden. Planned ones
 * stay visible so the product shape is legible, and each says so on its own
 * face — the marker belongs to the tile, not to a section heading above a block
 * of them. A desktop does not sort its icons under administrative captions, and
 * per-tile marking states the case more directly anyway: it survives however the
 * grid reflows, and it is impossible to read one icon without reading its own
 * status.
 *
 * The difference between "you can use this" and "this is where it will go" has
 * to be legible before the window opens, so a planned tile is drawn without the
 * lit face and the raised edge that make an available one look pressable.
 */
export function IconGrid({ onOpen }: { onOpen: (appId: string) => void }) {
  const { permissions } = useSessionContext();

  const visible = APP_MANIFESTS.filter((manifest) => {
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
      className="grid grid-cols-[repeat(auto-fill,minmax(104px,1fr))] gap-x-1 gap-y-3 p-6"
      aria-label="平台应用"
    >
      {ordered.map((manifest) => (
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
      className="zke-focus group rounded-panel flex w-full flex-col items-center gap-2 px-1.5 py-2.5 text-center"
    >
      <span
        className={cn(
          "grid size-14 place-items-center rounded-[17px] transition-[transform,box-shadow] duration-200",
          "group-hover:-translate-y-1 group-active:translate-y-0",
          planned
            ? "bg-surface-muted/70 text-subtle-foreground"
            : "zke-tile bg-primary-surface text-primary group-hover:shadow-e3",
        )}
      >
        <Icon className="size-6" strokeWidth={1.75} aria-hidden />
      </span>

      <span className="flex w-full min-w-0 flex-col items-center gap-0.5">
        <span
          className={cn(
            "w-full truncate text-[12.5px] leading-tight font-medium",
            planned ? "text-muted-foreground" : "text-foreground",
          )}
        >
          {manifest.title}
        </span>
        {planned ? (
          <span className="text-subtle-foreground w-full truncate text-[10px] leading-4">
            规划中 · Phase {availability.phase}
          </span>
        ) : null}
      </span>
    </button>
  );
}
