package httpapi

import (
	"errors"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	defaultListLimit = 50
	maxListLimit     = 100
	maxListOffset    = 1_000_000
	maxListSearch    = 253
)

type listQuery struct {
	Limit     int
	Offset    int
	Search    string
	Status    string
	Role      string
	ScopeType string
}

type listMetadata struct {
	Limit   int  `json:"limit"`
	Offset  int  `json:"offset"`
	Total   int  `json:"total"`
	HasMore bool `json:"has_more"`
}

func parseListQuery(c *gin.Context) (listQuery, error) {
	result := listQuery{
		Limit:     defaultListLimit,
		Search:    strings.TrimSpace(c.Query("q")),
		Status:    strings.TrimSpace(c.Query("status")),
		Role:      strings.TrimSpace(c.Query("role")),
		ScopeType: strings.TrimSpace(c.Query("scope_type")),
	}
	if len(result.Search) > maxListSearch {
		return listQuery{}, errors.New("list search is too long")
	}
	if value := c.Query("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > maxListLimit {
			return listQuery{}, errors.New("list limit is invalid")
		}
		result.Limit = parsed
	}
	if value := c.Query("offset"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 || parsed > maxListOffset {
			return listQuery{}, errors.New("list offset is invalid")
		}
		result.Offset = parsed
	}
	return result, nil
}

func paginate[T any](items []T, query listQuery) ([]T, listMetadata) {
	total := len(items)
	start := min(query.Offset, total)
	end := min(start+query.Limit, total)
	return items[start:end], listMetadata{
		Limit:   query.Limit,
		Offset:  query.Offset,
		Total:   total,
		HasMore: end < total,
	}
}

func containsFold(query string, values ...string) bool {
	if query == "" {
		return true
	}
	query = strings.ToLower(query)
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), query) {
			return true
		}
	}
	return false
}

func allowed(value string, candidates ...string) bool {
	if value == "" {
		return true
	}
	for _, candidate := range candidates {
		if value == candidate {
			return true
		}
	}
	return false
}
