import { useState } from "react";

import type { AppManifest } from "./types";

export function AppGlyph({ manifest, className }: { manifest: AppManifest; className: string }) {
  const [failedURL, setFailedURL] = useState<string | null>(null);
  const logoURL = manifest.customApplication?.logo_url;
  const Icon = manifest.icon;

  if (logoURL && failedURL !== logoURL) {
    return (
      <img
        src={logoURL}
        alt=""
        className={`${className} object-contain`}
        referrerPolicy="no-referrer"
        onError={() => setFailedURL(logoURL)}
      />
    );
  }
  return <Icon className={className} strokeWidth={1.75} aria-hidden />;
}
