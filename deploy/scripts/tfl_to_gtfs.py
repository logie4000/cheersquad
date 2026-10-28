"""Build a GTFS feed of London's rail-like lines from TfL's timetables.

TfL publishes TransXChange (XML), not GTFS. This keeps the Underground, DLR,
trams, Overground and Elizabeth line files, drops buses, coaches and boats,
and converts the rest with transx2gtfs (https://github.com/HTenkanen/transx2gtfs).

    python tfl_to_gtfs.py OUT.zip

The TfL archive is downloaded from TFL_TIMETABLES_URL (default: TfL's
journey planner timetables, which need no key), with TFL_APP_KEY appended
as app_key when set, or read from TFL_TIMETABLES_FILE if that points at a
local copy.
fetch-gtfs.sh runs this inside a python container; see deploy/README.md.
"""

import io
import os
import re
import shutil
import sys
import tempfile
import urllib.error
import urllib.parse
import urllib.request
import zipfile

# TfL's CDN copy is public; the older syndication URL needs app_id and
# app_key and answers 403 without them.
DEFAULT_URLS = [
    "https://tfl.gov.uk/cdn/static/cms/documents/journey-planner-timetables.zip",
    "https://tfl.gov.uk/tfl/syndication/feeds/journey-planner-timetables.zip",
]

# tfl.gov.uk refuses Python's default User-Agent.
USER_AGENT = "cheersquad-fetch-gtfs/1.0 (+https://github.com/logie4000/cheersquad)"

# TransXChange modes worth converting; the server then keeps light rail,
# metro and rail that runs often enough.
KEEP_MODES = {"underground", "metro", "tram", "rail"}

MODE_RE = re.compile(rb"<Mode>\s*([A-Za-z]+)\s*</Mode>")

# TfL line codes as they appear in route codes (e.g. "1-VIC"), with the
# line's name and colour, so the map shows "Victoria" in light blue rather
# than a route code.
TFL_LINES = {
    "BAK": ("Bakerloo", "B36305"),
    "CEN": ("Central", "E32017"),
    "CIR": ("Circle", "FFD300"),
    "DIS": ("District", "00782A"),
    "HAM": ("Hammersmith & City", "F3A9BB"),
    "JUB": ("Jubilee", "A0A5A9"),
    "MET": ("Metropolitan", "9B0056"),
    "NTN": ("Northern", "000000"),
    "PIC": ("Piccadilly", "003688"),
    "VIC": ("Victoria", "0098D4"),
    "WAC": ("Waterloo & City", "95CDBA"),
    "DLR": ("DLR", "00A4A7"),
}


def mode_of(xml):
    """The TransXChange Mode of a file, lower case, or "" if it has none."""
    m = MODE_RE.search(xml)
    return m.group(1).decode().lower() if m else ""


def extract_rail(archive, out_dir, prefix=""):
    """Write the rail-like XML files in archive (and nested zips) to out_dir.
    Returns how many were kept and how many skipped."""
    kept = skipped = 0
    for info in archive.infolist():
        name = info.filename
        lower = name.lower()
        if lower.endswith(".zip"):
            with zipfile.ZipFile(io.BytesIO(archive.read(info))) as inner:
                k, s = extract_rail(inner, out_dir, prefix + os.path.basename(name)[:-4] + "_")
            kept, skipped = kept + k, skipped + s
        elif lower.endswith(".xml"):
            data = archive.read(info)
            if mode_of(data) in KEEP_MODES:
                dest = os.path.join(out_dir, prefix + os.path.basename(name))
                with open(dest, "wb") as f:
                    f.write(data)
                kept += 1
            else:
                skipped += 1
    return kept, skipped


def name_lines(gtfs_zip):
    """Rewrite routes.txt in place: known TfL line codes become line names,
    with the line colour. Unknown routes are left as they are."""
    import csv

    with zipfile.ZipFile(gtfs_zip) as z:
        files = {i.filename: z.read(i) for i in z.infolist()}
    if "routes.txt" not in files:
        return
    rows = list(csv.DictReader(io.StringIO(files["routes.txt"].decode("utf-8-sig"))))
    if not rows:
        return
    fields = list(rows[0].keys())
    if "route_color" not in fields:
        fields.append("route_color")
    named = 0
    for r in rows:
        tokens = re.split(r"[^A-Za-z]+", r.get("route_short_name", "").upper())
        for t in tokens:
            if t in TFL_LINES:
                r["route_short_name"], r["route_color"] = TFL_LINES[t]
                named += 1
                break
    out = io.StringIO()
    w = csv.DictWriter(out, fieldnames=fields, lineterminator="\n")
    w.writeheader()
    w.writerows(rows)
    files["routes.txt"] = out.getvalue().encode()
    tmp = gtfs_zip + ".tmp"
    with zipfile.ZipFile(tmp, "w", zipfile.ZIP_DEFLATED) as z:
        for name, data in files.items():
            z.writestr(name, data)
    os.replace(tmp, gtfs_zip)
    print(f"named {named} of {len(rows)} routes after their TfL line", flush=True)


def download(dest):
    local = os.environ.get("TFL_TIMETABLES_FILE")
    if local:
        shutil.copyfile(local, dest)
        return
    override = os.environ.get("TFL_TIMETABLES_URL")
    urls = [override] if override else DEFAULT_URLS
    key = os.environ.get("TFL_APP_KEY")
    for i, url in enumerate(urls):
        shown = url
        if key and "/cdn/" not in url:
            sep = "&" if urllib.parse.urlparse(url).query else "?"
            url += sep + urllib.parse.urlencode({"app_key": key})
        print("downloading", shown, flush=True)
        req = urllib.request.Request(url, headers={"User-Agent": USER_AGENT})
        try:
            with urllib.request.urlopen(req, timeout=600) as resp, open(dest, "wb") as f:
                shutil.copyfileobj(resp, f)
            return
        except urllib.error.HTTPError as e:
            if i < len(urls) - 1:
                print(f"  {e.code} {e.reason}; trying the next URL", flush=True)
                continue
            if e.code == 403:
                sys.exit(
                    "TfL refused the download (403). Its firewall blocks some cloud "
                    "hosts: download " + DEFAULT_URLS[0] + " in a browser and "
                    "rerun with TFL_TIMETABLES_FILE set to the saved file."
                )
            raise


def main():
    if len(sys.argv) != 2:
        print(__doc__, file=sys.stderr)
        return 2
    out = sys.argv[1]
    import transx2gtfs  # imported late so --help works without it

    with tempfile.TemporaryDirectory() as tmp:
        raw = os.path.join(tmp, "tfl.zip")
        download(raw)
        if not zipfile.is_zipfile(raw):
            print("the TfL download is not a zip file (check TFL_APP_KEY)", file=sys.stderr)
            return 1
        xml_dir = os.path.join(tmp, "xml")
        os.mkdir(xml_dir)
        with zipfile.ZipFile(raw) as z:
            kept, skipped = extract_rail(z, xml_dir)
        print(f"converting {kept} rail files (skipped {skipped} bus, coach and boat files)", flush=True)
        if kept == 0:
            print("no Underground, DLR, tram or rail timetables found", file=sys.stderr)
            return 1
        # Write next to the output and move into place when complete, so a
        # failed run never replaces a working feed.
        partial = out + ".partial.zip"
        transx2gtfs.convert(xml_dir, partial)
        name_lines(partial)
        os.replace(partial, out)
        db = partial[: -len(".zip")] + ".db"
        if os.path.exists(db):
            os.remove(db)
    print("saved", out, flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
