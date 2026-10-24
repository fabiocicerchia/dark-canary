-- dark-canary capture hook.
--
-- Thin by design: everything about *whether* to mirror — the kill switch,
-- reads-only, sampling, the correlation id, PII scrubbing — lives in the
-- nginx-lua-waf-kit `mirror` module, because that hook is useful on its own and
-- building it there is what made this project cheaper.
--
-- What is here is the part that is dark-canary's and nobody else's: labelling
-- which side of the mirror a capture came from, putting it in the shape the
-- collector decodes, and shipping it there with the collector's token.
--
-- The file is `dark_canary.lua`, not `mirror.lua`, so it cannot shadow the kit
-- module it requires. Install the kit and point lua_package_path at both.
--
--   lua_package_path "/opt/nginx-lua-waf-kit/lua/?.lua;/opt/dark-canary/lua/?.lua;;";
--
-- Primary server (the one that answers the user):
--   set $dc_mirror 0;
--   access_by_lua_block      { require("dark_canary").decide() }
--   body_filter_by_lua_block { require("dark_canary").body_filter() }
--   log_by_lua_block         { require("dark_canary").log() }
--
-- Shadow server (the dead end): the same three lines with { path = "shadow" }.
-- The correlation header arrives on the mirrored subrequest, so the shadow does
-- not need to generate anything.

local kit = require("mirror")

local _M = { _VERSION = "0.1.0" }

local DEFAULTS = {
  path      = "primary",                    -- or "shadow", on the shadow server
  collector = { host = "127.0.0.1", port = 8099, path = "/captures", timeout = 1000,
                -- The collector's -token. Required unless it listens on loopback,
                -- where it accepts captures without one.
                token = nil,
                -- TLS to whatever terminates it in front of the collector. The
                -- certificate is verified against lua_ssl_trusted_certificate,
                -- for server_name (default: host).
                tls = false, server_name = nil },
  header    = "X-Dark-Canary-Id",
}

local TOKEN_HEADER = "X-Dark-Canary-Token"

local function merge(opts)
  opts = opts or {}
  local o = {}
  for k, v in pairs(DEFAULTS) do o[k] = v end
  for k, v in pairs(opts) do o[k] = v end
  -- collector is the one nested table, and overriding a single field of it
  -- ({ collector = { host = ... } }) is the normal case. A shallow copy would
  -- drop port/path/timeout and ship the captures nowhere. Copying it always
  -- also keeps callers from mutating DEFAULTS through the returned table.
  o.collector = {}
  for k, v in pairs(DEFAULTS.collector) do o.collector[k] = v end
  for k, v in pairs(opts.collector or {}) do o.collector[k] = v end
  return o
end

local function max_body(o)
  return o.max_body or kit.DEFAULTS.max_body
end

-- On the primary this decides and generates the correlation id. On the shadow
-- the decision was already made upstream — the request only exists because the
-- primary mirrored it — so capture unconditionally and adopt the id it carries.
function _M.decide(opts)
  local o = merge(opts)
  if o.path == "shadow" then
    ngx.ctx.dc_mirror = true
    ngx.ctx.dc_correl_id = ngx.var["http_" .. o.header:lower():gsub("-", "_")]
    if not ngx.ctx.dc_correl_id then
      -- No correlation id means nothing to pair this with. Capturing it would
      -- only fill the collector with orphans.
      ngx.ctx.dc_mirror = false
      ngx.log(ngx.WARN, "dark-canary: shadow request without a correlation id")
    elseif max_body(o) > 0 then
      -- The kit reads the body on the primary; nothing does here, and an
      -- unread body is one get_body_data() cannot return. max_body = 0 turns
      -- body capture off on both sides alike.
      ngx.req.read_body()
    end
    return ngx.ctx.dc_mirror
  end
  return kit.decide(o)
end

function _M.body_filter(opts)
  return kit.body_filter(merge(opts))
end

-- The collector decodes headers as lists of values, but ngx returns a string
-- for a header sent once and a table only for a repeated one. One string is
-- enough to make the whole capture fail to decode.
local function as_lists(headers)
  local out = {}
  for name, value in pairs(headers or {}) do
    out[name] = type(value) == "table" and value or { tostring(value) }
  end
  return out
end

-- The kit redacts scrub_args only in the query string, and its body scrubber
-- only the JSON keys in scrub_fields, which default to none. A request body is
-- where a login form puts password=..., so both lists apply to it, in both
-- encodings; otherwise the secret the URI path redacts ships here in clear.
local function secret_keys(o)
  local keys, set = {}, {}
  local lists = { o.scrub_fields or kit.DEFAULTS.scrub_fields, o.scrub_args or kit.DEFAULTS.scrub_args }
  for _, list in ipairs(lists) do
    for _, key in ipairs(list or {}) do
      if not set[key:lower()] then
        set[key:lower()] = true
        keys[#keys + 1] = key
      end
    end
  end
  return keys, set
end

-- A form key as the application will read it: `pass%77ord` and `pass+word`
-- arrive as `password` and `pass word`, so they are compared decoded.
local function form_decode(key)
  return (key:gsub("+", " "):gsub("%%(%x%x)", function(hex) return string.char(tonumber(hex, 16)) end))
end

-- The leading "&" lets the first pair match the same pattern as the rest. The
-- key is kept as written; only the value is replaced.
local function scrub_form(body, set)
  return (("&" .. body):gsub("&([^=&]+)=[^&]*", function(key)
    if set[form_decode(key):lower()] then return "&" .. key .. "=[redacted]" end
  end)):sub(2)
end

-- The request body, as far as the edge can see it. A body nginx spooled to a
-- temp file is not read back: that is blocking I/O in the log phase.
--
-- Scrubbed before it is cut: the scrubber matches a whole "key":"value" pair,
-- so a cut through a value would leave the start of that secret unredacted.
--
-- A multipart body is not shipped at all. It carries the same form fields as a
-- urlencoded one, but inside parts that neither scrubber reads, so a login or
-- upload form would send its password= to the collector in clear.
local function request_body(o)
  local body = ngx.req.get_body_data()
  if not body or max_body(o) <= 0 then return nil end
  if (ngx.var.content_type or ""):lower():find("^%s*multipart/") then return nil end
  local keys, set = secret_keys(o)
  body = scrub_form(kit.scrub_body(body, keys), set)
  return body:sub(1, max_body(o))
end

-- The kit's capture table is shared with its other consumers, so it keeps its
-- own shape and this maps it onto collector.Capture: the response body is
-- `res_body` there, the request body is not captured at all, and both are
-- []byte, which encoding/json reads only from base64.
function _M.to_wire(capture, o)
  local wire = {}
  for k, v in pairs(capture) do wire[k] = v end
  wire.path = o.path -- the one field the kit cannot know
  wire.body = nil
  wire.req_headers = as_lists(capture.req_headers)
  wire.res_headers = as_lists(capture.res_headers)
  wire.res_body = capture.body and ngx.encode_base64(capture.body) or nil
  local req = request_body(o)
  wire.req_body = req and ngx.encode_base64(req) or nil
  return wire
end

-- One POST to the collector, from inside a timer. The status line is read
-- because a wrong or missing token is a 401 on every capture; unread, the edge
-- would look healthy while the collector receives nothing.
local function post(collector, auth, payload)
  local sock = ngx.socket.tcp()
  sock:settimeout(collector.timeout or 1000)
  local ok, cerr = sock:connect(collector.host, collector.port)
  if not ok then
    ngx.log(ngx.WARN, "dark-canary: collector unreachable: ", cerr)
    return
  end
  if collector.tls then
    -- Verified, always: an unverified handshake would hand the token to
    -- whoever answers, which is the exposure TLS is here to close.
    local _, herr = sock:sslhandshake(nil, collector.server_name or collector.host, true)
    if herr then
      ngx.log(ngx.WARN, "dark-canary: collector TLS handshake failed: ", herr)
      sock:close()
      return
    end
  end
  local sent, serr = sock:send({
    "POST ", collector.path or "/captures", " HTTP/1.1\r\n",
    "Host: ", collector.host, "\r\n",
    "Content-Type: application/json\r\n",
    "Content-Length: ", #payload, "\r\n",
    auth,
    "Connection: close\r\n\r\n",
    payload,
  })
  if not sent then
    ngx.log(ngx.WARN, "dark-canary: could not send capture: ", serr)
  else
    local status = sock:receive("*l")
    if not (status and status:match("^HTTP/%d%.%d 202 ")) then
      ngx.log(ngx.WARN, "dark-canary: collector refused capture: ", status or "no response")
    end
  end
  sock:close()
end

-- The kit's ship() sends no headers of its own, and the collector refuses a
-- capture without its token anywhere but loopback. Same transport otherwise:
-- a cosocket from a timer, so the response is already on its way back.
function _M.ship(capture, collector)
  local token = collector.token or ""
  if token:find("[\r\n]") then
    -- A line break would end the header and start one the operator never wrote.
    ngx.log(ngx.ERR, "dark-canary: collector.token contains a line break; not shipping")
    return
  end
  local auth = token ~= "" and (TOKEN_HEADER .. ": " .. token .. "\r\n") or ""
  local payload = require("cjson.safe").encode(capture)
  if not payload then return end
  local queued, err = ngx.timer.at(0, function(premature)
    if not premature then post(collector, auth, payload) end
  end)
  if not queued then
    ngx.log(ngx.WARN, "dark-canary: could not queue capture: ", err)
  end
end

function _M.log(opts)
  local o = merge(opts)
  local capture = kit.capture(o)
  if not capture then return end
  _M.ship(_M.to_wire(capture, o), o.collector)
end

_M.DEFAULTS = DEFAULTS

return _M
