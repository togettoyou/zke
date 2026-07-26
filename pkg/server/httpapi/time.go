package httpapi

import "time"

const utc8OffsetSeconds = 8 * 60 * 60

var utc8Location = time.FixedZone("UTC+8", utc8OffsetSeconds)

func responseTime(value time.Time) time.Time {
	return value.In(utc8Location)
}

func responseTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	normalized := responseTime(*value)
	return &normalized
}
