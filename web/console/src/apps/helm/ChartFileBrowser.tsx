import { useMemo, useState } from "react";
import { ChevronDown, ChevronRight, File, FileCode2, FileWarning, Folder } from "lucide-react";

import { useHelmChartFile, useHelmChartFiles } from "@/api/queries/helm";
import type { HelmChartFileEntry } from "@/api/types";
import { CopyIconButton } from "@/components/common/copy";
import { EmptyState, ErrorState, LoadingState } from "@/components/common/state";
import { YamlEditor } from "@/components/common/yaml-editor";
import { Alert, CardTitle } from "@/components/ui/misc";
import { cn } from "@/lib/cn";

/**
 * Reading a chart's own files before installing it.
 *
 * values.yaml and the README say what a chart is for. They do not say what it
 * will create — that is only in the templates, and an operator who cannot read
 * them here reads them by fetching the archive somewhere else, which is the one
 * place ZKE has no say over what they get. So the whole archive is browsable,
 * exactly as the repository published it.
 *
 * The tree is requested by this component rather than carried by the chart
 * detail: most readers of a chart never open it, and the listing is what makes
 * the Server decide what several hundred archive members are. It reads the
 * archive the detail already fetched — the parsed chart is held for a few
 * minutes — so mounting this is not a second download. Contents are one request
 * per file: a reader opens a handful.
 *
 * This is a viewer, not an editor. What is installed is the archive the
 * repository published; the only document an operator edits is values, and that
 * happens in the install form.
 */
export function ChartFileBrowser({
  repositoryId,
  chart,
  version,
}: {
  repositoryId: string;
  chart: string;
  /** The version as it resolved, so the listing and the file reads agree. */
  version: string;
}) {
  const listing = useHelmChartFiles(repositoryId, chart, version, true);
  const files = useMemo(() => listing.data?.files ?? [], [listing.data]);
  const tree = useMemo(() => buildTree(files), [files]);
  // `null` until the listing arrives, and then the file the archive itself
  // suggests. Derived rather than stored so it does not need an effect to
  // reset: an initial state cannot see data that has not been fetched yet.
  const [chosen, setChosen] = useState<string | null>(null);
  const selected = chosen ?? defaultSelection(files);
  // Subcharts are folded to start with. A packaged dependency tree is most of
  // the file count and none of what an operator opened the chart to read; the
  // rest is expanded, because a chart's own templates are few enough to show.
  const [expanded, setExpanded] = useState<Set<string> | null>(null);
  const collapsed = expanded ?? foldedByDefault(tree);

  const toggle = (path: string) =>
    setExpanded((previous) => {
      const next = new Set(previous ?? collapsed);
      if (!next.delete(path)) {
        next.add(path);
      }
      return next;
    });

  if (listing.error) {
    return <ErrorState error={listing.error} onRetry={() => void listing.refetch()} />;
  }
  if (listing.isLoading) {
    return <LoadingState label="读取 Chart 归档…" />;
  }
  if (files.length === 0) {
    return (
      <EmptyState title="没有可浏览的文件" description="该 Chart 的归档中没有可列出的文件。" />
    );
  }

  const fileCount = listing.data?.file_count ?? files.length;
  const truncated = listing.data?.truncated ?? false;

  return (
    <div className="grid min-w-0 gap-2">
      <CardTitle>Chart 文件</CardTitle>
      <p className="text-subtle-foreground text-xs">
        仓库打包发布的原始文件，含 <code className="zke-mono">charts/</code> 下的子
        Chart。模板未经渲染：<code className="zke-mono">{"{{ }}"}</code>{" "}
        里的值要到目标集群安装时才有结果。
      </p>
      {truncated ? (
        <Alert tone="warning">
          该 Chart 的文件数超过单次列表上限，只列出了前 {files.length} 个。
        </Alert>
      ) : null}

      {/* Two columns of the same shape: a header line, then a framed box of the
          same height. The tree used to be the box alone, so it started level
          with the file name beside it and ended a header's worth of pixels
          short of the editor — the one asymmetry a reader notices without
          being able to say what it is. */}
      <div className="grid min-w-0 gap-3 @2xl:grid-cols-[15rem_minmax(0,1fr)]">
        <div className="grid min-w-0 gap-1.5">
          <div className="flex min-h-8 items-center">
            {/* The count belongs over the thing it counts, the way the file
                size sits over the file. */}
            <span className="text-subtle-foreground min-w-0 flex-1 truncate text-xs">
              共 {fileCount} 个文件
            </span>
          </div>
          <nav
            aria-label="Chart 文件"
            // The same frame the YAML editor draws for itself, so the pair reads
            // as one control rather than as a box beside a bare list.
            className="border-border rounded-control bg-surface shadow-e1 h-64 overflow-auto border p-1 @2xl:h-96"
          >
            <TreeLevel
              nodes={tree}
              depth={0}
              selected={selected}
              collapsed={collapsed}
              onSelect={setChosen}
              onToggle={toggle}
            />
          </nav>
        </div>
        <FileView
          repositoryId={repositoryId}
          chart={chart}
          version={version}
          path={selected}
          entry={files.find((file) => file.path === selected) ?? null}
        />
      </div>
    </div>
  );
}

function TreeLevel({
  nodes,
  depth,
  selected,
  collapsed,
  onSelect,
  onToggle,
}: {
  nodes: TreeNode[];
  depth: number;
  selected: string | null;
  collapsed: Set<string>;
  onSelect: (path: string) => void;
  onToggle: (path: string) => void;
}) {
  return (
    <ul className="grid">
      {/* `min-w-0` on every row, at every level. A grid item's automatic
          minimum size is its min-content size, and a file name has no spaces
          to break at — so one long name sets a floor wider than the pane, the
          whole tree grows to it, and the right-aligned sizes are carried out
          past the edge and clipped. The name truncates; the tree should not
          have to widen for it. */}
      {nodes.map((node) =>
        node.kind === "dir" ? (
          <li key={node.path} className="min-w-0">
            <TreeRow
              depth={depth}
              icon={
                collapsed.has(node.path) ? (
                  <ChevronRight className="size-3.5 shrink-0" aria-hidden="true" />
                ) : (
                  <ChevronDown className="size-3.5 shrink-0" aria-hidden="true" />
                )
              }
              onClick={() => onToggle(node.path)}
              expanded={!collapsed.has(node.path)}
            >
              <Folder className="text-subtle-foreground size-3.5 shrink-0" aria-hidden="true" />
              <span className="min-w-0 flex-1 truncate">{node.name}</span>
            </TreeRow>
            {collapsed.has(node.path) ? null : (
              <TreeLevel
                nodes={node.children}
                depth={depth + 1}
                selected={selected}
                collapsed={collapsed}
                onSelect={onSelect}
                onToggle={onToggle}
              />
            )}
          </li>
        ) : (
          <li key={node.path} className="min-w-0">
            <TreeRow
              depth={depth}
              // The chevron column is held open for files too, so names line up
              // with the directory names above them instead of shifting left.
              icon={<span className="size-3.5 shrink-0" aria-hidden="true" />}
              onClick={() => onSelect(node.path)}
              selected={selected === node.path}
            >
              {node.text ? (
                <FileCode2
                  className="text-subtle-foreground size-3.5 shrink-0"
                  aria-hidden="true"
                />
              ) : (
                <File className="text-subtle-foreground size-3.5 shrink-0" aria-hidden="true" />
              )}
              <span className="min-w-0 flex-1 truncate">{node.name}</span>
              {/* `shrink-0`: `1.3 KiB` is two words, and a size left free to
                  shrink wraps onto a second line, making that one row taller
                  than every other row in the tree. */}
              <span className="text-subtle-foreground zke-mono shrink-0 pl-2 text-[11px] whitespace-nowrap">
                {formatBytes(node.size)}
              </span>
            </TreeRow>
          </li>
        ),
      )}
    </ul>
  );
}

function TreeRow({
  depth,
  icon,
  children,
  onClick,
  selected = false,
  expanded,
}: {
  depth: number;
  icon: React.ReactNode;
  children: React.ReactNode;
  onClick: () => void;
  selected?: boolean;
  expanded?: boolean;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-expanded={expanded}
      aria-current={selected ? "true" : undefined}
      // A little taller than the text needs: a tree row is easy to hit
      // horizontally and hard to hit vertically, and `coarse:` is the only
      // question that has a real answer about the pointer.
      className={cn(
        "zke-focus rounded-control coarse:py-2 flex w-full items-center gap-1.5 py-1.5 pr-1.5 text-left text-xs",
        selected
          ? "bg-primary-surface text-foreground"
          : "text-muted-foreground hover:bg-surface-muted",
      )}
      style={{ paddingLeft: `${0.25 + depth * 0.75}rem` }}
    >
      {icon}
      {children}
    </button>
  );
}

/**
 * The selected file.
 *
 * Non-text members are named rather than shown. A chart may package anything —
 * a subchart archive, an image — and the Server decides on the bytes rather
 * than the extension, so a `.yaml` holding a gzip stream lands here too.
 */
function FileView({
  repositoryId,
  chart,
  version,
  path,
  entry,
}: {
  repositoryId: string;
  chart: string;
  version: string;
  path: string | null;
  entry: HelmChartFileEntry | null;
}) {
  // A file the tree already says is binary is not requested: the answer is
  // knowable from the listing, and asking would spend a round trip to be told
  // what is already on screen.
  const readable = Boolean(path) && entry?.text === true;
  const file = useHelmChartFile(repositoryId, chart, version, readable ? path : null);

  return (
    <div className="grid min-w-0 gap-1.5">
      {/* The header is always present, including before anything is selected:
          it is where the pane's height comes from, and letting it appear with
          the first file would shift the editor down as the content arrives. */}
      <div className="flex min-h-8 flex-wrap items-center gap-2">
        <span className="zke-mono text-foreground min-w-0 flex-1 truncate text-xs">
          {path ?? "未选择文件"}
        </span>
        {entry ? (
          <span className="text-subtle-foreground zke-mono text-[11px]">
            {formatBytes(entry.size)}
          </span>
        ) : null}
        {file.data?.content ? (
          <CopyIconButton value={file.data.content} label={`复制 ${path ?? "文件"}`} />
        ) : null}
      </div>
      {file.data?.truncated ? (
        <Alert tone="warning">
          该文件超过单次读取上限，只显示了前 {formatBytes(file.data.content.length)}，共{" "}
          {formatBytes(file.data.size)}。
        </Alert>
      ) : null}
      <FileBody path={path} entry={entry} file={file} />
    </div>
  );
}

function FileBody({
  path,
  entry,
  file,
}: {
  path: string | null;
  entry: HelmChartFileEntry | null;
  file: ReturnType<typeof useHelmChartFile>;
}) {
  // Every state is the same height, so choosing a file does not move the tree
  // beside it or the README below it.
  const frame =
    "border-border rounded-control bg-surface shadow-e1 grid h-96 place-items-center border p-4";

  if (!path || !entry) {
    return (
      <div className={frame}>
        <p className="text-subtle-foreground text-xs">从左侧选择一个文件。</p>
      </div>
    );
  }
  if (!entry.text) {
    return (
      <div className={frame}>
        <div className="grid justify-items-center gap-2 text-center">
          <FileWarning className="text-subtle-foreground size-6" aria-hidden="true" />
          <p className="text-subtle-foreground text-xs">
            这是一个二进制文件（{formatBytes(entry.size)}），没有可展示的文本内容。
          </p>
        </div>
      </div>
    );
  }
  if (file.error) {
    return (
      <div className={frame}>
        <ErrorState error={file.error} onRetry={() => void file.refetch()} />
      </div>
    );
  }
  if (!file.data) {
    return (
      <div className={frame}>
        <LoadingState label="读取文件…" />
      </div>
    );
  }
  return (
    /* The same read-only editor the rest of the Console uses for YAML. Chart
       files are overwhelmingly YAML or Go templates over YAML; a `.md` or a
       `.json` gets line numbers and monospace, which is what it needed.

       A height rather than a cap: the editor paints into absolutely positioned
       layers, so `max-h` alone leaves it nothing to be capped against and it
       collapses to a single line. It scrolls inside this. */
    <YamlEditor
      value={file.data.content || "# （空文件）"}
      onChange={() => {}}
      readOnly
      label={`${path} 内容`}
      className="h-96"
    />
  );
}

type FileNode = { kind: "file"; name: string; path: string; size: number; text: boolean };
type DirNode = { kind: "dir"; name: string; path: string; children: TreeNode[] };
type TreeNode = FileNode | DirNode;

/**
 * Turns the flat archive listing into a tree.
 *
 * The Server returns paths because that is what the archive holds; nesting is a
 * property of how they are read, not of how they are stored, so it is derived
 * here rather than shipped over the wire.
 */
function buildTree(files: HelmChartFileEntry[]): TreeNode[] {
  const root: DirNode = { kind: "dir", name: "", path: "", children: [] };
  const directories = new Map<string, DirNode>([["", root]]);
  for (const file of files) {
    const segments = file.path.split("/").filter(Boolean);
    const name = segments.at(-1);
    if (name === undefined) {
      continue;
    }
    let parent = root;
    let prefix = "";
    for (const segment of segments.slice(0, -1)) {
      prefix = prefix ? `${prefix}/${segment}` : segment;
      let directory = directories.get(prefix);
      if (!directory) {
        directory = { kind: "dir", name: segment, path: prefix, children: [] };
        directories.set(prefix, directory);
        parent.children.push(directory);
      }
      parent = directory;
    }
    parent.children.push({
      kind: "file",
      name,
      path: file.path,
      size: file.size,
      text: file.text,
    });
  }
  sortLevel(root);
  return root.children;
}

function sortLevel(directory: DirNode) {
  directory.children.sort((left, right) => {
    if (left.kind !== right.kind) {
      return left.kind === "dir" ? -1 : 1;
    }
    return left.name.localeCompare(right.name);
  });
  for (const child of directory.children) {
    if (child.kind === "dir") {
      sortLevel(child);
    }
  }
}

/**
 * What is open when the browser first appears.
 *
 * Chart.yaml rather than values.yaml: the defaults have a view of their own
 * beside this one, and Chart.yaml is the file that says what the thing is.
 */
function defaultSelection(files: HelmChartFileEntry[]): string | null {
  for (const preferred of ["Chart.yaml", "values.yaml", "README.md"]) {
    if (files.some((file) => file.path === preferred && file.text)) {
      return preferred;
    }
  }
  return files.find((file) => file.text)?.path ?? null;
}

/** The packaged subchart tree, folded so it does not bury the chart's own files. */
function foldedByDefault(tree: TreeNode[]): Set<string> {
  const folded = new Set<string>();
  for (const node of tree) {
    if (node.kind === "dir" && node.name === "charts") {
      folded.add(node.path);
    }
  }
  return folded;
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) {
    return `${bytes} B`;
  }
  const units = ["KiB", "MiB", "GiB"] as const;
  let value = bytes / 1024;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value < 10 ? value.toFixed(1) : Math.round(value)} ${units[unit] ?? "GiB"}`;
}
