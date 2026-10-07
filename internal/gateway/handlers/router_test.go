package handlers

import (
	"encoding/json"
	"net/http"
	"testing"

	"bank_proto_microservice/internal/apperr"

	"github.com/valyala/fasthttp"
	"google.golang.org/grpc/codes"
)

func TestHTTPStatusMapping(t *testing.T) {
	cases := map[codes.Code]int{
		codes.InvalidArgument:    http.StatusBadRequest,
		codes.Unauthenticated:    http.StatusUnauthorized,
		codes.PermissionDenied:   http.StatusForbidden,
		codes.NotFound:           http.StatusNotFound,
		codes.AlreadyExists:      http.StatusConflict,
		codes.Aborted:            http.StatusConflict,
		codes.FailedPrecondition: http.StatusUnprocessableEntity,
		codes.ResourceExhausted:  http.StatusServiceUnavailable,
		codes.Unavailable:        http.StatusServiceUnavailable,
		codes.DeadlineExceeded:   http.StatusGatewayTimeout,
		codes.Internal:           http.StatusInternalServerError,
		codes.Unknown:            http.StatusInternalServerError,
	}
	for c, want := range cases {
		if got := HTTPStatus(c); got != want {
			t.Errorf("HTTPStatus(%v) = %d, want %d", c, got, want)
		}
	}
}

func TestWriteErrorBody(t *testing.T) {
	var ctx fasthttp.RequestCtx
	writeError(&ctx, apperr.New(codes.DeadlineExceeded, apperr.ReasonOutcomeUnknown, "исход неизвестен", apperr.MetaTransactionID, "tx-42"))
	if ctx.Response.StatusCode() != http.StatusGatewayTimeout {
		t.Fatalf("статус %d", ctx.Response.StatusCode())
	}
	var body map[string]string
	if err := json.Unmarshal(ctx.Response.Body(), &body); err != nil {
		t.Fatal(err)
	}
	if body["code"] != apperr.ReasonOutcomeUnknown || body["transaction_id"] != "tx-42" {
		t.Fatalf("тело ответа: %v", body)
	}
}

func TestRouterMatch(t *testing.T) {
	noop := func(*fasthttp.RequestCtx) {}
	r := &Router{}
	r.handle("POST", "/transactions/transfer", noop)
	r.handle("GET", "/transactions/{id}", noop)
	r.handle("GET", "/transactions", noop)

	cases := []struct {
		method, path, pattern, id string
	}{
		{"POST", "/transactions/transfer", "/transactions/transfer", ""},
		{"GET", "/transactions/abc", "/transactions/{id}", "abc"},
		{"GET", "/transactions", "/transactions", ""},
		{"GET", "/transactions/", "", ""},
		{"GET", "/transactions/a/b", "", ""},
		{"DELETE", "/transactions/abc", "", ""},
	}
	for _, c := range cases {
		rt, id := r.match(c.method, c.path)
		got := ""
		if rt != nil {
			got = rt.pattern
		}
		if got != c.pattern || id != c.id {
			t.Errorf("%s %s: маршрут %q id %q, ожидалось %q %q", c.method, c.path, got, id, c.pattern, c.id)
		}
	}
}
