#!/usr/bin/env python3
"""
Import the full TBC raid loot catalog from real data sources (no hand-typing):

  - AtlasLootClassic  data-tbc.lua   -> raid -> boss -> item IDs
  - WoWSims TBC       all_item_tooltips.csv -> id -> name, quality, slot, classes

Writes/replaces the `catalog` table in the target SQLite db. Leaves items/
progress/wins untouched. Re-runnable; data is frozen so this is a one-time pull.

Usage: python3 import_tbc.py /path/to/lootlist.db
"""
import csv, io, re, sqlite3, sys, urllib.request

ATLAS = "https://raw.githubusercontent.com/Hoizame/AtlasLootClassic/master/AtlasLootClassic_DungeonsAndRaids/data-tbc.lua"
WOWSIMS = "https://raw.githubusercontent.com/wowsims/tbc/master/assets/item_data/all_item_tooltips.csv"

SLOTS = ("Head","Neck","Shoulder","Back","Chest","Wrist","Hands","Waist","Legs",
         "Feet","Finger","Trinket","Main Hand","Off Hand","One-Hand","Two-Hand",
         "Ranged","Wand","Relic","Held In Off-hand","Thrown","Shield")

ARMOR_SLOTS = {"Head", "Shoulder", "Chest", "Wrist", "Hands", "Waist", "Legs", "Feet"}

def mage_usable(slot, tip, cloth):
    """A caster item a MAGE can actually equip. Mages wear only cloth and
    wield only daggers, one-hand swords, staves, and wands — no maces, axes,
    polearms, two-hand swords, shields, or off-hand weapons (no dual-wield)."""
    if not slot:
        return True                       # tier tokens etc. — no equip slot
    if slot in ARMOR_SLOTS:
        return cloth
    if slot in {"Trinket", "Finger", "Neck", "Back", "Held In Off-hand", "Wand"}:
        return True
    if slot in {"Main Hand", "One-Hand"}:
        return ">Sword<" in tip or ">Dagger<" in tip
    if slot == "Two-Hand":
        return ">Staff<" in tip
    return False                          # Off Hand weapon, Shield, Ranged, Thrown, Relic

def fetch(url):
    print("  fetch", url.split("/")[-1], "...", flush=True)
    return urllib.request.urlopen(url, timeout=60).read().decode("utf-8", "replace")

# ---- WoWSims item dictionary: id -> (name, quality, slot, tags) ----
def build_dict(csv_text):
    d = {}
    # NOTE: the JSON_String column is NOT quote-escaped and contains commas,
    # so csv.DictReader truncates it. Split manually into 3 fields instead.
    for line in csv_text.splitlines()[1:]:
        parts = line.split(",", 2)
        if len(parts) < 3:
            continue
        try:
            iid = int(parts[0])
        except ValueError:
            continue
        js = parts[2]
        m = re.search(r'"name"\s*:\s*"((?:[^"\\]|\\.)*)"', js)
        if not m:
            continue
        name = m.group(1).encode().decode("unicode_escape")
        qm = re.search(r'"quality"\s*:\s*(\d+)', js)
        quality = int(qm.group(1)) if qm else 0
        tip = js
        slot = next((s for s in SLOTS if f">{s}<" in tip), "")
        # Some items' equip LOCATION hides their real kind: shields sit in the
        # "Off Hand" slot and wands in the "Ranged" slot. Re-label by the armor/
        # weapon SUBTYPE so they're categorized right (and so mages correctly
        # get wands but not shields).
        if ">Shield<" in tip:
            slot = "Shield"
        elif ">Wand<" in tip:
            slot = "Wand"
        tags = set()
        cm = re.search(r"Classes:\s*(.*?)(?:</td>|<br|</span></?(?:td|tr))", tip)
        if cm:
            for c in re.sub(r"<[^>]+>", "", cm.group(1)).split(","):
                c = c.strip().lower()
                if c:
                    tags.add(c)
        caster = any(k in tip for k in ("Intellect", "Spell Damage", "Spell Power",
                     "damage and healing", "Damage and Healing", "Spell Hit",
                     "Spell Critical", "Mana per 5", "mana per 5", "Healing Spells"))
        cloth = ">Cloth<" in tip
        if caster:
            tags.add("caster")
            if mage_usable(slot, tip, cloth):
                tags.add("mage")
        d[iid] = (name, quality, slot, ",".join(sorted(tags)))
    return d

# ---- AtlasLoot: raid -> boss -> [itemID] ----
# AtlasLoot doesn't name instances inline, but carries the game InstanceID.
# Map the 9 TBC raid instance IDs to names; everything else is ignored.
RAID_MAP = {
    532: "Karazhan", 565: "Gruul's Lair", 544: "Magtheridon's Lair",
    548: "Serpentshrine Cavern", 550: "Tempest Keep", 534: "Hyjal Summit",
    564: "Black Temple", 568: "Zul'Aman", 580: "Sunwell Plateau",
}
SKIP_BOSS = re.compile(r"pattern|recipe|plans|design|formula|schematic", re.I)
re_name = re.compile(r'name\s*=\s*(?:format\()?AL\["([^"]+)"\]')
re_inst = re.compile(r'InstanceID\s*=\s*(\d+)')
re_item = re.compile(r'^\s*\{\s*\d+\s*,\s*(\d+)\b')

def parse_atlas(lua):
    rows = []          # (raid, boss, itemID)
    raid = None
    boss = None
    pending = None
    for line in lua.splitlines():
        nm = re_name.search(line)
        if nm:
            pending = nm.group(1)
            boss = None            # new named block; not a boss until npcID confirms
        mi = re_inst.search(line)
        if mi:
            raid = RAID_MAP.get(int(mi.group(1)))   # None if not a tracked raid
            boss = None
        if "npcID" in line:
            boss = pending
            continue
        if raid and boss and not SKIP_BOSS.search(boss):
            im = re_item.match(line)
            if im:
                rows.append((raid, boss, int(im.group(1))))
    return rows

def main():
    if len(sys.argv) < 2:
        sys.exit("usage: import_tbc.py /path/to/lootlist.db")
    dbpath = sys.argv[1]

    print("Downloading sources...")
    items = build_dict(fetch(WOWSIMS))
    print(f"  item dictionary: {len(items)} items")
    triples = parse_atlas(fetch(ATLAS))
    print(f"  atlasloot raid drops: {len(triples)} (raid,boss,item) rows")

    seen = set()
    catalog = []
    skipped = 0
    for raid, boss, iid in triples:
        if (raid, boss, iid) in seen:
            continue
        seen.add((raid, boss, iid))
        info = items.get(iid)
        if info:
            name, quality, slot, tags = info
        else:
            name, quality, slot, tags = (f"item #{iid}", 0, "", "")
        if quality and quality < 3:       # rare+ only; drop greens/junk
            skipped += 1
            continue
        catalog.append((raid or "?", boss, name, slot, iid, tags))

    db = sqlite3.connect(dbpath)
    db.execute("""CREATE TABLE IF NOT EXISTS catalog(
        id INTEGER PRIMARY KEY AUTOINCREMENT, raid TEXT NOT NULL, boss TEXT NOT NULL DEFAULT '',
        name TEXT NOT NULL, slot TEXT NOT NULL DEFAULT '', wowhead_id INTEGER NOT NULL DEFAULT 0,
        tags TEXT NOT NULL DEFAULT '')""")
    db.execute("DELETE FROM catalog")
    db.executemany("INSERT INTO catalog(raid,boss,name,slot,wowhead_id,tags) VALUES(?,?,?,?,?,?)", catalog)
    db.commit()

    print(f"\nImported {len(catalog)} catalog items ({skipped} low-quality skipped).")
    print("Per raid:")
    for raid, n in db.execute("SELECT raid,COUNT(*) FROM catalog GROUP BY raid ORDER BY 2 DESC"):
        print(f"  {n:4d}  {raid}")
    db.close()

if __name__ == "__main__":
    main()
