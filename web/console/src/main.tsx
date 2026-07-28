import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Toaster } from "sonner";

import { isApiError } from "@/api/errors";
import { App } from "@/App";
import { SessionProvider } from "@/auth/SessionProvider";
import { TooltipProvider } from "@/components/ui/tooltip";

import "@/styles/theme.css";

// Caps a `Retry-After` the Server may state in minutes: a query that waits that
// long is a window stuck on a spinner with nothing to show for it.
const MAX_RETRY_DELAY_MS = 30_000;

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
          <Toaster position="bottom-right" richColors closeButton />
        </TooltipProvider>
      </SessionProvider>
    </QueryClientProvider>
  </StrictMode>,
);
