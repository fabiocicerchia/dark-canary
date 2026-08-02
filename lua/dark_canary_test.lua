-- Unit tests for lua/dark_canary.lua.
--
-- Plain asserts, no framework: the module is 77 lines and needs two stubs
-- (`ngx` and the nginx-lua-waf-kit `mirror` module), which is less setup than
-- any test runner would ask for.
--
--   lua lua/dark_canary_test.lua        # from the repo root
--
-- The stubs are the point. This code never runs outside OpenResty, so the only
-- way to pin the decision logic — which is what decides whether production
-- traffic gets mirrored at all — is to fake the world around it.

package.path = "lua/?.lua;" .. package.path

-- --- stubs -------------------------------------------------------------------

local kit = {}
local ngx_stub = {}

-- Reset to a known world before each test. Anything a test wants to observe
-- (logs, shipped payloads, the args the kit was called with) accumulates here.
local function reset()
  ngx_stub.ctx = {}
  ngx_stub.var = {}
  ngx_stub.logs = {}
  ngx_stub.WARN = "warn"
  ngx_stub.ERR = "err"
  ngx_stub.log = function(level, msg) table.insert(ngx_stub.logs, { level = level, msg = msg }) end

  kit.decide_result = true
  kit.decide_args = nil
  kit.body_filter_args = nil
  kit.capture_result = nil
  kit.shipped = nil

  kit.decide = function(o) kit.decide_args = o; return kit.decide_result end
  kit.body_filter = function(o) kit.body_filter_args = o; return "filtered" end
  kit.capture = function(o) kit.capture_args = o; return kit.capture_result end
  kit.ship = function(capture, collector) kit.shipped = { capture = capture, collector = collector } end
end

_G.ngx = ngx_stub
package.preload["mirror"] = function() return kit end

local dc = require("dark_canary")

-- --- runner ------------------------------------------------------------------

local failures, count = {}, 0

local function test(name, fn)
  count = count + 1
  reset()
  local ok, err = pcall(fn)
  if not ok then
    table.insert(failures, name .. ": " .. tostring(err))
    io.write("FAIL ", name, "\n")
  else
    io.write("ok   ", name, "\n")
  end
end

local function eq(got, want, what)
  if got ~= want then
    error(string.format("%s = %s, want %s", what or "value", tostring(got), tostring(want)), 2)
  end
end

-- --- option merging ----------------------------------------------------------

test("defaults apply when no options are passed", function()
  dc.decide()
  eq(kit.decide_args.path, "primary", "path")
  eq(kit.decide_args.header, "X-Dark-Canary-Id", "header")
  eq(kit.decide_args.collector.port, 8099, "collector.port")
end)

test("options override defaults", function()
  dc.decide({ header = "X-Trace" })
  eq(kit.decide_args.header, "X-Trace", "header")
  eq(kit.decide_args.path, "primary", "path stays default")
end)

-- Overriding one collector field is the natural thing an operator writes. A
-- shallow merge would silently drop port/path/timeout and ship nowhere.
test("a partial collector override keeps the other collector fields", function()
  dc.decide({ collector = { host = "10.0.0.5" } })
  local c = kit.decide_args.collector
  eq(c.host, "10.0.0.5", "collector.host")
  eq(c.port, 8099, "collector.port")
  eq(c.path, "/captures", "collector.path")
  eq(c.timeout, 1000, "collector.timeout")
end)

-- If merge wrote into DEFAULTS, the first caller with options would rewrite the
-- defaults for every later request in the worker.
test("merging does not mutate the shared DEFAULTS table", function()
  dc.decide({ path = "shadow", header = "X-Trace", collector = { host = "10.0.0.5" } })
  eq(dc.DEFAULTS.path, "primary", "DEFAULTS.path")
  eq(dc.DEFAULTS.header, "X-Dark-Canary-Id", "DEFAULTS.header")
  eq(dc.DEFAULTS.collector.host, "127.0.0.1", "DEFAULTS.collector.host")
end)

-- --- decide: primary ---------------------------------------------------------

test("the primary delegates the whole decision to the kit", function()
  kit.decide_result = false
  eq(dc.decide(), false, "decide")
  eq(kit.decide_args ~= nil, true, "kit.decide was called")
end)

-- --- decide: shadow ----------------------------------------------------------

-- The shadow request only exists because the primary mirrored it, so there is
-- no sampling decision left to make.
test("the shadow captures unconditionally and adopts the incoming id", function()
  ngx_stub.var["http_x_dark_canary_id"] = "abc123"
  eq(dc.decide({ path = "shadow" }), true, "decide")
  eq(ngx_stub.ctx.dc_mirror, true, "ctx.dc_mirror")
  eq(ngx_stub.ctx.dc_correl_id, "abc123", "ctx.dc_correl_id")
  eq(kit.decide_args, nil, "the kit must not be consulted on the shadow")
end)

-- Without an id there is nothing to pair the capture with, and an unpaired
-- capture is worse than no capture: it sits in the collector until it expires.
test("a shadow request with no correlation id is not captured", function()
  eq(dc.decide({ path = "shadow" }), false, "decide")
  eq(ngx_stub.ctx.dc_mirror, false, "ctx.dc_mirror")
  eq(#ngx_stub.logs, 1, "one warning")
  eq(ngx_stub.logs[1].level, "warn", "log level")
end)

-- X-Dark-Canary-Id has to become http_x_dark_canary_id: nginx lowercases header
-- names and turns dashes into underscores. Get this wrong and the shadow reads
-- nil for every request, which looks exactly like "no traffic".
test("the header name is translated to its nginx variable form", function()
  ngx_stub.var["http_x_trace_id"] = "from-custom-header"
  dc.decide({ path = "shadow", header = "X-Trace-Id" })
  eq(ngx_stub.ctx.dc_correl_id, "from-custom-header", "ctx.dc_correl_id")
end)

test("an already-lowercase header name still resolves", function()
  ngx_stub.var["http_x_trace_id"] = "v"
  dc.decide({ path = "shadow", header = "x-trace-id" })
  eq(ngx_stub.ctx.dc_correl_id, "v", "ctx.dc_correl_id")
end)

-- --- body_filter -------------------------------------------------------------

test("body_filter delegates to the kit with merged options", function()
  eq(dc.body_filter({ path = "shadow" }), "filtered", "return value")
  eq(kit.body_filter_args.path, "shadow", "path")
  eq(kit.body_filter_args.header, "X-Dark-Canary-Id", "header defaulted")
end)

-- --- log ---------------------------------------------------------------------

test("nothing is shipped when the kit produced no capture", function()
  kit.capture_result = nil
  dc.log()
  eq(kit.shipped, nil, "shipped")
end)

-- Which side of the mirror a capture came from is the one field the kit cannot
-- know, and the collector cannot pair without it.
test("the capture is stamped with its side and shipped to the collector", function()
  kit.capture_result = { correl_id = "abc123", status = 200 }
  dc.log({ path = "shadow" })
  eq(kit.shipped ~= nil, true, "something was shipped")
  eq(kit.shipped.capture.path, "shadow", "capture.path")
  eq(kit.shipped.capture.correl_id, "abc123", "the kit's fields survive")
  eq(kit.shipped.collector.port, 8099, "collector.port")
end)

test("the primary stamps its own side too", function()
  kit.capture_result = { correl_id = "x" }
  dc.log()
  eq(kit.shipped.capture.path, "primary", "capture.path")
end)

-- --- report ------------------------------------------------------------------

io.write(string.format("\n%d tests, %d failed\n", count, #failures))
for _, f in ipairs(failures) do io.write("  ", f, "\n") end
os.exit(#failures == 0 and 0 or 1)
