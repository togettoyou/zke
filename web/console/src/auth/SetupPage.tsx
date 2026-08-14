import { useRef, useState, type FormEvent } from "react";
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

/**
 * First run: the same view as sign-in, asking a different question.
 *
 * It deliberately carries no card of its own. Setup and login are the two halves
 * of one unauthenticated surface — an operator sees this screen once and the
 * next one forever — and floating the form on a raised, blurred panel here while
 * sign-in sat directly on the field made them read as two products. The form
 * rests on the same themed field, behind the same key light, drafting field and
 * grain, and is held by space rather than by a border.
 *
 * The topology is not repeated. Sign-in explains what ZKE is to somebody about
 * to use it; this page is a single administrative act with three fields, and a
 * drawing beside it would only make the act look longer than it is.
 */
export function SetupPage() {
  const initialize = useInitializeAdministrator();
  const theme = useThemeStore((state) => state.theme);
  const toggleTheme = useThemeStore((state) => state.toggleTheme);
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [confirmation, setConfirmation] = useState("");
  const [localFailure, setLocalFailure] = useState("");
  const passwordField = useRef<HTMLInputElement>(null);
  const confirmationField = useRef<HTMLInputElement>(null);

  const failure = localFailure || (initialize.error ? errorMessage(initialize.error) : "");
  const mismatched = localFailure !== "";

  /**
   * A rejected attempt describes what was typed, so it stops describing anything
   * the moment any field is edited.
   *
   * Guarded, exactly as sign-in guards it. `reset()` sets mutation state
   * unconditionally, so calling it from three `onChange` handlers dispatched a
   * React Query state update — and a re-render of the whole view — on every
   * keystroke of a username and two 15-character passwords, in the overwhelming
   * case where there was no failure to clear.
   */
  function clearFailure(): void {
    if (localFailure) {
      setLocalFailure("");
    }
    if (initialize.error) {
      initialize.reset();
    }
  }

  async function handleSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    const normalizedUsername = username.trim();
    if (!normalizedUsername || !password || !confirmation) {
      return;
    }
    if (password !== confirmation) {
      setLocalFailure("两次输入的密码不一致。");
      // The mismatch is the one failure the operator can fix without retyping
      // everything, so the caret is put where the fix goes. Both fields are
      // masked; a message alone leaves them comparing two rows of dots.
      confirmationField.current?.focus();
      confirmationField.current?.select();
      return;
    }
    try {
      await initialize.mutateAsync({ username: normalizedUsername, password });
    } catch {
      setPassword("");
      setConfirmation("");
      // A refused attempt clears both passwords, so the form is back to where
      // the next one starts and the caret should be there too.
      passwordField.current?.focus();
    }
  }

  return (
    <div className="from-desktop-from to-desktop-to relative h-full overflow-y-auto bg-linear-to-br">
      <div aria-hidden className="zke-auth-surface pointer-events-none absolute inset-0" />
      <div aria-hidden className="zke-auth-dots pointer-events-none absolute inset-0" />
      <div aria-hidden className="zke-grain pointer-events-none absolute inset-0" />

      <main className="relative z-10 flex min-h-full w-full items-center px-6 py-12">
        {/* The same measure the sign-in form is set to, so the two views hold
            their text in a column of one width. */}
        <section className="zke-rise mx-auto w-full max-w-[336px]">
          <header className="flex items-center gap-3">
            <ZkeMark className="size-10 rounded-[13px]" />
            <span className="min-w-0">
              <span className="text-foreground block text-sm leading-tight font-semibold">
                ZKE Console
              </span>
              <span className="text-subtle-foreground block text-[11px] leading-tight tracking-wide">
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
                className="bg-surface h-10 px-3"
              />
            </div>

            <div className="grid gap-2">
              <Label htmlFor="setup-password" className="text-muted-foreground text-xs">
                密码
              </Label>
              <Input
                ref={passwordField}
                id="setup-password"
                name="password"
                type="password"
                autoComplete="new-password"
                required
                minLength={15}
                aria-invalid={mismatched || undefined}
                value={password}
                onChange={(event) => {
                  setPassword(event.target.value);
                  clearFailure();
                }}
                className="bg-surface h-10 px-3"
              />
              {/*
               * The one rule the person typing has to satisfy, stated the way
               * every other password field in the Console states it.
               *
               * The Server also refuses a password over 1024 *bytes*, and this
               * was the only screen in the product that said so. Two different
               * units in one sentence is the smaller problem; the larger one is
               * that the ceiling is a bound on what gets fed to Argon2, not
               * advice — it is around 340 Chinese characters, and nobody
               * choosing a password is anywhere near it. It stays documented in
               * `docs/architecture/technical-foundation.md`, and the Server's
               * refusal says so plainly if anyone ever manages to hit it.
               */}
              <p className="text-subtle-foreground text-[11px]">至少 15 个字符。</p>
            </div>

            <div className="grid gap-2">
              <Label
                htmlFor="setup-password-confirmation"
                className="text-muted-foreground text-xs"
              >
                确认密码
              </Label>
              <Input
                ref={confirmationField}
                id="setup-password-confirmation"
                name="password-confirmation"
                type="password"
                autoComplete="new-password"
                required
                minLength={15}
                aria-invalid={mismatched || undefined}
                value={confirmation}
                onChange={(event) => {
                  setConfirmation(event.target.value);
                  clearFailure();
                }}
                className="bg-surface h-10 px-3"
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
