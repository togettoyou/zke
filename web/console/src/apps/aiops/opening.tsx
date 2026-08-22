import { Activity, HeartPulse, Layers, Telescope } from "lucide-react";

const STARTERS = [
  {
    icon: Telescope,
    title: "了解集群现状",
    prompt: "先看一下这个集群整体状况，有哪些值得注意的异常？",
  },
  {
    icon: HeartPulse,
    title: "排查不健康的工作负载",
    prompt: "找出当前不就绪或反复重启的工作负载，逐个说明原因和证据。",
  },
  {
    icon: Layers,
    title: "检查某个 Namespace",
    prompt: "检查 default Namespace 里的资源状态、最近的 Event 和明显的配置问题。",
  },
  {
    icon: Activity,
    title: "看资源用量与瓶颈",
    prompt: "最近一小时哪些节点或工作负载的 CPU、内存压力最大？给出数据来源。",
  },
];

/**
 * What a conversation with nothing in it shows.
 *
 * One component for both of them, because they are the same thing: the
 * application opened onto no session, and a session nobody has asked anything
 * in yet, are both an empty conversation in a named Cluster. Two screens for
 * one state meant the operator saw the workspace change shape for a difference
 * — whether a row already exists in the rail — that is none of their business.
 *
 * It names the Cluster, because the one thing an operator has to be sure of
 * before they let anything read a production cluster is which cluster it is
 * going to read. Beyond that it says as little as it can: this screen is a
 * caret waiting in a box, and everything drawn around the box is something the
 * reader has to get past before they can type.
 *
 * A suggestion fills the composer rather than sending. It is a starting point
 * for a question, not the question: an operator who picks 检查某个 Namespace
 * almost always means a different Namespace than `default`, and one that sent
 * immediately gave them no moment to say so. The prompt it writes is therefore
 * not shown here — it appears in the box a moment later, which is where they
 * are about to read it anyway, and printing it twice made four suggestions into
 * eight lines of prose.
 */
export function Opening({
  clusterName,
  ready,
  disabled,
  onPick,
}: {
  clusterName: string;
  /** There is a Cluster to ask in. */
  ready: boolean;
  /** Asking is refused for now — an archived conversation. */
  disabled?: boolean;
  onPick: (prompt: string) => void;
}) {
  return (
    // `my-auto` rather than a centring parent: the two hosts nest it
    // differently — one centres a column, the other a row — and this way the
    // screen sits in the middle of both.
    <div className="mx-auto my-auto flex w-full max-w-xl flex-col items-center py-6">
      <h2 className="text-foreground text-center text-[22px] font-semibold">
        {ready ? (
          <>
            要在 <span className="text-primary">{clusterName}</span> 里查什么？
          </>
        ) : (
          "先选择一个在线的目标集群"
        )}
      </h2>
      <p className="text-muted-foreground mt-2 text-center text-[13px] leading-relaxed text-balance">
        {ready
          ? "AIOps 自行决定读取什么，每一步都写进可复核的轨迹。当前运行时只读。"
          : "AIOps 的每一次查证都必须指向一个具体的集群。请先让当前项目里的某个 Cluster 上线，或切换到别的项目。"}
      </p>
      {/* Hidden rather than greyed out when there is nothing to ask: four
          disabled suggestions are four things to read before finding out that
          none of them can be used.

          Chips rather than cards. Four bordered panels put four boxes on a
          screen whose only real control is the box underneath them, and the eye
          has to work out which box it is supposed to type in. A chip is the
          size of its own label, so the row of them reads as one line of options
          instead of a second layout competing with the composer. */}
      {ready && !disabled ? (
        <div className="mt-6 flex flex-wrap justify-center gap-2">
          {STARTERS.map((starter) => {
            const Icon = starter.icon;
            return (
              <button
                key={starter.title}
                type="button"
                onClick={() => onPick(starter.prompt)}
                className="zke-focus border-border text-muted-foreground hover:border-border-strong hover:bg-surface-muted hover:text-foreground inline-flex items-center gap-1.5 rounded-full border px-3 py-1.5 text-[13px] transition-colors duration-150"
              >
                <Icon aria-hidden className="size-3.5 shrink-0 opacity-70" />
                {starter.title}
              </button>
            );
          })}
        </div>
      ) : null}
    </div>
  );
}
