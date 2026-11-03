# CLAUDE.md

Marathon spectator planner. Read SPEC.md for what to build and why; this file covers how to work in the repo.

## Working rules
- Build in the order listed in SPEC.md ("Build order"). Don't start the API or UI before the planner has passing tests.
- Keep the planner pure: no network calls, no file I/O. It takes nodes, a travel function, and parameters, and returns a plan.
- Travel providers sit behind one interface. The haversine stub must always work offline so tests never need external APIs.
- Times are seconds since race start internally. Convert at the edges only.
- Distances are meters internally.

## Conventions
- Write a test with each module before moving on. Use small hand-checkable fixtures (straight course, out-and-back).
- Cache routing results; never call a routing API inside the DP inner loop without the straight-line prefilter.
- Don't add dependencies without saying why.

## Stack
- Backend: Go (standard library `net/http`, no framework). Core modules live in `internal/`: `geo`, `gpx`, `pace`, `candidates`, `travel`, `planner`.
- Frontend: Angular in `web/` with Leaflet and OpenStreetMap tiles. Unit conversion (clock times, miles) happens in `web/src/app/plan.ts`.
- Deploy: one Docker container (Go serves the API and the built UI) under `/srv/cheersquad` on the EC2 host, published on `127.0.0.1:8085`. The host Nginx serves it at `www.specialbrew.net/cheersquad/`, stripping the prefix, so the UI uses a relative base href (`./`) and relative API URLs (`api/plan`, never `/api/plan`).

## Commands
- Run tests: `go test ./...` and `cd web && npx ng test --watch=false`
- Run server: `go run ./cmd/cheersquad` (API on :8080) plus `cd web && npx ng serve` (proxies `/api` to :8080), or `docker compose up --build` for the whole app on 127.0.0.1:8085
- Deploy: CI green on main -> Images workflow pushes `cheersquad-app:<sha>` to ECR -> on the host `deploy/scripts/deploy.sh` (see `deploy/README.md`). Mirrors logie4000/sweat-equity's setup; keep them consistent.
- Lint/format: `gofmt -l . && go vet ./...` and `cd web && npx prettier --check "src/**/*.{ts,html,css}"`

## Open decisions
- Routing (settled for now): transit uses GTFS feeds, subway/metro/light rail lines, plus rail (S-Bahn, RER, commuter rail) only where it runs at least every 15 min on Sunday morning; never buses (rerouted on race day). Light rail is kept for lines like Boston's Green Line, used when the network covers the start or any part of the course, with waits from scheduled frequencies rather than timetables. Walking is a straight-line estimate by default, with an optional self-hosted OSRM foot profile (`OSRM_WALK_URL`). Street routes (hybrid, `travel.Routed`, OpenRouteService via `ORS_API_KEY` from SSM `/webapp/cheer-squad/ORS_API_KEY` at deploy): walk/bike/drive plans time candidate legs up front with the ORS matrix API (a few requests), solve on those, and fetch directions only for the chosen legs; transit and avoid-ferries plans (the matrix can't avoid ferries) solve on estimates, route the chosen legs and re-solve (3 rounds, then up to 3 repair rounds over the plan's own spots). Never call ORS inside the DP. ORS answers are saved to `ORS_CACHE_FILE` (the `ors-cache` Docker volume) and reloaded at startup, kept 180 days. Feeds are fetched on the host with `deploy/scripts/fetch-gtfs.sh` into `/srv/cheersquad/gtfs`, mounted as `GTFS_DIR`; with none covering a course, transit mode plans on foot and says no transit data is available.
- Closures: v1 plan is that users click crossing points on the map, and `travel.Closures` blocks legs that cross the course elsewhere
