# Scaling CheerSquad

A plan for running more than one CheerSquad container, written before it's
needed. Today one container is plenty; this says what to watch, what to do
first, and what to change when one isn't.

## Where we are

- One container (`cheersquad`) on denali, published on `127.0.0.1:8085`,
  behind the host Nginx at `/cheersquad/`.
- The app is stateless between requests. The only in-process state is:
  - the GTFS networks, loaded once at startup from the read-only
    `/data/gtfs` mount (built three times per feed, one per race-day
    timetable);
  - the optional OSRM walking-time memo, which is just a cache.
  So any container can answer any request, and no sticky sessions or shared
  store are needed.
- Measured capacity (2026-10-28, NYC subway loaded, 2 cores): about 110
  plans/s for ordinary plans (4-20 ms each). Plans with crossing points are
  the expensive ones: 3 crossings take about 0.5 s, 10 about 1.2 s.
- Memory: about 65 MB idle and 105 MB peak with one city's feed. More feeds
  (London is the big one) add to the idle figure, per container.
- Limits per container (`internal/api/api.go`): 4 plans at once, at most 2
  of them with crossings, 32 more waiting up to 10 s, then a 503.
- Limits in front (Nginx): 6 plans a minute per client IP with a burst of 5,
  answered with a 429.

Even a busy race week is unlikely to come close to 110 plans/s. The first
thing to run out will be the crossing-plan slots, not the box.

## When to scale

Scaling is worth it when real users are getting turned away or kept waiting,
not when the box looks busy. Watch these, in order of how directly they mean
"not enough capacity":

| Signal | Where to see it | Act when |
| --- | --- | --- |
| 503s from `/cheersquad/api/plan` (the queue was full or a slot didn't free up in 10 s) | Nginx access log, status 503 | More than a handful an hour during a busy period, or any sustained run of them |
| Slow plans | `$upstream_response_time` on `/api/plan` (log format below) | p95 above 2 s, or anything near 10 s (that's time spent waiting in line) |
| CPU | `docker stats cheersquad`, `top` | Above about 70% for minutes at a time while plans are coming in |
| Memory | `docker stats cheersquad` | Above about 60% of `mem_limit`, or the host itself short on memory |

429s are not a capacity signal: they mean one client IP asked too often.
Lots of them from many IPs could mean the per-IP limit is too tight for
shared IPs (an office, a running club on one Wi-Fi), which is a config
change in `cheersquad.http.conf`, not more containers.

To get the latency number, add a CheerSquad log format and use it in the
two `/cheersquad/` locations. `log_format` must go in the `http { }` context,
so it belongs in `cheersquad.http.conf`:

```nginx
log_format cheersquad '$remote_addr [$time_local] "$request" $status '
                      '$body_bytes_sent rt=$request_time urt=$upstream_response_time '
                      'up=$upstream_addr';
```

```nginx
# inside both /cheersquad/ locations
access_log /var/log/nginx/cheersquad.access.log cheersquad;
```

Then, for example:

```sh
# plan requests and 503s today
grep 'POST /cheersquad/api/plan' /var/log/nginx/cheersquad.access.log | awk '{print $6}' | sort | uniq -c
# slowest plan responses
grep 'api/plan' /var/log/nginx/cheersquad.access.log | grep -o 'urt=[0-9.]*' | sort -t= -k2 -n | tail
```

Traffic will be spiky: the week before a big marathon, and the evening
before it. Scaling up ahead of a known race (say, the Thursday before NYC)
is cheaper and safer than reacting on race morning.

## Step 0: tune the one container

On a single host, a second container doesn't add CPU. Go already spreads
work over every core, so the first step is to let one container use them:

- **Raise the plan slots** to roughly the core count (and the crossing slots
  to half that), so plans aren't waiting in line while cores sit idle. Today
  these are constants; make them settable (see "Code changes").
- **Give the container a CPU cap** (`cpus:` in compose) so it can't starve
  Rob's other sites, and set `GOMAXPROCS` to match. Go 1.25 reads the cgroup
  CPU limit itself; on Go 1.23/1.24, set `GOMAXPROCS` in the environment.
- **A bigger instance** if denali is genuinely CPU-bound across all its
  sites. That's one AWS console change and helps everything on the box.

This covers a lot more traffic than CheerSquad is likely to see.

## Step 1: two or more containers on denali

The reasons to run several containers on one host are availability, not
raw speed:

- **Deploys and restarts without downtime.** Today a deploy or a GTFS
  refresh means the site is down while the new container loads its feeds
  (which takes a while with London). With two, one keeps serving.
- **Crash isolation.** A bad request that kills one container leaves the
  other serving.
- It's also the shape that carries over to several hosts later.

So this step is worth doing once CheerSquad matters enough that a minute of
downtime during a race week is a problem, even before traffic demands it.

### Compose

`--scale` / `deploy.replicas` don't fit well: they need the fixed
`container_name` removed and a port range, and Nginx would have to guess
which port each replica got. Explicit services are clearer and match how
the host Nginx already addresses containers by port. Put the second one in
a production overlay so local `docker compose up` stays a single container:

```yaml
# deploy/docker-compose.replicas.yml
services:
  cheersquad-2:
    extends:
      file: ../docker-compose.yml
      service: cheersquad
    container_name: cheersquad-2
    ports: !override
      - "127.0.0.1:8086:8080"
```

(`!override` needs Compose 2.24 or later; check with `docker compose
version`.) A third would be `cheersquad-3` on 8087. Both share the same
read-only `./gtfs` mount, so feeds are fetched once.

Each replica also wants:

```yaml
    mem_limit: 1g              # per replica; see the memory budget below
    cpus: "1.0"                # or more, as long as the sum leaves room for the other sites
    stop_grace_period: 45s     # let in-flight plans finish (queue wait + plan timeout)
```

### Nginx

Add an upstream (it must be in `http { }`, so in `cheersquad.http.conf`) and
point both locations at it instead of `127.0.0.1:8085`:

```nginx
upstream cheersquad {
    least_conn;                                   # plans vary a lot in cost
    server 127.0.0.1:8085 max_fails=1 fail_timeout=10s;
    server 127.0.0.1:8086 max_fails=1 fail_timeout=10s;
    keepalive 16;
}
```

```nginx
location /cheersquad/ {
    proxy_pass http://cheersquad/;
    proxy_set_header Connection "";               # needed for upstream keepalive
    proxy_next_upstream error timeout http_502;
    # ...the rest as today
}

location = /cheersquad/api/plan {
    limit_req zone=cheersquad_plan burst=5 nodelay;
    limit_req_status 429;
    proxy_pass http://cheersquad/api/plan;
    proxy_set_header Connection "";
    # A plan is pure (same input, same answer), so it is safe to retry on the
    # other replica if this one is down or its queue is full.
    proxy_next_upstream error timeout http_502 http_503 non_idempotent;
    proxy_next_upstream_tries 2;
    proxy_next_upstream_timeout 15s;
    # ...the rest as today
}
```

- `least_conn` sends a new plan to the replica with fewer in flight, which
  suits plans that range from 5 ms to over a second.
- A replica that is restarting refuses connections while it loads feeds
  (the server only listens once loading is done), so Nginx skips it and
  tries the other. That's what makes deploys seamless.
- Retrying a 503 lets a request that hit a full queue on one replica try the
  other. The 15 s cap keeps a retried request inside the 60 s
  `proxy_read_timeout`.
- **The rate limit needs no change.** `limit_req` lives in Nginx and counts
  per client IP across everything behind it, so it already applies to the
  site as a whole. The per-container gate is what grows: total concurrency
  becomes replicas x plan slots.

### Memory budget

Each replica loads every GTFS feed into its own memory, so memory grows
linearly with replicas. Before adding one, check the real figure with every
feed loaded:

```sh
docker stats --no-stream cheersquad
free -m
```

Budget roughly peak RSS x replicas, plus headroom, and make sure the sum of
`mem_limit`s plus Rob's other sites fits in the instance's RAM. The current
2 GB limit is a ceiling for a runaway request, not an expected size; with
several replicas, 1 GB each is a better ceiling unless measured peaks say
otherwise. If feeds grow enough to make this painful, only load the cities
with preloaded races, or split cities across replicas (see "Later").

### Deploy and rollout

`deploy.sh` changes from "restart the container" to a rolling update, one
replica at a time:

```sh
services=(cheersquad cheersquad-2)          # service = container name here
dc=(docker compose -f docker-compose.yml -f deploy/docker-compose.prod.yml \
    -f deploy/docker-compose.replicas.yml --env-file .env)
"${dc[@]}" pull
for s in "${services[@]}"; do
  "${dc[@]}" up -d --no-deps "$s"
  wait_healthy "$s" || exit 1   # today's health loop, per container; stop here and the rest keep the old image
done
```

If a new image fails its health check on the first replica, the script stops
there and the other replicas keep serving the old version; rolling back is
the same `IMAGE_TAG=<sha> deploy/scripts/deploy.sh` as today. The health
wait should allow for the GTFS load time (today it's 60 s, which may be
short with London; make it configurable).

A GTFS refresh becomes the same loop: `docker restart` each replica in turn
and wait for healthy, instead of restarting the only one. A
`deploy/scripts/restart.sh` that does just that is worth adding alongside.

## Step 2: more than one host

Only if denali itself is the limit (or its downtime is). In order of effort:

### a. A second EC2 instance behind denali's Nginx

The smallest change: another instance in the same VPC runs the same
compose setup and `deploy.sh`, and denali's upstream gains its private IP:

```nginx
server 10.0.x.y:8085 max_fails=1 fail_timeout=10s;
```

- Publish the port on the private interface, and allow it only from
  denali's security group.
- **GTFS feeds**: the second host needs the same files. Push them to a
  private S3 bucket after fetching (`aws s3 sync /srv/cheersquad/gtfs
  s3://<bucket>/gtfs`) and have `deploy.sh` sync down before starting. This
  also fixes the London problem once: whoever downloads the TfL zip in a
  browser runs the converter once and uploads the result.
- Deploys run on each host in turn.
- Denali's Nginx stays a single point of failure, but it already is for
  every other site.

### b. Load balancer + Auto Scaling group

An Application Load Balancer in front of an Auto Scaling group whose launch
template runs the GTFS sync and `deploy.sh` at boot. This scales on CPU or
request count without anyone logging in, at the cost of an ALB (roughly
$20/month before traffic) and moving CheerSquad off denali's Nginx (the ALB
would need its own hostname or a path rule, and TLS via ACM). Nginx's
`limit_req` would have to be replaced, e.g. with AWS WAF rate rules.

### c. ECS Fargate

The image already runs as-is on ECS. A Fargate service behind an ALB, with
GTFS either baked into a separate image layer (a `cheersquad-gtfs` image
rebuilt when feeds change) or on EFS. The most hands-off option and the
furthest from Rob's current setup; only worth it if CheerSquad outgrows
being one of several apps on one box.

Recommendation: (a) if it ever comes to that. It reuses everything Rob
already runs.

## Code changes

Nothing about the app's design has to change: it's stateless, so replicas
just work. A few small changes make running several of them smoother:

1. **Settable limits.** Read `MaxConcurrentPlans`, `MaxCrossingPlans`,
   `MaxQueuedPlans` and `QueueWait` from the environment (e.g.
   `PLAN_SLOTS`, `CROSSING_SLOTS`, `PLAN_QUEUE`), keeping today's values as
   defaults, so each replica can be sized to its CPU cap without a rebuild.
2. **Graceful shutdown.** `main.go` runs `log.Fatal(srv.ListenAndServe())`,
   so a stop kills in-flight plans. Catch SIGTERM and call
   `srv.Shutdown(ctx)` with a timeout a little under `stop_grace_period`,
   so a rolling deploy doesn't drop the requests a replica is working on.
3. **GOMAXPROCS.** Either move to Go 1.25 (which honors the container's CPU
   limit) or set `GOMAXPROCS` in compose next to `cpus:`, so a capped
   replica doesn't schedule as if it owned every core.
4. **A busy counter (optional).** A log line when a plan gets a 503, with
   the queue length, makes "are we turning people away?" a `docker logs |
   grep` instead of an Nginx log search. Nginx's log covers it well enough
   to start.

Not needed:

- **Shared cache.** The OSRM memo is per replica. Each warms up on its own,
  which only matters if OSRM walking is turned on, and even then the OSRM
  server (one shared container) does the real work.
- **Sticky sessions.** Every request carries its whole GPX and parameters.
- **Readiness endpoint.** The server only starts listening after feeds
  load, so "accepting connections" already means "ready".

## Summary

1. Now: nothing. Optionally add the Nginx log format so the numbers are
   there if they're ever needed.
2. If plans start queueing: make the slot limits settable and raise them to
   the core count; add a CPU cap.
3. When downtime during deploys or GTFS reloads starts to matter: a second
   replica on denali, Nginx upstream, graceful shutdown, rolling
   `deploy.sh`.
4. If denali runs out: a second EC2 host behind the same Nginx, with feeds
   synced from S3.
