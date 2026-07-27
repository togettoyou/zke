import { useState, type FormEvent } from "react";
import { Loader2, Moon, Sun } from "lucide-react";

import { useLogin } from "@/api/queries/auth";
import { errorMessage, errorRequestId, isApiError } from "@/api/errors";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Alert } from "@/components/ui/misc";
import { useThemeStore } from "@/theme/theme-store";

/**
 * Login is the only unauthenticated view. On success the Server sets the
 * session and CSRF cookies; nothing sensitive is returned in the body, so the
 * Console simply re-reads `/api/v1/auth/me`.
 */
export function LoginPage({ onAuthenticated }: { onAuthenticated: () => void }) {
  const login = useLogin();
  const theme = useThemeStore((state) => state.theme);
  const toggleTheme = useThemeStore((state) => state.toggleTheme);
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");

  const error = login.error;
  const retryAfter = isApiError(error) ? error.retryAfterSeconds : null;

  async function handleSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    if (!username.trim() || !password) {
      return;
    }
    try {
      await login.mutateAsync({ username: username.trim(), password });
      setPassword("");
      onAuthenticated();
    } catch {
      // Rendered from `login.error`; nothing is logged, the password is kept
      // out of any diagnostic path.
      setPassword("");
    }
  }

  return (
    <div className="from-desktop-from to-desktop-to relative flex min-h-full items-center justify-center bg-linear-to-br p-6">
      <Button
        variant="ghost"
        size="icon"
        className="absolute top-4 right-4"
        onClick={toggleTheme}
        aria-label={theme === "dark" ? "切换到浅色主题" : "切换到深色主题"}
      >
        {theme === "dark" ? <Sun /> : <Moon />}
      </Button>

      <div className="rounded-window border-border bg-surface shadow-window w-full max-w-92 border p-6">
        <header className="mb-6">
          <p className="text-subtle-foreground text-xs tracking-[0.2em] uppercase">ZKE Console</p>
          <h1 className="text-foreground mt-1 text-xl font-semibold">登录 ZKE 平台</h1>
          <p className="text-muted-foreground mt-1 text-[13px]">
            AI 原生 Kubernetes 管理与算力平台
          </p>
        </header>

        <form onSubmit={handleSubmit} className="grid gap-4">
          <div className="grid gap-1.5">
            <Label htmlFor="login-username">用户名</Label>
            <Input
              id="login-username"
              name="username"
              autoComplete="username"
              autoFocus
              required
              value={username}
              onChange={(event) => setUsername(event.target.value)}
            />
          </div>

          <div className="grid gap-1.5">
            <Label htmlFor="login-password">密码</Label>
            <Input
              id="login-password"
              name="password"
              type="password"
              autoComplete="current-password"
              required
              value={password}
              onChange={(event) => setPassword(event.target.value)}
            />
          </div>

          {error ? (
            <Alert tone="danger">
              {errorMessage(error)}
              {retryAfter ? `（请在 ${retryAfter} 秒后重试）` : ""}
              {errorRequestId(error) ? (
                <span className="zke-mono mt-1 block text-xs opacity-80">
                  请求 ID：{errorRequestId(error)}
                </span>
              ) : null}
            </Alert>
          ) : null}

          <Button type="submit" variant="primary" disabled={login.isPending}>
            {login.isPending ? <Loader2 className="animate-spin" /> : null}
            {login.isPending ? "登录中…" : "登录"}
          </Button>
        </form>

        <p className="text-subtle-foreground mt-5 text-xs">
          连续登录失败会触发账号锁定与来源限流；锁定到期后自动恢复，也可由全局管理员解锁。
        </p>
      </div>
    </div>
  );
}
