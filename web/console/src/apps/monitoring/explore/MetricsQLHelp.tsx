import { CircleQuestionMark } from "lucide-react";

import { Button } from "@/components/ui/button";
import { HintTooltip } from "@/components/ui/tooltip";

import { METRICSQL_DOCUMENTATION_URL } from "./metricsql";

/**
 * The way out to the query language's own documentation.
 *
 * Explore is the one screen in the Console that asks an operator to write
 * something in a language rather than choose from a list, so the reference has
 * to be one click away — and it has to be the right reference. The storage is
 * VictoriaMetrics and the language is MetricsQL, not PromQL; a link to the
 * Prometheus documentation would quietly describe a smaller language than the
 * box below accepts.
 */
export function MetricsQLHelpLink() {
  return (
    <HintTooltip label="查看 MetricsQL 语法文档（VictoriaMetrics）">
      <Button asChild size="icon-sm" variant="ghost">
        <a
          href={METRICSQL_DOCUMENTATION_URL}
          target="_blank"
          rel="noopener noreferrer"
          aria-label="查看 MetricsQL 语法文档"
        >
          <CircleQuestionMark />
        </a>
      </Button>
    </HintTooltip>
  );
}
