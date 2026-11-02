# go-steam-organizer

A Go CLI and interactive Terminal UI (TUI) utility that authenticates with Steam via your web browser, queries your purchase history, license activations, playtime, and store metadata, and organizes your Steam Library items into collections.

## Features

- **Automatic Browser Login**: Prompts you to sign in to Steam via your browser (using Chrome/Edge/Brave DevTools Protocol or a local browser callback page) and caches your session at `~/.steam-organizer/session.json`.
- **Add Specific Items to Collections**: Add games by AppID or title directly into local Steam `cloud-storage-namespace-1.json` collections (with automatic backups and optional `ICloudConfigStoreService` cloud sync).
- **Query & Filter Purchases Across 4 Criteria Categories**:
  1. **Purchase Date / Year**: Filter by purchase year(s) (`--year 2025`), date ranges (`--after`, `--before`), or group dynamically (`Bought in {{.PurchaseYear}}`).
  2. **Price & Acquisition Type**: Filter by `purchase`, `free`, `retail_key`, `gift`, `bundle`, `refunded`, or price bounds (`--min-price`, `--max-price`, `--free`).
  3. **Store Metadata**: Filter by genres (`--genre RPG`), user tags (`--tag Roguelike`), categories (`--category Co-op`), developer/publisher, and Steam Deck compatibility (`--deck verified`).
  4. **Playtime / Played Status**: Filter for unplayed games (`--unplayed`) or playtime bounds (`--min-hours`, `--max-hours 2.0`).
- **Declarative YAML/JSON Rules Engine**: Define reusable rules in `rules.yaml` (see `rules.example.yaml`) with `--dry-run` preview support.
- **Interactive Terminal UI (TUI)**: Run `steam-organizer tui` or `steam-organizer --tui` to interactively filter purchases, select games, assign collections, and apply rules.

## Quick Start

### 1. Build
```powershell
go build -o steam-organizer.exe .
```

### 2. Log In via Browser
Any command that needs your Steam library or purchase history will automatically prompt you to log in via the browser if not already authenticated, or you can log in explicitly:
```powershell
.\steam-organizer.exe login
.\steam-organizer.exe login --status
```

### 3. Add Specific Items to a Collection
```powershell
# Add by AppID
.\steam-organizer.exe collections add --collection "Favorites" --app 440,620

# Add by Game Title
.\steam-organizer.exe collections add --collection "Co-op Night" --title "Portal 2","Left 4 Dead 2"

# List all current collections
.\steam-organizer.exe collections list
```

### 4. Query Purchases & Add Matches to Collections
```powershell
# Query games purchased in 2025
.\steam-organizer.exe purchases --year 2025

# Find unplayed RPGs bought for under $10 and add them to "Backlog: RPGs"
.\steam-organizer.exe purchases --genre RPG --unplayed --max-price 10 --enrich-metadata --add-to "Backlog: RPGs"

# Automatically organize all purchases into "Bought in YYYY" collections (preview first with --dry-run)
.\steam-organizer.exe purchases --type purchase --add-to "Bought in {{.PurchaseYear}}" --dry-run
```

### 5. Run Declarative Rules (`organize`)
```powershell
# Preview rule matches without modifying files
.\steam-organizer.exe organize --config rules.example.yaml --dry-run

# Apply rules to your Steam collections
.\steam-organizer.exe organize --config rules.example.yaml --enrich-metadata
```

### 6. Interactive Terminal UI (TUI)
```powershell
.\steam-organizer.exe tui
# or
.\steam-organizer.exe --tui
```
