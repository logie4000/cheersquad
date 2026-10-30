# Street routing: Google, or keep our own?

Rob asked whether Google's routing API would be better than running OSRM
routers on denali. This is the plan for a hybrid ("plan cheaply, route only
the final legs"), what it would take with Google, and what else could do the
same job. Prices and terms were checked on 2026-10-30.

## Short answer

Don't use Google here. The hybrid idea is good, but Google's terms forbid
showing Routes API results with a non-Google map, so using it means replacing
Leaflet/OSM with a Google map and reworking Print and Export PDF. That's most
of the cost, and it adds a per-plan bill.

Build the hybrid with an OSM-based router instead (OpenRouteService's free
tier), which draws on the map we already have. That takes the routers off
denali. For the upcoming NYC race, keep the NYC-only OSRM build from PR #26;
nothing here needs to land first.

## How the hybrid works

Today the planner asks for travel times between up to ~2M spot pairs per
plan. With OSRM configured, walk/bike/drive times come from the router (the
`Prefetcher` warms up to 1,500 points with `/table` requests); otherwise from
the straight-line stub (`travel.Haversine`: distance × 1.3 ÷ mode speed).

The hybrid splits that in two:

1. **Plan with estimates.** The planner uses the straight-line stub for every
   pair, as it does with no router today. No network calls in the DP.
2. **Route the final legs.** For each leg of the chosen plan, ask the router
   for a street route: duration and path. That's the `Steps` call that already
   exists per leg (`travel.OSRM.Steps`), so a new provider slots in behind the
   same interface.
3. **Correct and re-plan (bounded).** If a routed leg is slower than its
   estimate, the plan may no longer work (you'd arrive after the runner
   passed). So pin the routed times for those pairs, re-solve, and route any
   new legs the new plan uses. Stop when the plan only uses routed legs, or
   after 3 rounds. The loop lives in `internal/api`; the planner stays pure
   (it just gets a provider that returns pinned times where known and
   estimates elsewhere).

Every leg in the returned plan is then a routed leg, so the plan is feasible
on real streets. It isn't guaranteed optimal: a pair the estimate made look
slow is never checked. In practice the stub's 1.3 detour is a fair fit for a
grid (the average grid detour is 4/π ≈ 1.27), and the misses are near rivers,
parks and bridges, which is exactly where the correction loop kicks in.

Calls per plan: one per leg, two for a leg detoured through a crossing point,
two for a transit leg (walk to the station, walk from it). A typical plan has
4 to 8 stops, so about 10 calls, and about 15 with the correction rounds.

## Doing it with Google

**API.** Routes API `computeRoutes` (the legacy Directions API has the same
terms and price). Travel modes WALK, BICYCLE, DRIVE, with a field mask for
`duration`, `distanceMeters` and `polyline`. Without traffic options it bills
as *Compute Routes Essentials*. `TRAFFIC_AWARE` bills as *Pro* at twice the
price; race-day closures make live traffic of little use anyway.

**Price** (Essentials): 10,000 free requests a month, then $5 per 1,000
(down to $4 above 100k).

| Plans a month | Routing calls (~15/plan) | Routes cost |
| --- | --- | --- |
| 500 | 7,500 | $0 |
| 2,000 | 30,000 | ~$100 |
| 10,000 (a big race week) | 150,000 | ~$660 |

Plus the map, because of the terms below: Dynamic Maps is 10,000 free loads a
month, then $7 per 1,000; Static Maps (for the PDF) 10,000 free, then $2 per
1,000.

**Terms (the blocker).** The Maps Service Specific Terms, section 19 (Routes
API), say: "Customer must not use Google Maps Content from the Routes API in
conjunction with a non-Google map." Drawing Google routes on our Leaflet/OSM
map is out, and so, arguably, is using Google times to choose spots that we
then show on OSM. Results may be used with no map at all, but CheerSquad is a
map. To comply we would:

- Replace Leaflet + OSM tiles with the Google Maps JavaScript API, and redraw
  the course, spots, subway lines and leg paths with it.
- Rework Print (a printed Google map with its attribution intact is fine).
- Rework Export PDF: it draws OSM tiles onto a canvas today. With Google it
  would need a Static Maps image with the course and legs as encoded paths,
  which caps how much we can draw (URL length).
- Show "Google Maps" attribution wherever results appear.

**Caching.** Only latitude/longitude values may be cached, for up to 30 days.
Our in-memory per-request use is fine; a long-lived shared cache of routes
like the OSRM memo is not.

**API keys.** Two keys: a server key for Routes in an env var
(`GOOGLE_ROUTES_API_KEY` in denali's `.env`, never in the repo or image),
restricted to Routes API and denali's IP; and a browser key for the map,
restricted by HTTP referrer to `www.teapothill.org`. Set a daily quota cap on
Routes (e.g. 3,000 requests) and a budget alert in Cloud Console so a bot
can't run up a bill; Nginx's 6 plans/min per IP helps but isn't a cap.

## Transit

Unchanged in every option. Subway/rail times stay on our GTFS data; only the
walking parts are street-routed. Google's own transit routing would include
buses (rerouted on race day) and can't be restricted to our lines.

## Print and PDF

With an OSM-based router: nothing changes. The routed paths are already drawn
on the map, printed, and exported (PR #23). With Google: see the terms above.

## Alternatives

| Option | Cost | On our OSM map? | Runs on denali? | Notes |
| --- | --- | --- | --- | --- |
| Google Routes API | ~$0.07/plan after 10k free calls/mo, plus map loads | No (terms) | No | Best data, but forces a Google map |
| Mapbox Directions | 100k requests/mo free, then $2 per 1,000 | Not forbidden as far as I found | No | Product terms forbid caching or storing results; 100k free covers ~6,500 plans/mo |
| OpenRouteService (hosted) | Free: 2,000 directions/day, 40/min | Yes (OSM data) | No | Foot, bike, car profiles. Needs a free key and ORS + OSM attribution. ~130 plans/day before falling back to straight line |
| OSRM self-hosted, NYC only (today) | Free | Yes | Yes, ~1+ GB RAM per profile | What PR #26 set up. Could run walk only to save memory, since walking and transit are the common modes |
| OSRM on another box | A small instance (~$15-30/mo) | Yes | No | Keeps today's full-matrix accuracy; one more machine to look after |

## Recommendation

1. **Now (NYC race):** keep the NYC-only OSRM build from PR #26. If denali's
   memory is still tight, start only the foot router.
2. **Next:** build the hybrid (estimate, route final legs, bounded re-plan)
   behind the existing `Provider`/`Steps` interface, backed by
   OpenRouteService. It works in every city without building data, and lets
   denali drop the routers. When ORS's daily limit runs out or it's down,
   legs fall back to straight line and the routing label says so, like OSRM
   today.
3. **Keep OSRM as an option**, for a city where full-matrix accuracy matters
   or for a box with memory to spare. The re-plan loop also works with it.
4. **Skip Google** unless we're willing to move the whole UI to a Google map.

Rough size of step 2: an ORS client in `internal/travel` (like `osrm.go`,
mostly `Steps` and an HTTP client) with tests against a fake server; the
re-plan loop in `internal/api` with tests on small fixtures; `ORS_API_KEY` in
deploy config and docs; attribution in the map footer and PDF. No UI work
beyond attribution.

## Sources

- [Google Maps Platform pricing](https://developers.google.com/maps/billing-and-pricing/pricing)
- [Routes API usage and billing](https://developers.google.com/maps/documentation/routes/usage-and-billing) (Essentials vs Pro)
- [Google Maps Service Specific Terms](https://cloud.google.com/maps-platform/terms/maps-service-terms/) (section 19, Routes API)
- [Routes API policies](https://developers.google.com/maps/documentation/routes/policies) (attribution)
- [OpenRouteService plans](https://openrouteservice.org/plans/)
- [Mapbox pricing summary](https://storerocket.io/learn/mapbox-pricing) and [Mapbox Product Terms](https://www.mapbox.com/legal/product-terms)
