import { useState, type FormEvent } from "react";
import { Loader2, Moon, Sun } from "lucide-react";

import { useInitializeAdministrator } from "@/api/queries/auth";
import { errorMessage, errorRequestId } from "@/api/errors";
import { ZkeMark } from "@/components/brand/zke-mark";
import { OpenSourceLink } from "@/components/common/open-source-link";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Alert } from "@/components/ui/misc";
import { useThemeStore } from "@/theme/theme-store";

export function SetupPage() {
  const initialize = useInitializeAdministrator();
  const theme = useThemeStore((state) => state.theme);
  const toggleTheme = useThemeStore((state) => state.toggleTheme);
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [confirmation, setConfirmation] = useState("");
  const [localFailure, setLocalFailure] = useState("");

  const failure = localFailure || (initialize.error ? errorMessage(initialize.error) : "");

  function clearFailure(): void {
    setLocalFailure("");
    initialize.reset();
  }

  async function handleSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    const normalizedUsername = username.trim();
    if (!normalizedUsername || !password || !confirmation) {
      return;
    }
    if (password !== confirmation) {
      setLocalFailure("两次输入的密码不一致。");
      return;
    }
    try {
      await initialize.mutateAsync({ username: normalizedUsername, password });
    } catch {
      setPassword("");
      setConfirmation("");
    }
  }

  return (
    <div className="from-desktop-from to-desktop-to relative h-full overflow-y-auto bg-linear-to-br">
      <div aria-hidden className="zke-auth-surface pointer-events-none absolute inset-0" />
      <div aria-hidden className="zke-auth-dots pointer-events-none absolute inset-0" />
      <div aria-hidden className="zke-grain pointer-events-none absolute inset-0" />

      <main className="relative z-10 mx-auto flex min-h-full w-full max-w-[560px] items-center px-6 py-12">
        <section className="zke-rise bg-surface/90 border-border w-full rounded-2xl border p-7 shadow-2xl backdrop-blur-sm sm:p-10">
          <header className="flex items-center gap-3">
            <ZkeMark className="size-11 rounded-[13px]" />
            <span>
              <span className="text-foreground block text-sm font-semibold">ZKE Console</span>
              <span className="text-subtle-foreground block text-[11.5px] tracking-wide">
                首次初始化
              </span>
            </span>
          </header>

          <div className="mt-9">
            <h1 className="text-foreground text-[22px] leading-tight font-semibold">
              创建全局管理员
            </h1>
            <p className="text-muted-foreground mt-2 text-[13px] leading-relaxed">
              当前系统尚未配置全局管理员。请设置登录用户名和密码，创建后将自动进入控制台。
            </p>
          </div>

          <form onSubmit={handleSubmit} className="mt-8 grid gap-4">
            <div className="grid gap-2">
              <Label htmlFor="setup-username" className="text-muted-foreground text-xs">
                用户名
              </Label>
              <Input
                id="setup-username"
                name="username"
                autoComplete="username"
                autoFocus
                required
                maxLength={128}
                value={username}
                onChange={(event) => {
                  setUsername(event.target.value);
                  clearFailure();
                }}
                className="bg-background h-10 px-3"
              />
            </div>

            <div className="grid gap-2">
              <Label htmlFor="setup-password" className="text-muted-foreground text-xs">
                密码
              </Label>
              <Input
                id="setup-password"
                name="password"
                type="password"
                autoComplete="new-password"
                required
                minLength={15}
                value={password}
                onChange={(event) => {
                  setPassword(event.target.value);
                  clearFailure();
                }}
                className="bg-background h-10 px-3"
              />
              <p className="text-subtle-foreground text-[11.5px]">
                至少 15 个字符，最长 1024 字节。
              </p>
            </div>

            <div className="grid gap-2">
              <Label
                htmlFor="setup-password-confirmation"
                className="text-muted-foreground text-xs"
              >
                确认密码
              </Label>
              <Input
                id="setup-password-confirmation"
                name="password-confirmation"
                type="password"
                autoComplete="new-password"
                required
                minLength={15}
                value={confirmation}
                onChange={(event) => {
                  setConfirmation(event.target.value);
                  clearFailure();
                }}
                className="bg-background h-10 px-3"
              />
            </div>

            <p aria-live="polite" aria-atomic="true" className="sr-only">
              {failure}
            </p>
            {failure ? (
              <Alert tone="danger" role="presentation">
                {failure}
                {initialize.error && errorRequestId(initialize.error) ? (
                  <span className="zke-mono mt-1 block text-xs opacity-80">
                    请求 ID：{errorRequestId(initialize.error)}
                  </span>
                ) : null}
              </Alert>
            ) : null}

            <Button
              type="submit"
              variant="primary"
              className="mt-2 h-10"
              disabled={initialize.isPending}
            >
              {initialize.isPending ? <Loader2 className="animate-spin" /> : null}
              {initialize.isPending ? "正在创建…" : "创建并进入控制台"}
            </Button>
          </form>
        </section>
      </main>

      <div className="absolute top-5 right-5 z-20 flex items-center gap-1">
        <OpenSourceLink size="icon" className="text-subtle-foreground" />
        <Button
          variant="ghost"
          size="icon"
          className="text-subtle-foreground"
          onClick={toggleTheme}
          aria-label={theme === "dark" ? "切换到浅色主题" : "切换到深色主题"}
        >
          {theme === "dark" ? <Sun /> : <Moon />}
        </Button>
      </div>
    </div>
  );
}
