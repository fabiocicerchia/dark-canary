-- Unit tests for lua/dark_canary.lua.
--
-- Plain asserts, no framework: the module needs three stubs (`ngx`, cjson and
-- the nginx-lua-waf-kit `mirror` module), which is less setup than any test
-- runner would ask for.
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

-- cjson is OpenResty's, so the tests carry their own encoder. Keys are sorted
-- so the payload is byte-stable and can be pinned as a fixture.
local function encode(v)
  local t = type(v)
  if t == "string" then
    return '"' .. v:gsub('[%c"\\]', function(c)
      return string.format("\\u%04x", c:byte())
    end) .. '"'
  elseif t == "number" then
    return v == math.floor(v) and string.format("%d", v) or tostring(v)
  elseif t == "boolean" then
    return tostring(v)
  elseif t == "table" then
    if #v > 0 then
      local parts = {}
      for _, x in ipairs(v) do parts[#parts + 1] = encode(x) end
      return "[" .. table.concat(parts, ",") .. "]"
    end
    local keys = {}
    for k in pairs(v) do keys[#keys + 1] = k end
    table.sort(keys)
    local parts = {}
    for _, k in ipairs(keys) do parts[#parts + 1] = encode(k) .. ":" .. encode(v[k]) end
    return "{" .. table.concat(parts, ",") .. "}"
  end
  error("cannot encode " .. t)
end

-- ngx.encode_base64, in arithmetic so it runs on a Lua 5.1 without bit ops.
local B64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
local function base64(s)
  local out = {}
  for i = 1, #s, 3 do
    local a, b, c = s:byte(i, i + 2)
    local n = a * 65536 + (b or 0) * 256 + (c or 0)
    local quad = {}
    for j = 4, 1, -1 do
      quad[j] = B64:sub(n % 64 + 1, n % 64 + 1)
      n = math.floor(n / 64)
    end
    if not b then quad[3] = "=" end
    if not c then quad[4] = "=" end
    out[#out + 1] = table.concat(quad)
  end
  return table.concat(out)
end

-- Reset to a known world before each test. Anything a test wants to observe
-- (logs, shipped payloads, the args the kit was called with) accumulates here.
local function reset()
  ngx_stub.ctx = {}
  ngx_stub.var = {}
  ngx_stub.logs = {}
  ngx_stub.WARN = "warn"
  ngx_stub.ERR = "err"
  ngx_stub.log = function(level, ...) table.insert(ngx_stub.logs, { level = level, msg = table.concat({ ... }) }) end
  ngx_stub.encode_base64 = base64
  ngx_stub.body_read = false
  ngx_stub.body_data = nil
  ngx_stub.req = {
    read_body = function() ngx_stub.body_read = true end,
    get_body_data = function() return ngx_stub.body_data end,
  }
  -- The timer runs at once and the socket records what it was sent, so a test
  -- sees the exact bytes the collector would.
  ngx_stub.sent = nil
  ngx_stub.connect_ok = true
  ngx_stub.reply = "HTTP/1.1 202 Accepted"
  ngx_stub.timer = { at = function(_, fn) fn(false); return true end }
  ngx_stub.socket = {
    tcp = function()
      return {
        settimeout = function() end,
        connect = function(_, host, port)
          ngx_stub.connected = { host = host, port = port }
          if ngx_stub.connect_ok then return true end
          return nil, "connection refused"
        end,
        send = function(_, parts) ngx_stub.sent = table.concat(parts); return #ngx_stub.sent end,
        receive = function() return ngx_stub.reply end,
        close = function() end,
      }
    end,
  }

  kit.decide_result = true
  kit.decide_args = nil
  kit.body_filter_args = nil
  kit.capture_result = nil
  kit.shipped = nil

  kit.decide = function(o) kit.decide_args = o; return kit.decide_result end
  kit.body_filter = function(o) kit.body_filter_args = o; return "filtered" end
  kit.capture = function(o) kit.capture_args = o; return kit.capture_result end
  kit.DEFAULTS = { max_body = 65536, scrub_fields = {} }
  kit.scrub_body = function(body) return body end
end

_G.ngx = ngx_stub
package.preload["mirror"] = function() return kit end
package.preload["cjson.safe"] = function() return { encode = encode } end

-- What the collector would receive: the request head and the JSON body.
local function sent()
  local raw = ngx_stub.sent
  if not raw then return nil end
  local head, body = raw:match("^(.-)\r\n\r\n(.*)$")
  return head, body
end

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
  eq(ngx_stub.sent, nil, "sent")
end)

-- Which side of the mirror a capture came from is the one field the kit cannot
-- know, and the collector cannot pair without it.
test("the capture is stamped with its side and shipped to the collector", function()
  kit.capture_result = { correl_id = "abc123", path = "primary", status = 200 }
  dc.log({ path = "shadow", collector = { host = "10.0.0.5" } })
  local head, body = sent()
  eq(head ~= nil, true, "something was shipped")
  eq(head:match("^POST /captures HTTP/1.1") ~= nil, true, "request line")
  eq(body:find('"path":"shadow"', 1, true) ~= nil, true, "capture.path")
  eq(body:find('"correl_id":"abc123"', 1, true) ~= nil, true, "the kit's fields survive")
  eq(ngx_stub.connected.port, 8099, "collector.port")
end)

test("the primary stamps its own side too", function()
  kit.capture_result = { correl_id = "x" }
  dc.log()
  local _, body = sent()
  eq(body:find('"path":"primary"', 1, true) ~= nil, true, "capture.path")
end)

-- --- the wire shape ----------------------------------------------------------
--
-- collector.Capture is the other half of this contract: res_body and req_body
-- as base64 (Go's []byte), headers as lists. The kit names the response body
-- `body` and captures no request body, so without this mapping every capture
-- decoded with nothing to compare.

test("the response body is renamed and base64-encoded", function()
  kit.capture_result = { correl_id = "x", body = '{"a":1}' }
  local wire = dc.to_wire(kit.capture_result, { path = "primary" })
  eq(wire.res_body, base64('{"a":1}'), "res_body")
  eq(wire.body, nil, "the kit's key does not reach the wire")
  eq(kit.capture_result.body, '{"a":1}', "the kit's table is not mutated")
end)

test("the request body is captured, scrubbed and base64-encoded", function()
  ngx_stub.body_data = '{"password":"hunter2","q":1}'
  kit.scrub_body = function(body, fields)
    eq(fields[1], "password", "scrub_fields reach the scrubber")
    return (body:gsub("hunter2", "[redacted]"))
  end
  local wire = dc.to_wire({ correl_id = "x" }, { path = "primary", scrub_fields = { "password" } })
  eq(wire.req_body, base64('{"password":"[redacted]","q":1}'), "req_body")
end)

-- The URI path redacts scrub_args; a login form carries the same keys in its
-- body, and scrub_fields defaults to none.
test("scrub_args keys are redacted in a form-encoded request body", function()
  ngx_stub.body_data = "user=ann&Password=hunter2&next=%2F&token="
  local wire = dc.to_wire({}, { path = "primary", scrub_args = { "password", "token" } })
  eq(wire.req_body, base64("user=ann&Password=[redacted]&next=%2F&token=[redacted]"), "req_body")
end)

test("scrub_args keys reach the JSON scrubber too", function()
  ngx_stub.body_data = '{"token":"abc"}'
  local seen
  kit.scrub_body = function(body, fields) seen = fields; return body end
  dc.to_wire({}, { path = "primary", scrub_fields = { "card" }, scrub_args = { "token", "CARD" } })
  eq(#seen, 2, "the two lists are merged without repeats")
  eq(seen[2], "token", "scrub_args key")
end)

test("the kit's default scrub_args apply when none are configured", function()
  kit.DEFAULTS.scrub_args = { "password" }
  ngx_stub.body_data = "password=hunter2"
  local wire = dc.to_wire({}, { path = "primary" })
  eq(wire.req_body, base64("password=[redacted]"), "req_body")
end)

test("a request body over max_body is cut at it", function()
  ngx_stub.body_data = string.rep("x", 10)
  local wire = dc.to_wire({}, { path = "primary", max_body = 4 })
  eq(wire.req_body, base64("xxxx"), "req_body")
end)

-- The scrubber matches a whole "key":"value" pair; a cut through the value
-- first would ship the start of the secret.
test("the request body is scrubbed before it is cut", function()
  ngx_stub.body_data = '{"password":"hunter2"}'
  kit.scrub_body = function(body)
    return (body:gsub('("password":)"[^"]*"', '%1"[redacted]"'))
  end
  local wire = dc.to_wire({}, { path = "primary", max_body = 16 })
  eq(wire.req_body, base64('{"password":"[re'), "req_body")
end)

test("max_body = 0 captures no request body on either side", function()
  ngx_stub.body_data = '{"id":7}'
  local wire = dc.to_wire({}, { path = "shadow", max_body = 0 })
  eq(wire.req_body, nil, "req_body")
  ngx_stub.var["http_x_dark_canary_id"] = "abc123"
  dc.decide({ path = "shadow", max_body = 0 })
  eq(ngx_stub.body_read, false, "read_body called")
end)

test("no request body is no req_body, not an empty one", function()
  local wire = dc.to_wire({}, { path = "primary" })
  eq(wire.req_body, nil, "req_body")
end)

-- ngx gives a string for a header sent once; Go refuses a string where it
-- wants []string, and refuses the whole capture with it.
test("headers are always lists of values", function()
  local wire = dc.to_wire({
    req_headers = { accept = "application/json", cookie = { "a=1", "b=2" } },
    res_headers = { ["content-type"] = "application/json" },
  }, { path = "primary" })
  eq(wire.req_headers.accept[1], "application/json", "single value")
  eq(#wire.req_headers.cookie, 2, "repeated header kept whole")
  eq(wire.res_headers["content-type"][1], "application/json", "response header")
end)

test("the shadow reads the request body so it can be captured", function()
  ngx_stub.var["http_x_dark_canary_id"] = "abc123"
  dc.decide({ path = "shadow" })
  eq(ngx_stub.body_read, true, "read_body called")
end)

-- --- authentication ----------------------------------------------------------

test("the collector token is sent when configured", function()
  kit.capture_result = { correl_id = "x" }
  dc.log({ collector = { token = "s3cret" } })
  local head = sent()
  eq(head:find("\r\nX-Dark-Canary-Token: s3cret\r\n", 1, true) ~= nil, true, "token header")
end)

test("no token configured sends no token header", function()
  kit.capture_result = { correl_id = "x" }
  dc.log()
  local head = sent()
  eq(head:find("X-Dark-Canary-Token", 1, true), nil, "token header")
end)

test("a token with a line break is refused rather than sent", function()
  kit.capture_result = { correl_id = "x" }
  dc.log({ collector = { token = "s3cret\r\nX-Evil: 1" } })
  eq(ngx_stub.sent, nil, "sent")
  eq(ngx_stub.logs[1].level, "err", "log level")
end)

test("an unreachable collector is logged, not raised", function()
  ngx_stub.connect_ok = false
  kit.capture_result = { correl_id = "x" }
  dc.log()
  eq(ngx_stub.sent, nil, "sent")
  eq(ngx_stub.logs[1].level, "warn", "log level")
end)

test("a capture the collector refuses is logged", function()
  ngx_stub.reply = "HTTP/1.1 401 Unauthorized"
  kit.capture_result = { correl_id = "x" }
  dc.log()
  eq(ngx_stub.logs[1].msg, "dark-canary: collector refused capture: HTTP/1.1 401 Unauthorized", "log")
end)

test("an accepted capture logs nothing", function()
  kit.capture_result = { correl_id = "x" }
  dc.log()
  eq(#ngx_stub.logs, 0, "logs")
end)

-- --- the seam ----------------------------------------------------------------
--
-- The payloads below are pinned in lua/testdata/ and decoded by the collector's
-- own handler in cmd/dark-canary/edge_hook_test.go, so a change on either side
-- of the seam fails a test on that side. The capture table is the exact shape
-- nginx-lua-waf-kit's mirror.capture() returns.
--
--   DC_UPDATE_FIXTURES=1 lua lua/dark_canary_test.lua   # after a deliberate change

local function kit_capture(body)
  return {
    correl_id   = "edge-1",
    path        = "primary",
    method      = "GET",
    uri         = "/orders?id=7&token=[redacted]",
    status      = 200,
    req_headers = { accept = "application/json", ["x-dark-canary-id"] = "edge-1" },
    res_headers = { ["content-type"] = "application/json", vary = { "Accept", "Origin" } },
    body        = body,
    truncated   = false,
    latency_ms  = 12,
    at          = 1700000000,
  }
end

local function fixture(name, got)
  local file = "lua/testdata/" .. name
  if os.getenv("DC_UPDATE_FIXTURES") == "1" then
    local f = assert(io.open(file, "w"))
    f:write(got, "\n")
    f:close()
  end
  local f = io.open(file, "r")
  eq(f ~= nil, true, file .. " exists")
  local want = f:read("*a"):gsub("\n$", "")
  f:close()
  eq(got, want, file)
end

test("the primary payload matches the fixture the collector decodes", function()
  ngx_stub.body_data = '{"id":7}'
  kit.capture_result = kit_capture('{"total":10,"ok":true}')
  dc.log({ collector = { token = "s3cret" } })
  local _, body = sent()
  fixture("edge_primary.json", body)
end)

test("the shadow payload matches the fixture the collector decodes", function()
  ngx_stub.body_data = '{"id":7}'
  kit.capture_result = kit_capture('{"total":11,"ok":true}')
  dc.log({ path = "shadow", collector = { token = "s3cret" } })
  local _, body = sent()
  fixture("edge_shadow.json", body)
end)

-- --- report ------------------------------------------------------------------

io.write(string.format("\n%d tests, %d failed\n", count, #failures))
for _, f in ipairs(failures) do io.write("  ", f, "\n") end
os.exit(#failures == 0 and 0 or 1)
