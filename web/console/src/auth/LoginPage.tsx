import { useState, type FormEvent } from "react";
import { Boxes, Cpu, Loader2, Moon, ShieldCheck, Sun } from "lucide-react";

import { useLogin } from "@/api/queries/auth";
import { errorMessage, errorRequestId, isApiError } from "@/api/errors";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Alert } from "@/components/ui/misc";
import { useThemeStore } from "@/theme/theme-store";

/** Positioning statements, all drawn from the product's own documentation. */
const PILLARS = [
  { icon: Boxes, title: "多集群统一接入", detail: "每个集群部署 Agent，由 Agent 主动连接 Server" },
  { icon: Cpu, title: "算力与模型服务", detail: "全局查看 CPU/GPU 资源，工作负载仍在具体集群运行" },
  { icon: ShieldCheck, title: "受控的敏感操作", detail: "权限校验、影响展示、用户确认与完整审计" },
];

/**
 * Login is the only unauthenticated view, and the first impression of the
 * product. The left panel states what ZKE is — including that it is still in
 * early development, which must not be quietly dropped just because this is a
 * marketing-shaped surface. The right panel is the form and nothing else.
 *
 * On success the Server sets the session and CSRF cookies; nothing sensitive is
 * returned in the body, so the Console simply re-reads `/api/v1/auth/me`.
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
    <div className="from-desktop-from to-desktop-to relative min-h-full bg-linear-to-br">
      <DesktopGlow />

      <Button
        variant="ghost"
        size="icon"
        className="absolute top-4 right-4 z-10"
        onClick={toggleTheme}
        aria-label={theme === "dark" ? "切换到浅色主题" : "切换到深色主题"}
      >
        {theme === "dark" ? <Sun /> : <Moon />}
      </Button>

      <div className="relative mx-auto flex min-h-full max-w-5xl items-center justify-center px-6 py-10">
        <div className="rounded-window border-border bg-surface shadow-window grid w-full overflow-hidden border lg:grid-cols-[1.15fr_1fr]">
          {/* Brand panel. Hidden on narrow viewports: on a phone the form is the
              only thing worth the space. */}
          <section className="border-border bg-surface-muted relative hidden flex-col justify-between gap-10 border-r p-8 lg:flex">
            <div>
              <Brandmark />
              <h1 className="text-foreground mt-6 text-2xl leading-snug font-semibold tracking-tight">
                AI 原生 Kubernetes
                <br />
                管理与算力平台
              </h1>
              <p className="text-muted-foreground mt-3 max-w-xs text-[13px] leading-relaxed">
                全局观察，按集群执行；全局管理算力，按策略选择落点。
              </p>
            </div>

            <ul className="grid gap-4">
              {PILLARS.map((pillar) => (
                <li key={pillar.title} className="flex gap-3">
                  <span className="border-border bg-surface text-primary mt-0.5 grid size-8 shrink-0 place-items-center rounded-lg border">
                    <pillar.icon className="size-4" aria-hidden />
                  </span>
                  <span className="min-w-0">
                    <span className="text-foreground block text-[13px] font-medium">
                      {pillar.title}
                    </span>
                    <span className="text-subtle-foreground block text-xs leading-relaxed">
                      {pillar.detail}
                    </span>
                  </span>
                </li>
              ))}
            </ul>

            <p className="text-subtle-foreground border-border border-t pt-4 text-xs leading-relaxed">
              当前处于早期设计与开发阶段，部分模块尚未实现，不适用于生产环境。
            </p>
          </section>

          {/* Form panel. */}
          <section className="flex flex-col justify-center p-8">
            <div className="lg:hidden">
              <Brandmark />
            </div>

            <header className="mt-6 lg:mt-0">
              <h2 className="text-foreground text-lg font-semibold tracking-tight">登录</h2>
              <p className="text-muted-foreground mt-1 text-[13px]">
                使用平台账号登录 ZKE 控制台。
              </p>
            </header>

            <form onSubmit={handleSubmit} className="mt-6 grid gap-4">
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

              <Button
                type="submit"
                variant="primary"
                className="mt-1 h-10"
                disabled={login.isPending}
              >
                {login.isPending ? <Loader2 className="animate-spin" /> : null}
                {login.isPending ? "登录中…" : "登录"}
              </Button>
            </form>

            <p className="text-subtle-foreground mt-6 text-xs leading-relaxed">
              连续登录失败会触发账号锁定与来源限流；锁定到期后自动恢复，也可由全局管理员解锁。
            </p>
          </section>
        </div>
      </div>
    </div>
  );
}

function Brandmark() {
  return (
    <div className="flex items-center gap-2.5">
      <span className="bg-primary text-primary-foreground shadow-e2 grid size-9 place-items-center rounded-xl text-base font-bold">
        Z
      </span>
      <span className="min-w-0">
        <span className="text-foreground block text-sm leading-tight font-semibold">
          ZKE Console
        </span>
        <span className="text-subtle-foreground block text-xs leading-tight">
          Z Kubernetes Engine
        </span>
      </span>
    </div>
  );
}

/** The same layered glow the desktop uses, so the two surfaces feel continuous. */
function DesktopGlow() {
  return (
    <div
      aria-hidden
      className="pointer-events-none absolute inset-0"
      style={{
        backgroundImage:
          "radial-gradient(60rem 40rem at 15% 10%, var(--desktop-glow), transparent 60%), radial-gradient(50rem 30rem at 85% 85%, var(--desktop-glow), transparent 65%)",
      }}
    />
  );
}
