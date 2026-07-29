package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Felipyan19/webhook-inspector-go/internal/store"
)

func testApp(t *testing.T) http.Handler {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return New(db)
}

func TestCreateCaptureAndList(t *testing.T) {
	handler := testApp(t)
	create := httptest.NewRequest(http.MethodPost, "/api/endpoints",
		bytes.NewBufferString(`{"name":"Payments"}`))
	create.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, create)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", response.Code, response.Body)
	}
	var endpoint store.Endpoint
	if err := json.Unmarshal(response.Body.Bytes(), &endpoint); err != nil {
		t.Fatal(err)
	}

	capture := httptest.NewRequest(http.MethodPost, "/hooks/"+endpoint.Token,
		bytes.NewBufferString(`{"paid":true}`))
	capture.Header.Set("X-Signature", "test")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, capture)
	if response.Code != http.StatusAccepted {
		t.Fatalf("capture status = %d, body = %s", response.Code, response.Body)
	}

	list := httptest.NewRequest(http.MethodGet, "/api/endpoints/"+endpoint.Token+"/events", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, list)
	var events []store.Event
	if err := json.Unmarshal(response.Body.Bytes(), &events); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Body != `{"paid":true}` {
		t.Fatalf("unexpected events: %#v", events)
	}
}

func TestRejectsOversizedPayload(t *testing.T) {
	handler := testApp(t)
	request := httptest.NewRequest(http.MethodPost, "/api/endpoints",
		bytes.NewBufferString(`{"name":"Limit test"}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var endpoint store.Endpoint
	json.Unmarshal(response.Body.Bytes(), &endpoint)

	request = httptest.NewRequest(http.MethodPost, "/hooks/"+endpoint.Token,
		bytes.NewReader(make([]byte, maxBodySize+1)))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestSafeTargetBlocksPrivateNetworks(t *testing.T) {
	cases := []string{"http://localhost:3000", "http://127.0.0.1", "http://10.0.0.8/hook"}
	for _, target := range cases {
		if _, err := safeTarget(target); err == nil {
			t.Errorf("expected %s to be rejected", target)
		}
	}
}
