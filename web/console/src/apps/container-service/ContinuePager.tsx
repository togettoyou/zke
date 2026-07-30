import { ChevronLeft, ChevronRight } from "lucide-react";

import { Button } from "@/components/ui/button";

/** Page controls for {@link useContinuePagination}: forward while the list hands
 *  back a token, backward through the tokens already visited. */
export function ContinuePager({
  pageIndex,
  nextToken,
  onPrevious,
  onNext,
}: {
  pageIndex: number;
  nextToken: string;
  onPrevious: () => void;
  onNext: (nextToken: string) => void;
}) {
  return (
    <div className="ml-auto flex items-center gap-1.5">
      <span className="zke-mono text-subtle-foreground mr-1 text-xs">第 {pageIndex + 1} 页</span>
      <Button
        size="icon-sm"
        variant="ghost"
        aria-label="上一页"
        disabled={pageIndex === 0}
        onClick={onPrevious}
      >
        <ChevronLeft />
      </Button>
      <Button
        size="icon-sm"
        variant="ghost"
        aria-label="下一页"
        disabled={!nextToken}
        onClick={() => onNext(nextToken)}
      >
        <ChevronRight />
      </Button>
    </div>
  );
}
