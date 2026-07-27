package httpapi

import (
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/shared/pagination"
)

const maxListSearch = 253

// maxListFilterValue bounds an endpoint-specific filter value. The service
// validates what a filter may contain; this only stops an unbounded string
// from reaching it.
const maxListFilterValue = 253

// listQuery is the parsed form of a list endpoint's query string. Filter
// values are passed to the service unchecked against their allowed set: the
// service owns that validation so it applies to every caller, not just HTTP.
type listQuery struct {
	Page      pagination.Request
	Search    string
	Status    string
	Role      string
	ScopeType string
	extra     map[string]string
}

// Filter reports an endpoint-specific filter value, or the empty string when
// the request omitted it.
func (query listQuery) Filter(name string) string {
	return query.extra[name]
}

// listFilters declares which filters an endpoint supports. Declaring support
// positively means a new endpoint cannot accidentally accept and then silently
// ignore a filter it never implemented, and anything undeclared is rejected
// rather than dropped.
type listFilters struct {
	search    bool
	status    bool
	role      bool
	scopeType bool
	// extra names endpoint-specific filters that do not generalise across
	// list endpoints, such as the audit trail's actor and target selectors.
	extra []string
}

// listMetadata is the wire shape of a page's position. It lives here rather
// than in the pagination package so the JSON contract stays owned by the HTTP
// layer.
type listMetadata struct {
	Limit   int  `json:"limit"`
	Offset  int  `json:"offset"`
	Total   int  `json:"total"`
	HasMore bool `json:"has_more"`
}

func responsePagination(result pagination.Result) listMetadata {
	return listMetadata{
		Limit:   result.Limit,
		Offset:  result.Offset,
		Total:   result.Total,
		HasMore: result.HasMore,
	}
}

// parseListQuery reads the paging and filter parameters an endpoint supports
// and rejects any parameter it does not, rather than ignoring it. Silently
// dropping a filter is worse than refusing it: the caller believes the result
// is narrowed when it is not.
func parseListQuery(c *gin.Context, supported listFilters) (listQuery, error) {
	page := pagination.Request{Limit: pagination.DefaultLimit}
	if value := c.Query("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return listQuery{}, errors.New("list limit is invalid")
		}
		page.Limit = parsed
	}
	if value := c.Query("offset"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return listQuery{}, errors.New("list offset is invalid")
		}
		page.Offset = parsed
	}
	if err := page.Validate(); err != nil {
		return listQuery{}, err
	}

	result := listQuery{Page: page, extra: make(map[string]string)}
	known := []string{"limit", "offset"}
	for _, filter := range []struct {
		parameter string
		supported bool
		target    *string
	}{
		{"q", supported.search, &result.Search},
		{"status", supported.status, &result.Status},
		{"role", supported.role, &result.Role},
		{"scope_type", supported.scopeType, &result.ScopeType},
	} {
		if filter.supported {
			known = append(known, filter.parameter)
		}
		value := strings.TrimSpace(c.Query(filter.parameter))
		if value == "" {
			continue
		}
		if !filter.supported {
			return listQuery{}, errors.New("list filter is not supported")
		}
		*filter.target = value
	}
	for _, parameter := range supported.extra {
		known = append(known, parameter)
		value := strings.TrimSpace(c.Query(parameter))
		if value == "" {
			continue
		}
		if len(value) > maxListFilterValue {
			return listQuery{}, errors.New("list filter is too long")
		}
		result.extra[parameter] = value
	}
	for parameter := range c.Request.URL.Query() {
		if !slices.Contains(known, parameter) {
			return listQuery{}, errors.New("list parameter is not supported")
		}
	}
	if len(result.Search) > maxListSearch {
		return listQuery{}, errors.New("list search is too long")
	}
	return result, nil
}
