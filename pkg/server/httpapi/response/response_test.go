package response

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestWriteSuccessUsesUniformEnvelope(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	WriteSuccess(context, http.StatusCreated, map[string]string{"id": "resource-id"})

	if recorder.Code != http.StatusCreated {
		t.Fatalf("HTTP status = %d, want %d", recorder.Code, http.StatusCreated)
	}
	var body struct {
		Code    int               `json:"code"`
		Message string            `json:"message"`
		Data    map[string]string `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != recorder.Code || body.Message != SuccessMessage ||
		body.Data["id"] != "resource-id" {
		t.Fatalf("unexpected response envelope: %+v", body)
	}
}

func TestWriteErrorUsesUniformEnvelope(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	WriteError(
		context,
		http.StatusBadRequest,
		"invalid_request",
		"invalid request",
		"request-id",
	)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("HTTP status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	var body Error
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != recorder.Code ||
		body.Message != "invalid request" ||
		body.Data.ErrorCode != "invalid_request" ||
		body.Data.RequestID != "request-id" {
		t.Fatalf("unexpected error envelope: %+v", body)
	}
}

func TestWriteSuccessIncludesNullData(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	WriteSuccess(context, http.StatusOK, nil)

	var body map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if string(body["data"]) != "null" {
		t.Fatalf("data = %s, want null", body["data"])
	}
}
