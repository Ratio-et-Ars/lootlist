# ⚔ LootList

A tiny, self-hosted **soft-reserve / loot wishlist tracker** for WoW TBC Classic.
One Go binary, embedded SQLite, embedded web UI — no build step, no external
services, runs anywhere. Built for raiders who want to plan SRs without juggling
Discord channels or spreadsheets.

## Features

- **Plan page** — your wishlist per raid on top, the full browsable loot catalog
  below. Search + class/role filter chips (`mage`, `caster`, …). **Click any
  catalog row to add/remove it** — hover shows the live Wowhead tooltip, the
  click toggles, and the page stays anchored so nothing jumps.
- **⚔ Raid Mode** — a big-text, low-clutter toggle for raid night: just your
  *Tonight's SR* card and wishlist, with fat tap targets for reserve / "got".
- **+1 tracker** — marking an item "got" logs a win and bumps your `+1` count per
  raid, so you know your priority on contested rolls.
- **Progress board** — attunement / rep / notes, your single source of truth.
- **Wowhead links** — quality color, icons, and hover tooltips via the official
  widget.

The loot catalog covers all nine TBC raids (Karazhan → Sunwell), imported from
real data — never hand-typed.

## Run it (local)

```sh
LOOTLIST_DB=./lootlist.db LOOTLIST_ADDR=:8088 go run .
# open http://localhost:8088
```

## Import the loot catalog

The catalog is built from real, frozen data sources (see **Credits**):

```sh
python3 import_tbc.py ./lootlist.db
```

This pulls AtlasLoot's raid→boss→item mapping and WoWSims' item dictionary,
pairs them, and replaces the `catalog` table. Re-runnable; your wishlist,
progress, and `+1` history are left untouched.

## Deploy (Docker)

```sh
docker compose up -d --build
```

DB persists in `./data/lootlist.db` — back it up. Put it behind any reverse
proxy:

```
loot.example.com {
    reverse_proxy 127.0.0.1:8088
}
```

## API

The whole thing is a small JSON API, so you can seed/edit from a console or
scripts:

```sh
H=http://localhost:8088
curl $H/api/items                                   # wishlist
curl -X POST  $H/api/items   -d '{"raid":"Karazhan","name":"Nathrezim Mindblade","wowhead_id":28770,"priority":1,"reserved":true}'
curl -X PATCH $H/api/items/3 -d '{"reserved":true}'
curl -X POST  $H/api/items/3/got                    # mark obtained (+1)
curl "$H/api/catalog?q=mindblade"                   # search catalog
curl $H/api/progress
curl -X PUT   $H/api/progress/"Lower%20City" -d '{"value":"Revered"}'
```

| Env | Default | |
|---|---|---|
| `LOOTLIST_DB` | `/data/lootlist.db` | SQLite path |
| `LOOTLIST_ADDR` | `:8080` | listen address |

## Credits

LootList ships no game data of its own; the importer fetches it at build time from:

- **[AtlasLootClassic](https://github.com/Hoizame/AtlasLootClassic)** — raid → boss → item-ID mapping.
- **[WoWSims (TBC)](https://github.com/wowsims/tbc)** — item names, slots, quality, class.
- **[Wowhead](https://www.wowhead.com/tbc/)** — tooltips & icons via the official widget.

Please respect those projects' own licenses and terms.

## Contributing

Newcomers of good will are welcome. Please read the [Code of Conduct](CODE_OF_CONDUCT.md).

## License

[MIT](LICENSE) © Ratio-et-Ars
