/**
 * Runs `worker` over `items` with a bounded number of in-flight calls.
 *
 * The Console aggregates cluster data by walking Tenants → Projects → Clusters,
 * which can fan out into many requests; the bound keeps that from flooding the
 * Server or the browser connection pool. Results keep the input order.
 */
export async function mapWithConcurrency<T, R>(
  items: readonly T[],
  limit: number,
  worker: (item: T, index: number) => Promise<R>,
): Promise<R[]> {
  const effectiveLimit = Math.max(1, Math.min(limit, items.length));
  const results = new Array<R>(items.length);
  let nextIndex = 0;

  async function run(): Promise<void> {
    for (;;) {
      const index = nextIndex++;
      if (index >= items.length) {
        return;
      }
      results[index] = await worker(items[index] as T, index);
    }
  }

  await Promise.all(Array.from({ length: effectiveLimit }, () => run()));
  return results;
}

export type Page<T> = {
  items: T[];
  hasMore: boolean;
};

/**
 * Follows `limit`/`offset` pagination until the Server reports no more rows or
 * `maxPages` is reached. Truncation is reported instead of hidden: a partial
 * cluster list must never look complete.
 */
export async function fetchAllPages<T>(
  pageSize: number,
  maxPages: number,
  fetchPage: (offset: number, limit: number) => Promise<Page<T>>,
): Promise<{ items: T[]; truncated: boolean }> {
  const items: T[] = [];
  let offset = 0;
  for (let page = 0; page < maxPages; page += 1) {
    const result = await fetchPage(offset, pageSize);
    items.push(...result.items);
    if (!result.hasMore || result.items.length === 0) {
      return { items, truncated: false };
    }
    offset += result.items.length;
  }
  return { items, truncated: true };
}
