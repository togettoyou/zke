import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

import { isApiError } from "@/api/errors";
import { App } from "@/App";
import { SessionProvider } from "@/auth/SessionProvider";
import { AppToaster } from "@/components/common/toaster";
import { TooltipProvider } from "@/components/ui/tooltip";

import "@/styles/theme.css";

// Caps a `Retry-After` the Server may state in minutes: a query that waits that
// long is a window stuck on a spinner with nothing to show for it.
const MAX_RETRY_DELAY_MS = 30_000;

/**
 * Server codes that answer with a 5xx but will answer the same way for as long
 * as the deployment is configured the way it is. They are states, not failures,
 * and the view that explains one must not wait for a retry budget first.
 */
const TERMINAL_ERROR_CODES = new Set(["metrics_disabled"]);

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 15_000,
      refetchOnWindowFocus: false,
      retry: (failureCount, error) => {
        // Authentication, authorization and validation failures are terminal;
        // only transient transport errors are worth retrying.
        if (isApiError(error) && error.status < 500 && error.status !== 429) {
          return false;
        }
        // A 5xx that is a deployment decision rather than a transient failure.
        // Retrying it spends two more round trips to be told the same thing,
        // and the view that explains it only appears after the last one — which
        // is what made opening 监控 on a Server without metrics storage sit
        // on a spinner through three 503s.
        if (isApiError(error) && TERMINAL_ERROR_CODES.has(error.code)) {
          return false;
        }
        return failureCount < 2;
      },
      // A rate-limited response carries the Server's own answer to "when";
      // retrying on the default backoff just spends another rejection to be
      // told the same thing. Anything else falls back to exponential backoff.
      retryDelay: (attemptIndex, error) => {
        const retryAfterSeconds = isApiError(error) ? error.retryAfterSeconds : null;
        if (retryAfterSeconds !== null && retryAfterSeconds > 0) {
          return Math.min(retryAfterSeconds * 1_000, MAX_RETRY_DELAY_MS);
        }
        return Math.min(1_000 * 2 ** attemptIndex, MAX_RETRY_DELAY_MS);
      },
    },
    mutations: { retry: false },
  },
});

const container = document.getElementById("root");
if (!container) {
  throw new Error("root container missing");
}

createRoot(container).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <SessionProvider>
        <TooltipProvider delayDuration={300}>
          <App />
          <AppToaster />
        </TooltipProvider>
      </SessionProvider>
    </QueryClientProvider>
  </StrictMode>,
);
