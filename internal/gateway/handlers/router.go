package handlers

import (
	"log/slog"
	"strings"
	"time"

	"bank_proto_microservice/internal/gateway/middleware"
	"bank_proto_microservice/internal/utils"

	"github.com/valyala/fasthttp"
)

type route struct {
	method   string
	pattern  string // "/accounts/{id}" — последний сегмент пути передаётся в UserValue("id")
	prefix   string
	hasParam bool
	handler  fasthttp.RequestHandler
	metric   *utils.Histogram
}

// Router сопоставляет запрос с маршрутом, пишет access-лог (уровень debug)
// и метрики латентности по каждому маршруту.
type Router struct {
	routes    []route
	unmatched *utils.Histogram
}

func (r *Router) handle(method, pattern string, h fasthttp.RequestHandler) {
	rt := route{method: method, pattern: pattern, handler: h, metric: utils.Track("http " + method + " " + pattern)}
	if strings.HasSuffix(pattern, "/{id}") {
		rt.hasParam = true
		rt.prefix = strings.TrimSuffix(pattern, "{id}")
	}
	r.routes = append(r.routes, rt)
}

// NewRouter описывает публичный HTTP API шлюза.
func NewRouter(h *GatewayHandler, auth *middleware.AuthMiddleware) *Router {
	r := &Router{unmatched: utils.Track("http unmatched")}
	protected := auth.RequireAuth

	r.handle("GET", "/health", h.Health)
	r.handle("GET", "/health/ready", h.Ready)

	r.handle("POST", "/register", h.Register)
	r.handle("POST", "/login", h.Login)
	r.handle("DELETE", "/users/me", protected(h.DeleteUser))

	r.handle("POST", "/accounts", protected(h.CreateAccount))
	r.handle("GET", "/accounts", protected(h.GetAccounts))
	r.handle("GET", "/accounts/{id}", protected(h.GetAccountByID))
	r.handle("DELETE", "/accounts/{id}", protected(h.DeleteAccount))

	r.handle("POST", "/transactions", protected(h.CreateTransaction("", false)))
	r.handle("POST", "/transactions/transfer", protected(h.CreateTransaction("transfer", false)))
	r.handle("POST", "/transactions/payment", protected(h.CreateTransaction("payment", false)))
	r.handle("POST", "/transactions/async", protected(h.CreateTransaction("", true)))
	r.handle("GET", "/transactions", protected(h.GetHistory))
	r.handle("GET", "/transactions/{id}", protected(h.GetTransactionByID))
	return r
}

func (r *Router) match(method, path string) (*route, string) {
	for i := range r.routes {
		rt := &r.routes[i]
		if rt.method != method {
			continue
		}
		if rt.pattern == path {
			return rt, ""
		}
		if rt.hasParam && strings.HasPrefix(path, rt.prefix) {
			if id := path[len(rt.prefix):]; id != "" && !strings.Contains(id, "/") {
				return rt, id
			}
		}
	}
	return nil, ""
}

func (r *Router) Handler(ctx *fasthttp.RequestCtx) {
	start := time.Now()
	method, path := string(ctx.Method()), string(ctx.Path())

	rt, id := r.match(method, path)
	metric, name := r.unmatched, "unmatched"
	if rt == nil {
		writeJSON(ctx, fasthttp.StatusNotFound, map[string]string{"error": "маршрут не найден", "code": "ROUTE_NOT_FOUND"})
	} else {
		if rt.hasParam {
			ctx.SetUserValue("id", id)
		}
		rt.handler(ctx)
		metric, name = rt.metric, rt.pattern
	}

	d := time.Since(start)
	code := ctx.Response.StatusCode()
	outcome := utils.OutcomeOK
	switch {
	case code >= 500:
		outcome = utils.OutcomeError
	case code >= 400:
		outcome = utils.OutcomeRejected
	}
	metric.Observe(d, outcome)
	slog.Debug("http", "method", method, "route", name, "path", path, "status", code,
		"dur_ms", d.Milliseconds(), "user_id", ctx.UserValue("user_id"))
}
