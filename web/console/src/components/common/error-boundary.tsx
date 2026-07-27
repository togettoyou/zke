import { Component, type ErrorInfo, type ReactNode } from "react";

import { Button } from "@/components/ui/button";

type Props = {
  children: ReactNode;
  /** Shown in the fallback so the operator knows which window failed. */
  label: string;
};

type State = { error: Error | null };

/**
 * Keeps a crashing application from taking down the whole desktop: only the
 * affected window shows the failure and can be reloaded in place.
 */
export class AppErrorBoundary extends Component<Props, State> {
  override state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  override componentDidCatch(error: Error, info: ErrorInfo): void {
    console.error(`应用 ${this.props.label} 渲染失败`, error, info.componentStack);
  }

  override render(): ReactNode {
    if (!this.state.error) {
      return this.props.children;
    }
    return (
      <div className="flex h-full flex-col items-center justify-center gap-3 p-6 text-center">
        <p className="text-foreground text-sm font-medium">{this.props.label} 渲染失败</p>
        <p className="text-muted-foreground max-w-md text-[13px]">
          界面出现未处理的错误，可重试加载该窗口；如果持续出现，请提供浏览器控制台信息。
        </p>
        <Button size="sm" variant="secondary" onClick={() => this.setState({ error: null })}>
          重新加载窗口
        </Button>
      </div>
    );
  }
}
