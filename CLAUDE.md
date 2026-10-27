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

## Commands
Fill these in once the stack is chosen:
- Run tests: `TODO`
- Run server: `TODO`
- Lint/format: `TODO`

## Open decisions
- Language/framework (backend)
- Routing provider (OSRM self-hosted vs. hosted API)
- How users specify closures and crossing points
