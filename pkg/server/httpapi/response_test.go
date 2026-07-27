package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
)

type successResponseEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func decodeSuccessResponse(
	response *httptest.ResponseRecorder,
	target any,
) error {
	var envelope successResponseEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		return fmt.Errorf("decode response envelope: %w", err)
	}
	if envelope.Code != response.Code {
		return fmt.Errorf(
			"response envelope code = %d, want HTTP status %d",
			envelope.Code,
			response.Code,
		)
	}
	if envelope.Message != "Success" {
		return fmt.Errorf(
			"response envelope message = %q, want Success",
			envelope.Message,
		)
	}
	if len(envelope.Data) == 0 {
		return errors.New("response envelope data is missing")
	}
	if err := json.Unmarshal(envelope.Data, target); err != nil {
		return fmt.Errorf("decode response data: %w", err)
	}
	return nil
}
