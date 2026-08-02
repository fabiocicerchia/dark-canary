-- dark-canary capture hook.
--
-- Thin by design: everything about *whether* to mirror — the kill switch,
-- reads-only, sampling, the correlation id, PII scrubbing — lives in the
-- nginx-lua-waf-kit `mirror` module, because that hook is useful on its own and
-- building it there is what made this project cheaper.
--
-- What is here is the part that is dark-canary's and nobody else's: labelling
-- which side of the mirror a capture came from, and shipping it to the
-- collector.
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
  collector = { host = "127.0.0.1", port = 8099, path = "/captures", timeout = 1000 },
  header    = "X-Dark-Canary-Id",
}

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
    end
    return ngx.ctx.dc_mirror
  end
  return kit.decide(o)
end

function _M.body_filter(opts)
  return kit.body_filter(merge(opts))
end

function _M.log(opts)
  local o = merge(opts)
  local capture = kit.capture(o)
  if not capture then return end
  capture.path = o.path -- the one field the kit cannot know
  kit.ship(capture, o.collector)
end

_M.DEFAULTS = DEFAULTS

return _M
