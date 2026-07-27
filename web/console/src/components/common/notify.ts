import { toast } from "sonner";

import { errorMessage, errorRequestId } from "@/api/errors";

/**
 * Reports a failed operation with the Server's own reason.
 *
 * A bare "操作失败" hides the one thing the operator needs — why it failed — and
 * drops the request id that ties the failure to its audit event and server log.
 * The action name stays as the headline so the toast is still readable at a
 * glance; the Server's message and the request id go underneath.
 */
export function notifyFailure(action: string, error: unknown): void {
  const requestId = errorRequestId(error);
  const reason = errorMessage(error);
  toast.error(action, {
    description: requestId ? `${reason} · 请求 ID：${requestId}` : reason,
  });
}
