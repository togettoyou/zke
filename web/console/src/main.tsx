import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Toaster } from "sonner";

import { isApiError } from "@/api/errors";
import { App } from "@/App";
import { SessionProvider } from "@/auth/SessionProvider";
import { TooltipProvider } from "@/components/ui/tooltip";

import "@/styles/theme.css";

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
