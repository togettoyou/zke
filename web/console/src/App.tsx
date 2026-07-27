import { Loader2 } from "lucide-react";

import { LoginPage } from "@/auth/LoginPage";
import { useSessionContext } from "@/auth/session-context";
import { Desktop } from "@/desktop/Desktop";

/**
 * Two states only: the login view, or the desktop. The Server owns the session,
 * so the Console simply renders whichever one `/api/v1/auth/me` implies.
 */
export function App() {
  const { status, refresh } = useSessionContext();

  if (status === "loading") {
    return (
      <div className="from-desktop-from to-desktop-to flex h-full items-center justify-center bg-linear-to-br">
        <span className="text-muted-foreground flex items-center gap-2 text-[13px]">
          <Loader2 className="size-4 animate-spin" />
          正在恢复会话…
        </span>
      </div>
    );
  }

  if (status === "unauthenticated") {
    return <LoginPage onAuthenticated={refresh} />;
  }

  return <Desktop />;
}
