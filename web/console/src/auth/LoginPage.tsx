import { useState, type FormEvent } from "react";
import { Loader2, Moon, Sun } from "lucide-react";

import { useLogin } from "@/api/queries/auth";
import { errorMessage, errorRequestId, isApiError } from "@/api/errors";
import { OpenSourceLink } from "@/components/common/open-source-link";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Alert } from "@/components/ui/misc";
import { useThemeStore } from "@/theme/theme-store";

/**
 * Login is the only unauthenticated view, and the first impression of the
 * product.
 *
 * One surface, one drawing, one form. Not a card, and not a two-tone split
 * either: a half held permanently dark would contradict a product that ships a
 * theme switch, so both columns sit on the same themed field and are separated
 * by space and a single fading hairline.
 *
 * What fills the left is the real topology — clusters dialling out to the
 * Server — drawn as a constellation rather than a flowchart. Boxes and labelled
 * rows are the language of a wireframe; points, links and one horizon line are
 * the language of a mark. The accent colour appears exactly three times on this
 * view, and nothing but the light field is soft.
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
  const failure = error
    ? `${errorMessage(error)}${retryAfter ? `（请在 ${retryAfter} 秒后重试）` : ""}`
    : "";

  /**
   * A rejected attempt describes the state of what was typed, so it stops
   * describing anything the moment either field is edited. Left standing, a red
   * panel above the button says the corrected credentials are wrong too.
   */
  const { reset } = login;
  function clearFailure(): void {
    if (error) {
      reset();
    }
  }

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
    <div className="from-desktop-from to-desktop-to relative h-full overflow-hidden bg-linear-to-br">
      {/* Light, then the drafting field, then grain over both. */}
      <div aria-hidden className="zke-auth-surface pointer-events-none absolute inset-0" />
      <div aria-hidden className="zke-auth-dots pointer-events-none absolute inset-0" />
      <div aria-hidden className="zke-grain pointer-events-none absolute inset-0" />

      {/*
       * Sign-in is one screen, not a document: at any ordinary viewport the
       * composition is exactly the viewport and there is no scroll travel at
       * all. `auto` rather than `hidden` is the safety valve — on a very short
       * window (a landscape phone, or heavy browser zoom) the form has to stay
       * reachable, and clipping it would lock the operator out of the product.
       */}
      <div className="relative z-10 h-full overflow-y-auto">
        <div className="mx-auto grid min-h-full w-full max-w-[1440px] lg:grid-cols-[1.15fr_1fr]">
          {/* Dropped entirely on narrow viewports: on a phone the form is the
              only thing worth the height. */}
          <section className="zke-rise hidden flex-col px-14 py-14 lg:flex xl:px-20">
            <Brandmark />

            {/*
             * Takes the rest of the column and centres the drawing in it, so
             * the mark stays pinned to the corner without being positioned
             * absolutely against the section's own padding.
             *
             * `pb-10` is the height of the brandmark row. Centring in what is
             * left below the mark would put the drawing half that height lower
             * than the form opposite it, which centres in the whole column;
             * padding the bottom by the same amount cancels the offset, and the
             * two optical centres land on one line.
             */}
            <div className="flex flex-1 items-center pb-10">
              <div className="mx-auto w-full max-w-[460px]">
                <Topology />
                <p className="text-muted-foreground mt-10 text-center text-[13px] tracking-wide">
                  全局观察，按集群执行
                </p>
              </div>
            </div>
          </section>

          <section className="relative flex flex-col justify-center px-6 py-12 sm:px-12 lg:px-16 xl:px-24">
            {/* The column rule. A hairline faded out at both ends reads as a
                seam in one surface; a full-height border reads as the edge of a
                box, and a change of colour reads as two surfaces. */}
            <div
              aria-hidden
              className="via-border absolute inset-y-28 left-0 hidden w-px bg-linear-to-b from-transparent to-transparent lg:block"
            />

            <div
              className="zke-rise mx-auto w-full max-w-[336px]"
              style={{ animationDelay: "80ms" }}
            >
              <div className="lg:hidden">
                <Brandmark />
                <div className="mt-8 w-full max-w-[260px]">
                  <TopologyCompact />
                </div>
              </div>

              <header className="mt-9 lg:mt-0">
                <h1 className="text-foreground text-[22px] leading-tight font-semibold">
                  登录 ZKE 控制台
                </h1>
                <p className="text-muted-foreground mt-1.5 text-[13px]">使用平台账号继续。</p>
              </header>

              <form onSubmit={handleSubmit} className="mt-8 grid gap-4">
                <div className="grid gap-2">
                  <Label htmlFor="login-username" className="text-muted-foreground text-xs">
                    用户名
                  </Label>
                  <Input
                    id="login-username"
                    name="username"
                    autoComplete="username"
                    autoFocus
                    required
                    value={username}
                    onChange={(event) => {
                      setUsername(event.target.value);
                      clearFailure();
                    }}
                    className="bg-surface h-10 px-3"
                  />
                </div>

                <div className="grid gap-2">
                  <Label htmlFor="login-password" className="text-muted-foreground text-xs">
                    密码
                  </Label>
                  <Input
                    id="login-password"
                    name="password"
                    type="password"
                    autoComplete="current-password"
                    required
                    value={password}
                    onChange={(event) => {
                      setPassword(event.target.value);
                      clearFailure();
                    }}
                    className="bg-surface h-10 px-3"
                  />
                </div>

                {/*
                 * Always mounted, and absolutely positioned by `sr-only` so it
                 * claims no row in the grid. A live region only announces when
                 * it was already in the accessibility tree before its contents
                 * changed — one rendered alongside the failure it describes is
                 * an insertion, and may be read out by nothing at all. The
                 * visible panel below is therefore presentational: it says the
                 * same thing, and two regions would say it twice.
                 */}
                <p aria-live="polite" aria-atomic="true" className="sr-only">
                  {failure}
                </p>

                {error ? (
                  <Alert tone="danger" role="presentation">
                    {failure}
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
                  className="mt-2 h-10"
                  disabled={login.isPending}
                >
                  {login.isPending ? <Loader2 className="animate-spin" /> : null}
                  {login.isPending ? "登录中…" : "登录"}
                </Button>
              </form>

              <p className="text-subtle-foreground mt-7 text-[11.5px] leading-relaxed">
                连续登录失败会触发账号锁定与来源限流；锁定到期后自动恢复，也可由全局管理员解锁。
              </p>
            </div>
          </section>
        </div>
      </div>

      {/* Last in the document, positioned back to the corner. It is a preference
          control, not a step in signing in, so it belongs after the form in tab
          order — otherwise shift-tabbing off the username field lands on it. */}
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

/**
 * The three clusters sit on one shallow arc — a horizon for the fleet — and each
 * reaches up to the single Server. Agent positions are the curve midpoints, so
 * every dot sits on its own link.
 */
const HUB = { x: 180, y: 52 };

const NODES = [
  {
    label: "Cluster A",
    cluster: { x: 94, y: 198 },
    // One of the three Agents is named. They are identical marks on identical
    // links, so labelling every one would only add ink.
    agent: { x: 137, y: 133, named: true },
    link: "M94 198C94 140 180 128 180 65",
  },
  {
    label: "Cluster B",
    cluster: { x: 180, y: 195 },
    agent: { x: 180, y: 130 },
    link: "M180 195L180 65",
  },
  {
    label: "Cluster C",
    cluster: { x: 266, y: 198 },
    agent: { x: 223, y: 133 },
    link: "M266 198C266 140 180 128 180 65",
  },
];

function Topology() {
  return (
    <svg
      viewBox="0 0 360 244"
      fill="none"
      role="img"
      aria-label="一个 ZKE Server 位于顶端，三个 Kubernetes 集群各部署一个 Agent，由 Agent 主动向上连接 Server"
      className="w-full"
    >
      <defs>
        {/* The horizon has no ends: it is a fragment of something wider, so the
            stroke itself fades out rather than stopping at a point. */}
        <linearGradient id="zke-horizon" x1="0" y1="0" x2="1" y2="0">
          <stop offset="0%" stopColor="var(--auth-line)" stopOpacity="0" />
          <stop offset="24%" stopColor="var(--auth-line)" stopOpacity="1" />
          <stop offset="76%" stopColor="var(--auth-line)" stopOpacity="1" />
          <stop offset="100%" stopColor="var(--auth-line)" stopOpacity="0" />
        </linearGradient>
      </defs>

      <path d="M8 208Q180 182 352 208" stroke="url(#zke-horizon)" strokeWidth="1" />

      {NODES.map((node) => (
        <path key={node.link} d={node.link} stroke="var(--auth-line)" strokeWidth="1" />
      ))}

      {NODES.map((node, index) => (
        <path
          key={`flow-${node.link}`}
          d={node.link}
          pathLength={100}
          stroke="var(--primary)"
          strokeWidth="1.5"
          strokeLinecap="round"
          strokeDasharray="8 92"
          strokeDashoffset={8}
          className="zke-auth-flow"
          style={{ animationDelay: `${index * 1.5}s` }}
        />
      ))}

      {/* The Server: two concentric rings and a solid centre. Weight and
          repetition make it the hub — not a blur behind it, which on a dark
          field can only ever render as fog. */}
      <circle cx={HUB.x} cy={HUB.y} r="21" stroke="var(--auth-line-soft)" strokeWidth="1" />
      <circle
        cx={HUB.x}
        cy={HUB.y}
        r="13"
        fill="var(--auth-node)"
        stroke="var(--auth-line-strong)"
        strokeWidth="1"
      />
      <circle cx={HUB.x} cy={HUB.y} r="4.5" fill="var(--primary)" />
      <text
        x={HUB.x}
        y="16"
        textAnchor="middle"
        dominantBaseline="middle"
        fontSize="11"
        fill="var(--muted-foreground)"
      >
        ZKE Server
      </text>

      {NODES.map((node) => (
        <g key={`agent-${node.label}`}>
          <circle
            cx={node.agent.x}
            cy={node.agent.y}
            r="4.5"
            fill="var(--auth-node)"
            stroke="var(--auth-line-strong)"
            strokeWidth="1"
          />
          {"named" in node.agent ? (
            <text
              x={node.agent.x - 13}
              y={node.agent.y}
              textAnchor="end"
              dominantBaseline="middle"
              fontSize="10"
              fill="var(--subtle-foreground)"
            >
              Agent
            </text>
          ) : null}
        </g>
      ))}

      {NODES.map((node) => (
        <g key={node.label}>
          <circle
            cx={node.cluster.x}
            cy={node.cluster.y}
            r="6"
            fill="var(--auth-node)"
            stroke="var(--auth-line-strong)"
            strokeWidth="1"
          />
          <text
            x={node.cluster.x}
            y="226"
            textAnchor="middle"
            dominantBaseline="middle"
            fontSize="10"
            fill="var(--subtle-foreground)"
          >
            {node.label}
          </text>
        </g>
      ))}
    </svg>
  );
}

/**
 * The narrow-viewport motif.
 *
 * The full drawing cannot simply be scaled down to fit a phone: its labels are
 * sized for a 360-unit canvas and would land near six pixels. So the same idea
 * is restated at a scale that works — one link laid flat, the three parts named
 * once each. The product still says what it is on a phone, and the form keeps
 * the height it needs.
 */
function TopologyCompact() {
  return (
    <svg
      viewBox="0 0 220 76"
      fill="none"
      role="img"
      aria-label="集群中的 Agent 主动连接 ZKE Server"
      className="w-full"
    >
      <path d="M26 32H186" stroke="var(--auth-line)" strokeWidth="1" />
      <path
        d="M26 32H186"
        pathLength={100}
        stroke="var(--primary)"
        strokeWidth="1.5"
        strokeLinecap="round"
        strokeDasharray="8 92"
        strokeDashoffset={8}
        className="zke-auth-flow"
      />

      <circle
        cx="26"
        cy="32"
        r="6"
        fill="var(--auth-node)"
        stroke="var(--auth-line-strong)"
        strokeWidth="1"
      />
      <circle
        cx="110"
        cy="32"
        r="4.5"
        fill="var(--auth-node)"
        stroke="var(--auth-line-strong)"
        strokeWidth="1"
      />

      <circle cx="186" cy="32" r="17" stroke="var(--auth-line-soft)" strokeWidth="1" />
      <circle
        cx="186"
        cy="32"
        r="10"
        fill="var(--auth-node)"
        stroke="var(--auth-line-strong)"
        strokeWidth="1"
      />
      <circle cx="186" cy="32" r="3.5" fill="var(--primary)" />

      {[
        { x: 26, label: "Cluster" },
        { x: 110, label: "Agent" },
        { x: 186, label: "ZKE Server" },
      ].map((item) => (
        <text
          key={item.label}
          x={item.x}
          y="62"
          textAnchor="middle"
          dominantBaseline="middle"
          fontSize="9"
          fill="var(--subtle-foreground)"
        >
          {item.label}
        </text>
      ))}
    </svg>
  );
}

function Brandmark() {
  return (
    <div className="flex items-center gap-3">
      <span className="bg-primary text-primary-foreground grid size-10 place-items-center rounded-[13px] text-base font-semibold">
        Z
      </span>
      <span className="min-w-0">
        <span className="text-foreground block text-sm leading-tight font-semibold">
          ZKE Console
        </span>
        <span className="text-subtle-foreground block text-[11.5px] leading-tight tracking-wide">
          Z Kubernetes Engine
        </span>
      </span>
    </div>
  );
}
