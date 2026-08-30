import { CircleQuestionMark } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { HintTooltip } from "@/components/ui/tooltip";
import {
  SCRAPE_ANNOTATION_GUIDE,
  SCRAPE_ANNOTATION_PREFIX,
  scrapeAnnotationName,
} from "@/lib/scrape-annotations";

/**
 * How a Cluster's own workloads get collected.
 *
 * The three installed components answer questions about the Cluster itself and
 * never touch a business endpoint, so an operator on this screen has no way to
 * guess that their application is one annotation away from being collected —
 * and this is not the screen that writes it, because the object it goes on
 * lives in 容器服务. A popover is the honest shape for that: a reference, read
 * once and then acted on somewhere else.
 */
export function ScrapeAnnotationHelp() {
  return (
    <Popover>
      <HintTooltip label="如何采集自定义应用的指标">
        <PopoverTrigger asChild>
          <Button size="icon-sm" variant="ghost" aria-label="如何采集自定义应用的指标">
            <CircleQuestionMark />
          </Button>
        </PopoverTrigger>
      </HintTooltip>
      <PopoverContent align="end" className="w-96">
        <div className="grid gap-2.5">
          <div className="grid gap-1">
            <h4 className="text-foreground text-xs font-semibold">采集自定义应用的指标</h4>
            <p className="text-muted-foreground text-xs leading-relaxed">
              内置的 kubelet、kube-state-metrics 与 node-exporter
              只描述集群自身。要采集自己工作负载暴露的指标， 给它的 Service 或 Endpoint
              加下面这组注解即可，
              <strong className="text-foreground font-medium">不需要重新安装采集组件</strong>。
            </p>
          </div>

          <div className="border-border/70 rounded-control overflow-hidden border">
            <table className="w-full text-[11px]">
              <thead className="bg-surface-muted/50 text-subtle-foreground">
                <tr>
                  <th className="px-2 py-1 text-left font-medium">
                    注解（前缀 <span className="zke-mono">{SCRAPE_ANNOTATION_PREFIX}</span>）
                  </th>
                  <th className="px-2 py-1 text-left font-medium">取值</th>
                </tr>
              </thead>
              <tbody>
                {SCRAPE_ANNOTATION_GUIDE.map((entry) => (
                  <tr key={entry.key} className="border-border/60 border-t align-top">
                    <td className="px-2 py-1">
                      <div className="zke-mono text-foreground break-all">
                        {scrapeAnnotationName(entry.key)}
                      </div>
                      <div className="text-subtle-foreground">{entry.purpose}</div>
                    </td>
                    <td className="text-muted-foreground px-2 py-1">{entry.values}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          <ul className="text-muted-foreground grid list-disc gap-1 pl-4 text-[11px] leading-relaxed">
            <li>
              在「容器服务 → 服务与路由」编辑 Service 或 Endpoint
              时，注解表单里可以一键填入这组注解。
            </li>
            <li>
              同名 Service 与 Endpoint 上的同一个注解以 Endpoint 为准，但那只对没有 selector 的
              Service 生效；有 selector 的一律注解 Service 本身。只有
              <strong className="text-foreground font-medium">就绪</strong>端点会被抓取。
            </li>
            <li>
              写错的值会直接丢弃这个目标，而不是回退到默认值：路径必须以{" "}
              <span className="zke-mono">/</span> 开头，端口必须在 1-65535 之间。
            </li>
            <li>
              <span className="zke-mono">auth=service-account</span> 只允许配合
              https，携带的是采集组件自己的 Token；注解不支持引用 Secret。
            </li>
            <li>
              点进本页任一集群，可以看到它当前生效的采集 Job 与就绪目标。采集组件读的是
              EndpointSlice，所以写在 Endpoint 上的注解会以 EndpointSlice 的身份出现在那份清单里。
            </li>
            <li>每个接入的端点都会消耗该集群的摄取预算，高基数标签会让整个集群被限流。</li>
          </ul>
        </div>
      </PopoverContent>
    </Popover>
  );
}
