import { useState } from "react";

import { newIdempotencyKey } from "@/api/client";

/**
 * An idempotency key that stays fixed for as long as one submission is open.
 *
 * The Server tells a retry apart from a new request by this key alone: creation
 * endpoints record it and answer a repeat with the original result instead of
 * creating a second Tenant, Project or Cluster enrollment. That only works if
 * the key survives the retry.
 *
 * It does not survive a click. Every one of these forms stays open when a submit
 * fails, precisely so the operator can try again — and the failure worth
 * protecting against is the one where the response was lost after the Server
 * committed. Minting a key inside the click handler labelled that retry a new
 * request and duplicated the resource, which is the exact outcome the Server's
 * idempotency table was built to prevent.
 *
 * So the key is minted when the form opens and held until it closes. Reopening
 * is the operator starting over, and gets a new one.
 */
export function useSubmissionKey(open: boolean): string {
  const [key, setKey] = useState(newIdempotencyKey);
  // Reset during render rather than in an effect, matching how these dialogs
  // already clear their fields: a stale key must never be readable, not even
  // for the frame between opening and an effect running.
  const [wasOpen, setWasOpen] = useState(open);
  if (wasOpen !== open) {
    setWasOpen(open);
    if (open) {
      setKey(newIdempotencyKey());
    }
  }
  return key;
}
