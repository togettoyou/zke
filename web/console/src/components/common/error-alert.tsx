import { errorMessage, errorRequestId } from "@/api/errors";
import { Alert } from "@/components/ui/misc";

/**
 * A rejected request, reported in place.
 *
 * The Console's toast surface deliberately sits below dialogs, because a report
 * must never cover a surface that is asking a question — which means a failure
 * raised from inside a dialog cannot be a toast at all: it lands behind the
 * overlay, blurred and unreadable, while the form that caused it waits in front
 * of it. Anything submitted from a dialog or from a form's own save button
 * reports here instead, next to the control that was pressed.
 *
 * The request id is part of the report rather than a detail: it is the only
 * handle an operator has for the matching audit event and Server log.
 */
export function ErrorAlert({ error, className }: { error: unknown; className?: string }) {
  if (!error) {
    return null;
  }
  const requestId = errorRequestId(error);
  return (
    <Alert tone="danger" className={className}>
      {errorMessage(error)}
      {requestId ? (
        <span className="zke-mono mt-1 block text-xs opacity-80">请求 ID：{requestId}</span>
      ) : null}
    </Alert>
  );
}
