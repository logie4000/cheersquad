# Preloaded race courses

Drop course files here as `<slug>.gpx` (for example `city-marathon-2026.gpx`).

- **Id:** the file name without `.gpx` is the race id, used in URLs
  (`GET api/races/<slug>`). Use lowercase letters, digits and hyphens.
- **Dropdown label:** the GPX `<metadata><name>`, falling back to the first
  `<trk><name>`, then the first `<rte><name>`, then the file name.
- **Other files** (like this README) are ignored.
- Files are compiled into the binary with `go:embed`, so adding, changing or
  removing a course needs a rebuild and redeploy.
