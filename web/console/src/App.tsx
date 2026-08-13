import { Loader2, RefreshCw } from "lucide-react";

import { useSetupStatus } from "@/api/queries/auth";
import { LoginPage } from "@/auth/LoginPage";
import { SetupPage } from "@/auth/SetupPage";
import { useSessionContext } from "@/auth/session-context";
import { Button } from "@/components/ui/button";
import { Desktop } from "@/desktop/Desktop";

/** The Server owns both first-run state and the authenticated session. */
export function App() {
  const { status, refresh } = useSessionContext();
  const setup = useSetupStatus();

  if (setup.isPending || status === "loading") {
    return (
      <div className="from-desktop-from to-desktop-to flex h-full items-center justify-center bg-linear-to-br">
        <span className="text-muted-foreground flex items-center gap-2 text-[13px]">
          <Loader2 className="size-4 animate-spin" />
          正在检查系统状态…
        </span>
      </div>
    );
  }

  if (setup.isError) {
    return (
      <div className="from-desktop-from to-desktop-to flex h-full items-center justify-center bg-linear-to-br px-6">
        <div className="text-center">
          <p className="text-foreground text-sm font-medium">无法读取系统初始化状态</p>
          <p className="text-muted-foreground mt-1 text-xs">请确认 Server 与数据库可用后重试。</p>
          <Button className="mt-5" onClick={() => void setup.refetch()}>
            <RefreshCw />
            重试
          </Button>
        </div>
      </div>
    );
  }

  if (setup.data?.required) {
    return <SetupPage />;
  }

  if (status === "unauthenticated") {
    return <LoginPage onAuthenticated={refresh} />;
  }

  return <Desktop />;
}
