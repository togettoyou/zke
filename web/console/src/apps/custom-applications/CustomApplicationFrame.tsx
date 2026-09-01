import { useState } from "react";
import { ShieldAlert } from "lucide-react";

import type { AppComponentProps } from "@/apps/types";
import { Alert } from "@/components/ui/misc";

export function CustomApplicationFrame({ manifest }: AppComponentProps) {
  const application = manifest.customApplication;
  const [loadedURL, setLoadedURL] = useState<string | null>(null);
  if (!application) {
    return null;
  }

  const target = new URL(application.url);
  const sameOrigin = target.origin === window.location.origin;
  const loading = !sameOrigin && loadedURL !== application.url;

  return (
    <div className="bg-surface flex h-full min-h-0 flex-col">
      {loading ? (
        <div
          className="bg-surface-muted relative h-0.5 shrink-0 overflow-hidden"
          role="progressbar"
          aria-label={`正在加载 ${application.name}`}
          aria-valuetext="加载中"
        >
          <span className="zke-iframe-loading-bar bg-primary absolute inset-y-0 left-0 w-1/3" />
        </div>
      ) : null}

      {sameOrigin ? (
        <div className="flex flex-1 items-center justify-center p-6">
          <Alert tone="warning" className="max-w-lg">
            <span className="flex items-start gap-2">
              <ShieldAlert className="mt-0.5 size-4 shrink-0" aria-hidden />
              <span>
                为避免同源页面访问 Console 会话，ZKE 不在窗口内嵌同源地址。请使用“新标签页打开”。
              </span>
            </span>
          </Alert>
        </div>
      ) : (
        <>
          {/* Modern applications need their real origin for ES Modules,
              cookies and browser storage. Initial Console-origin URLs are
              refused above; application.manage must still be granted only to
              people trusted to choose the external content users will run. */}
          <iframe
            key={application.url}
            title={application.name}
            src={application.url}
            className="min-h-0 flex-1 border-0"
            referrerPolicy="no-referrer"
            sandbox="allow-downloads allow-forms allow-modals allow-popups allow-popups-to-escape-sandbox allow-same-origin allow-scripts"
            onLoad={() => setLoadedURL(application.url)}
          />
        </>
      )}
    </div>
  );
}
