import { Trash2 } from "lucide-react";

import { Button } from "@/components/ui/button";
import { HintTooltip } from "@/components/ui/tooltip";

type DeleteActionProps = {
  /** The object being deleted. It names the control for screen readers. */
  name: string;
  /**
   * Why the control is there, or — when `disabled` — why it is not usable.
   * A disabled button with no explanation reads as a broken one.
   */
  hint?: string;
  disabled?: boolean;
  onDelete: () => void;
};

/**
 * Deleting an object, in a table row.
 *
 * Tinted rather than filled: a row of solid red buttons would make deletion the
 * loudest thing in a list whose job is reading, and the confirmation dialog —
 * DryRun first, then the typed name — is where the operation is actually
 * guarded. The tint is only there so the destructive control is not the same
 * colour as the ones next to it.
 *
 * It exists as one component because it appears in every section, and a colour
 * that has to be remembered in eleven places is a colour that will be missing
 * from some of them.
 */
export function RowDeleteAction({ name, hint, disabled, onDelete }: DeleteActionProps) {
  const button = (
    <Button
      size="icon-sm"
      variant="ghost"
      className="text-danger hover:text-danger"
      aria-label={`删除 ${name}`}
      disabled={disabled}
      onClick={onDelete}
    >
      <Trash2 />
    </Button>
  );
  // Radix needs an enabled element to hang a tooltip on: a disabled button
  // fires no pointer events, so the span carries them instead.
  return hint ? (
    <HintTooltip label={hint}>
      <span>{button}</span>
    </HintTooltip>
  ) : (
    button
  );
}

/**
 * The same action in a detail page's header, where there is room for the word.
 *
 * Last in the header for the same reason it is last in a row: the actions
 * before it are ones an operator can take back, and this one is not.
 */
export function DetailDeleteAction({ name, hint, disabled, onDelete }: DeleteActionProps) {
  const button = (
    <Button
      size="sm"
      variant="secondary"
      className="text-danger"
      aria-label={`删除 ${name}`}
      disabled={disabled}
      onClick={onDelete}
    >
      <Trash2 />
      删除
    </Button>
  );
  return hint ? (
    <HintTooltip label={hint}>
      <span>{button}</span>
    </HintTooltip>
  ) : (
    button
  );
}
