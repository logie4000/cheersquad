# CheerSquad web UI

Angular app with a Leaflet map. It talks to the Go API with relative URLs
(`api/plan`), so it works both at the site root and under `/cheersquad/`.

```sh
npm ci
npx ng serve                  # http://localhost:4200, proxies /api to :8080
npx ng test --watch=false     # unit tests (Vitest)
npx ng build                  # output in dist/cheersquad-web/browser
```

Run the API alongside with `go run ./cmd/cheersquad` from the repo root.
