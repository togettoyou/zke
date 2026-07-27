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
    <div className="mx-auto flex h-full max-w-2xl flex-col justify-center gap-4 p-6">
      <div className="flex items-center gap-3">
        <span className="border-border-strong bg-surface-muted text-subtle-foreground grid size-11 place-items-center rounded-2xl border border-dashed">
          <manifest.icon className="size-5" aria-hidden />
        </span>
        <div>
          <h3 className="text-foreground text-base font-semibold">{manifest.title}</h3>
          <p className="text-muted-foreground text-[13px]">{manifest.description}</p>
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
        <h4 className="text-foreground mb-2 text-[13px] font-medium">规划中的能力</h4>
        <ul className="grid gap-1.5">
          {availability.plannedCapabilities.map((capability) => (
            <li
              key={capability}
              className="border-border bg-surface-muted text-muted-foreground rounded-md border px-3 py-2 text-[13px]"
            >
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
