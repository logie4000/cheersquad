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
- Deploy: one Docker container (Go serves the API and the built UI) under `/srv/cheersquad` on the EC2 host, published on `127.0.0.1:8085`. The host Nginx serves it at `www.teapothill.org/cheersquad/`, stripping the prefix, so the UI uses a relative base href (`./`) and relative API URLs (`api/plan`, never `/api/plan`).

## Commands
- Run tests: `go test ./...` and `cd web && npx ng test --watch=false`
- Run server: `go run ./cmd/cheersquad` (API on :8080) plus `cd web && npx ng serve` (proxies `/api` to :8080), or `docker compose up --build` for the whole app on 127.0.0.1:8085
- Deploy: CI green on main -> Images workflow pushes `cheersquad-app:<sha>` to ECR -> on the host `deploy/scripts/deploy.sh` (see `deploy/README.md`). Mirrors logie4000/sweat-equity's setup; keep them consistent.
- Lint/format: `gofmt -l . && go vet ./...` and `cd web && npx prettier --check "src/**/*.{ts,html,css}"`

## Open decisions
- Routing provider (OSRM self-hosted vs. hosted API)
- Closures: v1 plan is that users click crossing points on the map, and `travel.Closures` blocks legs that cross the course elsewhere
