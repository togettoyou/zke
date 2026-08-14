import { CalendarClock } from "lucide-react";

import { Alert } from "@/components/ui/misc";
import type { AppComponentProps } from "@/apps/types";

/**
 * Placeholder window for applications that are on the roadmap but have no
 * backend yet. It states the phase and the planned capabilities, and shows no
 * data at all — nothing here may look like a working feature.
 */
export function PlannedApp({ manifest }: AppComponentProps) {
  const availability = manifest.availability;
  if (availability.state !== "planned") {
    return null;
  }

  return (
    <div className="mx-auto flex h-full max-w-xl flex-col justify-center gap-5 p-6">
      <div className="flex items-center gap-3.5">
        {/* The same unlit face the launcher gives a planned tile. A dashed
            outline is wireframe shorthand, and it would also say something
            different here than the icon the operator just clicked. */}
        <span className="bg-surface-muted/70 text-subtle-foreground grid size-12 shrink-0 place-items-center rounded-[15px]">
          <manifest.icon className="size-5" strokeWidth={1.75} aria-hidden />
        </span>
        <div className="min-w-0">
          <h3 className="text-foreground text-[15px] font-semibold">{manifest.title}</h3>
          <p className="text-muted-foreground mt-0.5 text-[13px]">{manifest.description}</p>
        </div>
      </div>

      <Alert tone="info" className="flex items-start gap-2">
        <CalendarClock className="mt-0.5 size-4 shrink-0" aria-hidden />
        <span>
          该能力属于 Roadmap Phase {availability.phase}
          ，当前版本尚未实现，界面不提供任何实际数据或操作。
        </span>
      </Alert>

      <section>
        <h4 className="text-subtle-foreground mb-1 text-[11px] font-medium">规划中的能力</h4>
        {/* An itemised list, not four filled boxes. Boxing each line gives every
            one of them the weight of a control, on a screen whose whole point is
            that there is nothing here to operate yet. */}
        <ul className="divide-border/60 divide-y">
          {availability.plannedCapabilities.map((capability) => (
            <li
              key={capability}
              className="text-muted-foreground flex items-center gap-2.5 py-2 text-[13px]"
            >
              <span aria-hidden className="bg-subtle-foreground/50 size-1 shrink-0 rounded-full" />
              {capability}
            </li>
          ))}
        </ul>
      </section>

      <p className="text-subtle-foreground text-xs">
        完整开发规划见仓库文档 <span className="zke-mono">docs/roadmap.md</span>
        。规划内容可能随产品设计与技术验证调整，不代表交付时间承诺。
      </p>
    </div>
  );
}
