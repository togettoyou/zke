package validation

import (
	"encoding/hex"
	"strings"
)

func IsUUID(value string) bool {
	if len(value) != 36 ||
		value[8] != '-' ||
		value[13] != '-' ||
		value[18] != '-' ||
		value[23] != '-' {
		return false
	}
	compact := strings.NewReplacer("-", "").Replace(value)
	if len(compact) != 32 {
		return false
	}
	decoded := make([]byte, 16)
	_, err := hex.Decode(decoded, []byte(compact))
	return err == nil
}
