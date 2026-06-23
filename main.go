// lootlist — a tiny self-hosted soft-reserve / loot wishlist tracker.
// Single Go binary + embedded SQLite + embedded SPA. Just for one raider.
package main

import (
	"database/sql"
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed web/*
var webFS embed.FS

var db *sql.DB

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func main() {
	dbPath := env("LOOTLIST_DB", "/data/lootlist.db")
	addr := env("LOOTLIST_ADDR", ":8080")

	var err error
	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		log.Fatal(err)
	}
	db.SetMaxOpenConns(1) // sqlite: avoid "database is locked"
	mustExec("PRAGMA journal_mode=WAL;")
	mustExec("PRAGMA busy_timeout=5000;")
	mustInit()
	seedIfEmpty()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/items", listItems)
	mux.HandleFunc("POST /api/items", createItem)
	mux.HandleFunc("PATCH /api/items/{id}", patchItem)
	mux.HandleFunc("DELETE /api/items/{id}", deleteItem)
	mux.HandleFunc("POST /api/items/{id}/got", gotItem)
	mux.HandleFunc("GET /api/catalog", searchCatalog)
	mux.HandleFunc("POST /api/catalog", addCatalog)
	mux.HandleFunc("GET /api/stats", getStats)
	mux.HandleFunc("GET /api/progress", getProgress)
	mux.HandleFunc("PUT /api/progress/{key}", putProgress)
	mux.HandleFunc("DELETE /api/progress/{key}", delProgress)
	mux.HandleFunc("POST /api/reserves/clear", clearReserves)

	sub, _ := fs.Sub(webFS, "web")
	mux.Handle("GET /", http.FileServer(http.FS(sub)))

	log.Printf("lootlist listening on %s (db=%s)", addr, dbPath)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func mustExec(q string, args ...any) {
	if _, err := db.Exec(q, args...); err != nil {
		log.Fatalf("exec %q: %v", q, err)
	}
}

func mustInit() {
	mustExec(`CREATE TABLE IF NOT EXISTS items (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  raid TEXT NOT NULL,
  boss TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL,
  slot TEXT NOT NULL DEFAULT '',
  wowhead_id INTEGER NOT NULL DEFAULT 0,
  priority INTEGER NOT NULL DEFAULT 2,
  status TEXT NOT NULL DEFAULT 'wanted',
  reserved INTEGER NOT NULL DEFAULT 0,
  note TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);`)
	mustExec(`CREATE TABLE IF NOT EXISTS wins (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  item_id INTEGER,
  raid TEXT NOT NULL,
  name TEXT NOT NULL,
  won_at TEXT NOT NULL
);`)
	mustExec(`CREATE TABLE IF NOT EXISTS catalog (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  raid TEXT NOT NULL,
  boss TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL,
  slot TEXT NOT NULL DEFAULT '',
  wowhead_id INTEGER NOT NULL DEFAULT 0,
  tags TEXT NOT NULL DEFAULT ''
);`)
	mustExec(`CREATE TABLE IF NOT EXISTS progress (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL
);`)
}

// ---- items ----

type Item struct {
	ID        int64  `json:"id"`
	Raid      string `json:"raid"`
	Boss      string `json:"boss"`
	Name      string `json:"name"`
	Slot      string `json:"slot"`
	WowheadID int64  `json:"wowhead_id"`
	Priority  int    `json:"priority"`
	Status    string `json:"status"`
	Reserved  bool   `json:"reserved"`
	Note      string `json:"note"`
	CreatedAt string `json:"created_at"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func listItems(w http.ResponseWriter, r *http.Request) {
	rows, err := db.Query(`SELECT id,raid,boss,name,slot,wowhead_id,priority,status,reserved,note,created_at
		FROM items ORDER BY raid, priority, boss, name`)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()
	out := []Item{}
	for rows.Next() {
		var it Item
		var res int
		if err := rows.Scan(&it.ID, &it.Raid, &it.Boss, &it.Name, &it.Slot, &it.WowheadID,
			&it.Priority, &it.Status, &res, &it.Note, &it.CreatedAt); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		it.Reserved = res != 0
		out = append(out, it)
	}
	writeJSON(w, 200, out)
}

func createItem(w http.ResponseWriter, r *http.Request) {
	var it Item
	if err := json.NewDecoder(r.Body).Decode(&it); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if it.Priority == 0 {
		it.Priority = 2
	}
	if it.Status == "" {
		it.Status = "wanted"
	}
	res, err := db.Exec(`INSERT INTO items(raid,boss,name,slot,wowhead_id,priority,status,reserved,note,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`,
		it.Raid, it.Boss, it.Name, it.Slot, it.WowheadID, it.Priority, it.Status, b2i(it.Reserved), it.Note, now())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	it.ID, _ = res.LastInsertId()
	writeJSON(w, 201, it)
}

func patchItem(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Raid      *string `json:"raid"`
		Boss      *string `json:"boss"`
		Name      *string `json:"name"`
		Slot      *string `json:"slot"`
		WowheadID *int64  `json:"wowhead_id"`
		Priority  *int    `json:"priority"`
		Status    *string `json:"status"`
		Reserved  *bool   `json:"reserved"`
		Note      *string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	sets := []string{}
	args := []any{}
	add := func(col string, v any) { sets = append(sets, col+"=?"); args = append(args, v) }
	if p.Raid != nil {
		add("raid", *p.Raid)
	}
	if p.Boss != nil {
		add("boss", *p.Boss)
	}
	if p.Name != nil {
		add("name", *p.Name)
	}
	if p.Slot != nil {
		add("slot", *p.Slot)
	}
	if p.WowheadID != nil {
		add("wowhead_id", *p.WowheadID)
	}
	if p.Priority != nil {
		add("priority", *p.Priority)
	}
	if p.Status != nil {
		add("status", *p.Status)
	}
	if p.Reserved != nil {
		add("reserved", b2i(*p.Reserved))
	}
	if p.Note != nil {
		add("note", *p.Note)
	}
	if len(sets) == 0 {
		writeJSON(w, 200, map[string]string{"ok": "nochange"})
		return
	}
	args = append(args, r.PathValue("id"))
	if _, err := db.Exec("UPDATE items SET "+strings.Join(sets, ",")+" WHERE id=?", args...); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "updated"})
}

func deleteItem(w http.ResponseWriter, r *http.Request) {
	if _, err := db.Exec("DELETE FROM items WHERE id=?", r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "deleted"})
}

// gotItem marks an item obtained and logs a +1 win for that raid/tier.
func gotItem(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var raid, name string
	if err := db.QueryRow("SELECT raid,name FROM items WHERE id=?", id).Scan(&raid, &name); err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	tx, err := db.Begin()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	_, e1 := tx.Exec("UPDATE items SET status='got', reserved=0 WHERE id=?", id)
	_, e2 := tx.Exec("INSERT INTO wins(item_id,raid,name,won_at) VALUES(?,?,?,?)", id, raid, name, now())
	if e1 != nil || e2 != nil {
		tx.Rollback()
		http.Error(w, "tx failed", 500)
		return
	}
	tx.Commit()
	writeJSON(w, 200, map[string]string{"ok": "got"})
}

func clearReserves(w http.ResponseWriter, r *http.Request) {
	raid := r.URL.Query().Get("raid")
	if raid != "" {
		mustExec("UPDATE items SET reserved=0 WHERE raid=?", raid)
	} else {
		mustExec("UPDATE items SET reserved=0")
	}
	writeJSON(w, 200, map[string]string{"ok": "cleared"})
}

// ---- catalog (searchable item source) ----

func searchCatalog(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	var rows *sql.Rows
	var err error
	if q == "" {
		rows, err = db.Query("SELECT raid,boss,name,slot,wowhead_id,tags FROM catalog ORDER BY raid,boss,name LIMIT 5000")
	} else {
		rows, err = db.Query("SELECT raid,boss,name,slot,wowhead_id,tags FROM catalog WHERE name LIKE ? ORDER BY name LIMIT 25", "%"+q+"%")
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var raid, boss, name, slot, tags string
		var id int64
		rows.Scan(&raid, &boss, &name, &slot, &id, &tags)
		out = append(out, map[string]any{"raid": raid, "boss": boss, "name": name, "slot": slot, "wowhead_id": id, "tags": tags})
	}
	writeJSON(w, 200, out)
}

func addCatalog(w http.ResponseWriter, r *http.Request) {
	var c struct {
		Raid, Boss, Name, Slot, Tags string
		WowheadID                    int64 `json:"wowhead_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	mustExec("INSERT INTO catalog(raid,boss,name,slot,wowhead_id,tags) VALUES(?,?,?,?,?,?)", c.Raid, c.Boss, c.Name, c.Slot, c.WowheadID, c.Tags)
	writeJSON(w, 201, map[string]string{"ok": "added"})
}

// ---- stats (+1 per raid) ----

func getStats(w http.ResponseWriter, r *http.Request) {
	rows, err := db.Query("SELECT raid,COUNT(*) FROM wins GROUP BY raid")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()
	m := map[string]int{}
	for rows.Next() {
		var raid string
		var n int
		rows.Scan(&raid, &n)
		m[raid] = n
	}
	writeJSON(w, 200, m)
}

// ---- progress (attunement / rep / notes) ----

func getProgress(w http.ResponseWriter, r *http.Request) {
	rows, err := db.Query("SELECT key,value,updated_at FROM progress ORDER BY key")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()
	out := []map[string]string{}
	for rows.Next() {
		var k, v, u string
		rows.Scan(&k, &v, &u)
		out = append(out, map[string]string{"key": k, "value": v, "updated_at": u})
	}
	writeJSON(w, 200, out)
}

func putProgress(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	mustExec(`INSERT INTO progress(key,value,updated_at) VALUES(?,?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
		r.PathValue("key"), body.Value, now())
	writeJSON(w, 200, map[string]string{"ok": "saved"})
}

func delProgress(w http.ResponseWriter, r *http.Request) {
	mustExec("DELETE FROM progress WHERE key=?", r.PathValue("key"))
	writeJSON(w, 200, map[string]string{"ok": "deleted"})
}

// ---- seed ----

func seedIfEmpty() {
	var n int
	db.QueryRow("SELECT COUNT(*) FROM catalog").Scan(&n)
	if n == 0 {
		cat := [][]any{
			{"Karazhan", "Prince Malchezaar", "Nathrezim Mindblade", "Main Hand", 28772, "mage,caster"},
			{"Karazhan", "Prince Malchezaar", "Collar of the Aldor", "Head", 29076, "mage"},
			{"Karazhan", "The Curator", "Gloves of the Aldor", "Hands", 29080, "mage"},
			{"Karazhan", "Shade of Aran", "Pendant of the Violet Eye", "Trinket", 28727, "mage,caster,healer"},
			{"Karazhan", "Opera (The Crone)", "Ruby Slippers", "Feet", 28585, "mage,caster"},
			{"Gruul's Lair", "High King Maulgar", "Pauldrons of the Aldor", "Shoulders", 29079, "mage"},
			{"Magtheridon's Lair", "Magtheridon", "Vestments of the Aldor", "Chest", 29077, "mage"},
			{"Magtheridon's Lair", "Magtheridon", "Eredar Wand of Obliteration", "Wand", 28783, "mage,caster"},
			{"Tempest Keep", "High Astromancer Solarian", "Wand of the Forgotten Star", "Wand", 29982, "mage,caster"},
		}
		for _, c := range cat {
			mustExec("INSERT INTO catalog(raid,boss,name,slot,wowhead_id,tags) VALUES(?,?,?,?,?,?)", c...)
		}
	}

	db.QueryRow("SELECT COUNT(*) FROM items").Scan(&n)
	if n == 0 {
		items := []Item{
			{Raid: "Karazhan", Boss: "Prince Malchezaar", Name: "Nathrezim Mindblade", Slot: "Main Hand", WowheadID: 28772, Priority: 1, Status: "wanted", Reserved: true, Note: "Top SR - biggest upgrade, replaces PvP sword"},
			{Raid: "Karazhan", Boss: "The Curator", Name: "Gloves of the Aldor", Slot: "Hands", WowheadID: 29080, Priority: 1, Status: "wanted", Reserved: true, Note: "Replaces PvP Tempest's Touch (no set bonus needed - 4pc useless for arcane)"},
			{Raid: "Karazhan", Boss: "Shade of Aran", Name: "Pendant of the Violet Eye", Slot: "Trinket", WowheadID: 28727, Priority: 3, Status: "wanted", Note: "Mana-comfort pick; or skip & farm Quagmirran's Eye from H Slave Pens"},
		}
		for _, it := range items {
			mustExec(`INSERT INTO items(raid,boss,name,slot,wowhead_id,priority,status,reserved,note,created_at)
				VALUES(?,?,?,?,?,?,?,?,?,?)`,
				it.Raid, it.Boss, it.Name, it.Slot, it.WowheadID, it.Priority, it.Status, b2i(it.Reserved), it.Note, now())
		}
	}

	db.QueryRow("SELECT COUNT(*) FROM progress").Scan(&n)
	if n == 0 {
		prog := [][2]string{
			{"Honor Hold", "Revered DONE - Flamewrought Key bought"},
			{"Cenarion Expedition", "~halfway Honored->Revered (Steamvault + Coilfang Armaments turn-ins)"},
			{"Lower City", "Friendly - need Revered for Heroic Sethekk (Nightbane chain) + Heroic Shadow Lab (Strength trial)"},
			{"The Sha'tar", "Friendly - need Revered for Heroic Arcatraz (Tenacity trial) + Warpforged Key"},
			{"Karazhan attunement", "DONE (cleared Kara 1x)"},
			{"The Violet Eye", "~Friendly - need Honored to START the Blackened Urn / Nightbane chain (Alturus, Deadwind Pass)"},
			{"Cipher of Damnation (TK)", "On the LAST step"},
			{"SSC key (Mark of Vashj)", "Need: Earthen Signet (Gruul) + Blazing Signet (Nightbane) + start/finish at H Slave Pens (CE Revered)"},
			{"TK key (Tempest Key)", "Cipher almost done -> Trials of the Naaru (H ShattHalls/Steamvault/ShadowLab/Arcatraz) + kill Magtheridon"},
		}
		for _, p := range prog {
			mustExec("INSERT INTO progress(key,value,updated_at) VALUES(?,?,?)", p[0], p[1], now())
		}
	}
}
