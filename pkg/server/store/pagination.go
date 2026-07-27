package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/togettoyou/zke/pkg/shared/pagination"
)

// queryPage runs the filtered COUNT and the matching page as one pipelined
// batch.
//
// The count is a separate statement rather than a COUNT(*) OVER () window,
// because a window count disappears together with the rows whenever the
// requested offset lies past the end of the result set, which is exactly what
// happens when rows are removed while a Console sits on the last page.
//
// countSQL and pageSQL must apply the same filter and share the same leading
// placeholders; pageSQL additionally takes the limit and offset as its final
// two placeholders.
func queryPage[T any](
	ctx context.Context,
	pool *pgxpool.Pool,
	countSQL string,
	pageSQL string,
	filterArgs []any,
	page pagination.Request,
	scan func(pgx.Rows) (T, error),
	description string,
) ([]T, int, error) {
	batch := &pgx.Batch{}
	batch.Queue(countSQL, filterArgs...)
	batch.Queue(pageSQL, append(append([]any{}, filterArgs...), page.Limit, page.Offset)...)

	results := pool.SendBatch(ctx, batch)
	defer results.Close()

	var total int
	if err := results.QueryRow().Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count %s: %w", description, err)
	}

	rows, err := results.Query()
	if err != nil {
		return nil, 0, fmt.Errorf("list %s: %w", description, err)
	}
	items := make([]T, 0, page.Limit)
	for rows.Next() {
		item, err := scan(rows)
		if err != nil {
			rows.Close()
			return nil, 0, fmt.Errorf("scan %s: %w", description, err)
		}
		items = append(items, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate %s: %w", description, err)
	}
	return items, total, nil
}
