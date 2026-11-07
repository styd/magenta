package main

import (
	"fmt"
	"strings"
	"time"
)

// What a Rails application logs for a request, the way Rails does it, and the
// backtrace of the requests that fail.

// versions are the gems that a backtrace passes through.
var versions = map[string]string{
	"actionpack":    "7.1.3",
	"actionview":    "7.1.3",
	"activerecord":  "7.1.3",
	"activesupport": "7.1.3",
	"railties":      "7.1.3",
	"rack":          "3.0.9",
	"redis-client":  "0.19.1",
	"unicorn":       "6.1.0",
}

func gem(name, file string, line int, method string) string {
	return fmt.Sprintf("/usr/local/bundle/gems/%s-%s/lib/%s:%d:in `%s'", name, versions[name], file, line, method)
}

func app(file string, line int, method string) string {
	return fmt.Sprintf("/srv/app/%s:%d:in `%s'", file, line, method)
}

func ruby(file string, line int, method string) string {
	return fmt.Sprintf("/usr/local/lib/ruby/3.3.0/%s:%d:in `%s'", file, line, method)
}

// railsTail is the end of every backtrace: from the controller, through the
// router and the middleware, to the unicorn worker that served the request.
var railsTail = []string{
	gem("actionpack", "action_controller/metal/basic_implicit_render.rb", 6, "send_action"),
	gem("actionpack", "abstract_controller/base.rb", 215, "process_action"),
	gem("actionpack", "action_controller/metal/rendering.rb", 165, "process_action"),
	gem("actionpack", "abstract_controller/callbacks.rb", 234, "block in process_action"),
	gem("activesupport", "active_support/callbacks.rb", 118, "block in run_callbacks"),
	gem("activesupport", "active_support/callbacks.rb", 127, "run_callbacks"),
	gem("actionpack", "abstract_controller/callbacks.rb", 233, "process_action"),
	gem("actionpack", "action_controller/metal/rescue.rb", 23, "process_action"),
	gem("actionpack", "action_controller/metal/instrumentation.rb", 42, "block in process_action"),
	gem("activesupport", "active_support/notifications.rb", 206, "block in instrument"),
	gem("activesupport", "active_support/notifications/instrumenter.rb", 58, "instrument"),
	gem("activesupport", "active_support/notifications.rb", 206, "instrument"),
	gem("actionpack", "action_controller/metal/instrumentation.rb", 41, "process_action"),
	gem("actionpack", "abstract_controller/base.rb", 151, "process"),
	gem("actionpack", "action_controller/metal.rb", 261, "dispatch"),
	gem("actionpack", "action_dispatch/routing/route_set.rb", 50, "dispatch"),
	gem("actionpack", "action_dispatch/routing/route_set.rb", 33, "serve"),
	gem("actionpack", "action_dispatch/journey/router.rb", 50, "block in serve"),
	gem("actionpack", "action_dispatch/journey/router.rb", 32, "each"),
	gem("actionpack", "action_dispatch/journey/router.rb", 32, "serve"),
	gem("actionpack", "action_dispatch/routing/route_set.rb", 852, "call"),
	gem("rack", "rack/etag.rb", 29, "call"),
	gem("rack", "rack/conditional_get.rb", 31, "call"),
	gem("actionpack", "action_dispatch/middleware/cookies.rb", 704, "call"),
	gem("actionpack", "action_dispatch/middleware/debug_exceptions.rb", 29, "call"),
	gem("actionpack", "action_dispatch/middleware/show_exceptions.rb", 31, "call"),
	gem("railties", "rails/rack/logger.rb", 37, "call_app"),
	gem("railties", "rails/rack/logger.rb", 24, "call"),
	gem("actionpack", "action_dispatch/middleware/request_id.rb", 26, "call"),
	gem("rack", "rack/runtime.rb", 24, "call"),
	gem("actionpack", "action_dispatch/middleware/executor.rb", 14, "call"),
	gem("unicorn", "unicorn/http_server.rb", 634, "process_client"),
	gem("unicorn", "unicorn/http_server.rb", 732, "worker_loop"),
	gem("unicorn", "unicorn/http_server.rb", 132, "block in spawn_missing_workers"),
	gem("unicorn", "unicorn/http_server.rb", 129, "fork"),
	gem("unicorn", "unicorn/http_server.rb", 129, "spawn_missing_workers"),
	gem("unicorn", "unicorn/http_server.rb", 140, "maintain_worker_count"),
	gem("unicorn", "unicorn/http_server.rb", 299, "join"),
	"/usr/local/bundle/gems/unicorn-6.1.0/bin/unicorn:128:in `<top (required)>'",
	"/usr/local/bundle/bin/unicorn:25:in `load'",
	"/usr/local/bundle/bin/unicorn:25:in `<main>'",
}

var statusText = map[int]string{
	200: "OK", 201: "Created", 302: "Found", 304: "Not Modified", 400: "Bad Request",
	404: "Not Found", 422: "Unprocessable Entity", 500: "Internal Server Error",
}

// route is one endpoint of the application.
type route struct {
	weight       int
	verb         string
	path         string // holds one %v for the id, the page or the search term when arg is set
	arg          string // "id", "page", "term", or "" for none
	ctrl, action string
	file         string // the controller file and the view folder
	model        string
	format       string // "HTML" or "JSON"
	ok           int    // the status of a good response
	fast, spread int    // a request takes fast ms, plus up to spread
	light        bool   // no database and no views
	slow         bool   // may take seconds
}

var routes = []route{
	{weight: 12, verb: "GET", path: "/health", ctrl: "HealthController", action: "show", file: "health", format: "JSON", ok: 200, fast: 1, spread: 3, light: true},
	{weight: 18, verb: "GET", path: "/orders?page=%v", arg: "page", ctrl: "OrdersController", action: "index", file: "orders", model: "Order", format: "HTML", ok: 200, fast: 40, spread: 200},
	{weight: 16, verb: "GET", path: "/orders/%v", arg: "id", ctrl: "OrdersController", action: "show", file: "orders", model: "Order", format: "HTML", ok: 200, fast: 15, spread: 70},
	{weight: 4, verb: "POST", path: "/orders", ctrl: "OrdersController", action: "create", file: "orders", model: "Order", format: "HTML", ok: 302, fast: 30, spread: 90},
	{weight: 3, verb: "PATCH", path: "/orders/%v", arg: "id", ctrl: "OrdersController", action: "update", file: "orders", model: "Order", format: "HTML", ok: 302, fast: 25, spread: 80},
	{weight: 14, verb: "GET", path: "/api/v2/invoices?page=%v", arg: "page", ctrl: "Api::V2::InvoicesController", action: "index", file: "api/v2/invoices", model: "Invoice", format: "JSON", ok: 200, fast: 12, spread: 90},
	{weight: 8, verb: "GET", path: "/api/v2/invoices/%v", arg: "id", ctrl: "Api::V2::InvoicesController", action: "show", file: "api/v2/invoices", model: "Invoice", format: "JSON", ok: 200, fast: 8, spread: 40},
	{weight: 3, verb: "POST", path: "/api/v2/invoices", ctrl: "Api::V2::InvoicesController", action: "create", file: "api/v2/invoices", model: "Invoice", format: "JSON", ok: 201, fast: 20, spread: 80},
	{weight: 6, verb: "GET", path: "/search?q=%v", arg: "term", ctrl: "SearchController", action: "index", file: "search", model: "Order", format: "HTML", ok: 200, fast: 60, spread: 240, slow: true},
	{weight: 6, verb: "GET", path: "/users/%v", arg: "id", ctrl: "UsersController", action: "show", file: "users", model: "User", format: "HTML", ok: 200, fast: 10, spread: 50},
	{weight: 2, verb: "GET", path: "/reports/monthly", ctrl: "ReportsController", action: "monthly", file: "reports", model: "Report", format: "HTML", ok: 200, fast: 700, spread: 2500, slow: true},
	{weight: 3, verb: "POST", path: "/checkouts", ctrl: "CheckoutsController", action: "create", file: "checkouts", model: "Order", format: "HTML", ok: 302, fast: 120, spread: 400},
}

// route picks an endpoint at random, by weight, among those that fit.
func (g *generator) route(fits func(route) bool) route {
	total := 0
	for _, r := range routes {
		if fits(r) {
			total += r.weight
		}
	}
	roll := g.rng.Intn(total)
	for _, r := range routes {
		if !fits(r) {
			continue
		}
		if roll < r.weight {
			return r
		}
		roll -= r.weight
	}
	panic("no route fits")
}

// outcome is the status of a request: nearly always what the route answers
// when all goes well.
func (g *generator) outcome(r route) int {
	if r.light {
		return r.ok
	}
	switch roll := g.rng.Intn(100); {
	case roll < 90:
		return r.ok
	case roll < 95:
		if r.verb == "GET" && r.format == "HTML" {
			return 304
		}
	case roll < 97:
		return 404
	default:
		if r.verb != "GET" {
			return 422
		}
	}
	return r.ok
}

// call is one request, from the line that starts it to the line that ends it.
type call struct {
	g      *generator
	r      route
	worker int
	pid    int
	ip     string
	arg    any
	path   string
	ms     int
	out    []entry
}

// begin logs the start of a request, on a worker picked at random.
func (g *generator) begin(r route) *call {
	n := g.worker()
	c := &call{g: g, r: r, worker: n, pid: g.workers[n], ms: r.fast + g.rng.Intn(r.spread+1)}
	c.ip = fmt.Sprintf("10.0.%d.%d", g.rng.Intn(4), 2+g.rng.Intn(250))
	var args []any
	param := "id"
	switch r.arg {
	case "id":
		c.arg = 100 + g.rng.Intn(9900)
	case "page":
		c.arg, param = 1+g.rng.Intn(40), "page"
	case "term":
		c.arg, param = g.pick("kraken", "invoice", "refund", "pallet", "widget"), "q"
	}
	if c.arg != nil {
		args = []any{c.arg}
	}
	c.path = fmt.Sprintf(r.path, args...)
	c.out = append(c.out,
		g.line('I', c.pid, `Started %s "%s" for %s at %s`, r.verb, c.path, c.ip, g.clock.Format("2006-01-02 15:04:05 -0700")),
		g.line('I', c.pid, "Processing by %s#%s as %s", r.ctrl, r.action, r.format),
	)
	if c.arg != nil {
		c.out = append(c.out, g.line('I', c.pid, `  Parameters: {"%s"=>"%v"}`, param, c.arg))
	}
	return c
}

// finish logs the end of the request, after the time it took.
func (c *call) finish(status int) []entry {
	g := c.g
	switch status {
	case 304:
		c.ms = c.ms/4 + 1
	case 400, 404, 422:
		c.ms = c.ms/2 + 1
	}
	g.clock = g.clock.Add(time.Duration(c.ms) * time.Millisecond)
	if status == 302 {
		id := c.arg
		if c.r.arg != "id" {
			id = 100 + g.rng.Intn(9900)
		}
		c.out = append(c.out, g.line('I', c.pid, "Redirected to https://shop.example.com/orders/%v", id))
	}
	db := float64(c.ms) * float64(10+g.rng.Intn(30)) / 100
	views := float64(c.ms) * float64(20+g.rng.Intn(40)) / 100
	allocations := 300 + c.ms*30 + g.rng.Intn(400)
	var detail string
	switch {
	case c.r.light:
		detail = fmt.Sprintf("(Allocations: %d)", allocations)
	case status == 200 || status == 201 || status == 422:
		detail = fmt.Sprintf("(Views: %.1fms | ActiveRecord: %.1fms | Allocations: %d)", views, db, allocations)
	default:
		detail = fmt.Sprintf("(ActiveRecord: %.1fms | Allocations: %d)", db, allocations)
	}
	c.out = append(c.out, g.line('I', c.pid, "Completed %d %s in %dms %s", status, statusText[status], c.ms, detail))
	return c.out
}

// request is a request that goes well, nearly always.
func (g *generator) request() []entry {
	r := g.route(func(route) bool { return true })
	return g.begin(r).finish(g.outcome(r))
}

// slowRequest is a request that takes seconds, which the application warns of.
func (g *generator) slowRequest() []entry {
	c := g.begin(g.route(func(r route) bool { return r.slow }))
	c.ms = 3000 + g.rng.Intn(5000)
	out := c.finish(200)
	return append(out, g.line('W', c.pid, "worker=%d slow request: %s %s took %d.%ds ip=%s",
		c.worker, c.r.verb, c.path, c.ms/1000, c.ms%1000/100, c.ip))
}

// failure is an exception that a request can raise.
type failure struct {
	status int
	fits   func(route) bool // nil when any route can fail this way
	build  func(g *generator, c *call) (message string, frames []string)
}

func (c *call) action(line int) string {
	return app("app/controllers/"+c.r.file+"_controller.rb", line, c.r.action)
}

var failures = []failure{
	{status: 500, build: func(g *generator, c *call) (string, []string) {
		return "undefined method `name' for nil (NoMethodError)", []string{
			app("app/models/user.rb", 12, "name"),
			app("app/models/order.rb", 31, "customer_name"),
			c.action(8 + g.rng.Intn(30)),
		}
	}},
	{status: 404, fits: func(r route) bool { return r.arg == "id" }, build: func(g *generator, c *call) (string, []string) {
		return fmt.Sprintf("Couldn't find %s with 'id'=%v (ActiveRecord::RecordNotFound)", c.r.model, c.arg), []string{
			gem("activerecord", "active_record/core.rb", 296, "find"),
			app("app/controllers/"+c.r.file+"_controller.rb", 40+g.rng.Intn(20), "set_"+strings.ToLower(c.r.model)),
		}
	}},
	{status: 500, build: func(g *generator, c *call) (string, []string) {
		return `connection to server at "10.0.3.14", port 5432 failed: Connection refused (ActiveRecord::ConnectionNotEstablished)`, []string{
			gem("activerecord", "active_record/connection_adapters/postgresql_adapter.rb", 94, "rescue in new_client"),
			gem("activerecord", "active_record/connection_adapters/postgresql_adapter.rb", 88, "new_client"),
			gem("activerecord", "active_record/connection_adapters/abstract/connection_pool.rb", 748, "checkout_new_connection"),
			gem("activerecord", "active_record/connection_adapters/abstract/connection_pool.rb", 727, "try_to_checkout_new_connection"),
			gem("activerecord", "active_record/connection_adapters/abstract/connection_pool.rb", 708, "acquire_connection"),
			gem("activerecord", "active_record/connection_adapters/abstract/connection_pool.rb", 342, "checkout"),
			gem("activerecord", "active_record/connection_handling.rb", 276, "connection"),
			app("app/services/report_builder.rb", 33, "call"),
			c.action(8 + g.rng.Intn(30)),
		}
	}},
	{status: 500, build: func(g *generator, c *call) (string, []string) {
		return "Error connecting to Redis on 10.0.3.9:6379 (Errno::ECONNREFUSED) (Redis::CannotConnectError)", []string{
			gem("redis-client", "redis_client/ruby_connection.rb", 82, "rescue in initialize"),
			gem("redis-client", "redis_client/ruby_connection.rb", 77, "initialize"),
			gem("redis-client", "redis_client/config.rb", 105, "new_pool"),
			app("app/services/notifier.rb", 17, "enqueue"),
			c.action(8 + g.rng.Intn(30)),
		}
	}},
	{status: 500, fits: func(r route) bool { return r.format == "HTML" && !r.light }, build: func(g *generator, c *call) (string, []string) {
		name := strings.NewReplacer("/", "_").Replace(c.r.file) + "_" + c.r.action
		view := fmt.Sprintf("_app_views_%s_html_erb___%d_%d", name, 1000000000000000000+g.rng.Int63n(8000000000000000000), 70000000000000+g.rng.Int63n(300000000000000))
		return fmt.Sprintf("undefined local variable or method `total_cents' for an instance of #<ActionView::Base:0x00007f%06x> (ActionView::Template::Error)", g.rng.Intn(0xffffff)), []string{
			app(fmt.Sprintf("app/views/%s/%s.html.erb", c.r.file, c.r.action), 5+g.rng.Intn(40), view),
			gem("actionview", "action_view/base.rb", 252, "public_send"),
			gem("actionview", "action_view/base.rb", 252, "_run"),
			gem("actionview", "action_view/template.rb", 159, "block in render"),
			gem("activesupport", "active_support/notifications.rb", 206, "block in instrument"),
			gem("activesupport", "active_support/notifications/instrumenter.rb", 58, "instrument"),
			gem("actionview", "action_view/template.rb", 363, "instrument_render_template"),
			gem("actionview", "action_view/template.rb", 157, "render"),
			gem("actionview", "action_view/renderer/template_renderer.rb", 69, "render_template"),
			gem("actionview", "action_view/renderer/template_renderer.rb", 14, "render"),
			c.action(8 + g.rng.Intn(30)),
		}
	}},
	{status: 500, fits: func(r route) bool { return r.verb != "GET" }, build: func(g *generator, c *call) (string, []string) {
		return "Net::ReadTimeout with #<TCPSocket:(closed)> (Faraday::TimeoutError)", []string{
			ruby("net/protocol.rb", 229, "rbuf_fill"),
			ruby("net/protocol.rb", 199, "readuntil"),
			ruby("net/protocol.rb", 209, "readline"),
			ruby("net/http/response.rb", 158, "read_status_line"),
			ruby("net/http/response.rb", 147, "read_new"),
			ruby("net/http.rb", 1862, "block in transport_request"),
			ruby("net/http.rb", 1853, "catch"),
			ruby("net/http.rb", 1853, "transport_request"),
			ruby("net/http.rb", 1826, "request"),
			app("app/clients/payments_client.rb", 41, "charge"),
			c.action(8 + g.rng.Intn(30)),
		}
	}},
	{status: 400, fits: func(r route) bool { return r.verb != "GET" }, build: func(g *generator, c *call) (string, []string) {
		return "param is missing or the value is empty: order (ActionController::ParameterMissing)", []string{
			gem("actionpack", "action_controller/metal/strong_parameters.rb", 2020, "require"),
			app("app/controllers/"+c.r.file+"_controller.rb", 60+g.rng.Intn(15), strings.ToLower(c.r.model)+"_params"),
			c.action(8 + g.rng.Intn(30)),
		}
	}},
}

// appError is a request that raises. Rails logs the end of the request, then
// the application logs the exception the way Ruby prints one: the place it was
// raised and the message on the line with the logger prefix, and the rest of
// the backtrace as indented "from" lines without one.
func (g *generator) appError() []entry {
	r := g.route(func(r route) bool { return !r.light })
	f := failures[g.rng.Intn(len(failures))]
	for f.fits != nil && !f.fits(r) {
		f = failures[g.rng.Intn(len(failures))]
	}
	c := g.begin(r)
	message, frames := f.build(g, c)
	out := c.finish(f.status)
	frames = append(frames, railsTail...)
	g.clock = g.clock.Add(time.Millisecond)
	out = append(out, g.line('E', c.pid, "%s: %s", frames[0], message))
	for _, frame := range frames[1:] {
		out = append(out, entry{at: g.clock, text: "    from " + frame})
	}
	return out
}
