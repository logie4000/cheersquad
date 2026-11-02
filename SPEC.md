# Marathon Spectator Planner: Spec

## Goal
Given a marathon course (GPX), a runner's expected pace, and a spectator's start point and travel mode, compute the sequence of viewing spots that lets the spectator see the runner as many times as possible (optionally weighted toward late miles).

## Non-goals (v1)
- Live runner tracking
- Accounts, saved plans, payments
- Multi-runner optimization

## Inputs
- GPX file (track points with lat/lon)
- Runner: goal finish time or pace, optional split profile (e.g. negative/positive split), start delay (wave/chip offset), pace uncertainty (+/- %)
- Spectator: start location and time, travel mode (walk, bike, transit, drive), min buffer per stop (default 3 min)
- Optional: pinned spots, excluded spots, road closures / allowed crossing points, per-mile weights

## Outputs
- Ordered list of stops: location, course mile, runner earliest/latest pass time, spectator arrival time, expected wait
- Travel legs between stops (mode, duration)
- Total runner sightings and total weighted value

## Modules
1. **gpx**: parse track, compute cumulative distance (haversine), optional calibration to certified course length / mile markers.
2. **pace**: build T(d), the runner's time at distance d. Produce t_fast(d) and t_slow(d) from the uncertainty band.
3. **candidates**: generate spots every ~300 m along the course, plus pinned spots; drop excluded ones. Out-and-back or loop courses yield separate nodes for the same physical spot at different times.
4. **travel**: provider interface `travel(a, b, mode) -> seconds`.
   - v1: haversine distance / mode speed (stub, no external calls)
   - v2: OSRM or Valhalla
   - Must support closure overrides: block crossing the course except at user-marked crossing points.
   - Prefilter pairs by straight-line distance / max speed before calling a routing API; cache results.
5. **planner**: the DP below.
6. **api**: `POST /plan` (GPX upload + JSON params) returning the output above.
7. **web UI** (later): map with course, pass-time slider, suggested stops.

## Planner DP
Sort nodes by `t_fast`. `best[k]` is the max total value of a plan whose last stop is `k`.

```
best[k] = w[k] + max(0, max over feasible j->k of best[j])
feasible(start, k): start_time + travel(start, k) + buffer <= t_fast[k]
feasible(j, k): t_fast[j] + travel(j, k) + buffer <= t_fast[k]
```

The spectator leaves j as soon as the runner passes, whenever that is, and a runner who is late at j is late at k too. So the time available for j->k is the stretch between them at the runner's fastest plausible pace, t_fast[k] - t_fast[j]. (An earlier rule, t_slow[j] + travel + buffer <= t_fast[k], counted the uncertainty at both ends; since the window widens with distance it ruled out late-race hops.) Reported leave/arrive/wait times are for a runner on expected pace.

- Include a virtual start node (spectator's start place and time).
- Answer is argmax over `best`; follow `parent` pointers to recover the route.
- Complexity O(n^2), n around 200-400.
- Variants: max-stops cap via `best[k][m]`; per-stop penalty.

## Edge cases to handle
- Start delay and chip vs. gun time
- GPX length differing from certified distance
- Loops and out-and-backs (same spot visited twice)
- Runner faster or slower than the band (show the early/late window)
- Closures making cross-course travel slow or impossible

## Testing
- Synthetic straight course with constant pace: expected stops computable by hand
- Synthetic out-and-back: spectator can see the runner twice at the turnaround region
- Infeasible case: spectator too slow to ever reach a second spot, so the plan is one stop
- Property test: every consecutive pair in a returned plan satisfies the feasibility inequality

## Build order
gpx -> pace -> candidates -> travel (stub) -> planner (with tests) -> api -> real routing provider -> UI
