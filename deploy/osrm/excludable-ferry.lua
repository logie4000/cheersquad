-- Wraps a stock OSRM profile so ferries can be left out per request
-- (exclude=ferry). The stock car profile already allows it; foot and bicycle
-- don't tag ferries, so build-osrm.sh builds them through this wrapper,
-- naming the stock profile in OSRM_BASE_PROFILE. Excludable classes add
-- one more set of cell metrics to the built data.
package.path = '/opt/?.lua;' .. package.path
local Set = require('lib/set')
local Sequence = require('lib/sequence')
local base = dofile('/opt/' .. os.getenv('OSRM_BASE_PROFILE') .. '.lua')

local wrapped = {}
for k, v in pairs(base) do
  wrapped[k] = v
end

function wrapped.setup()
  local profile = base.setup()
  local classes = profile.classes or Sequence {}
  local has = false
  for _, c in ipairs(classes) do
    if c == 'ferry' then has = true end
  end
  if not has then table.insert(classes, 'ferry') end
  profile.classes = classes
  if not profile.excludable or #profile.excludable == 0 then
    profile.excludable = Sequence { Set { 'ferry' } }
  end
  return profile
end

function wrapped.process_way(profile, way, result, relations)
  base.process_way(profile, way, result, relations)
  if way:get_value_by_key('route') == 'ferry' then
    result.forward_classes['ferry'] = true
    result.backward_classes['ferry'] = true
  end
end

return wrapped
