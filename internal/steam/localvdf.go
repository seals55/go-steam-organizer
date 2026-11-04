package steam

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/csull/go-steam-organizer/internal/models"
)

var vdfGenreNames = map[int]string{
	1:  "Action",
	2:  "Strategy",
	3:  "RPG",
	4:  "Casual",
	9:  "Racing",
	18: "Sports",
	23: "Indie",
	25: "Adventure",
	28: "Simulation",
	29: "Massively Multiplayer",
	37: "Free to Play",
	50: "Accounting",
	51: "Animation & Modeling",
	52: "Audio Production",
	53: "Design & Illustration",
	54: "Education",
	55: "Software Training",
	56: "Utilities",
	57: "Video Production",
	58: "Web Publishing",
	59: "Game Development",
	60: "Photo Editing",
	70: "Early Access",
}

var vdfCategoryNames = map[string]string{
	"category_1":  "Multi-player",
	"category_2":  "Single-player",
	"category_9":  "Co-op",
	"category_18": "Partial Controller Support",
	"category_20": "MMO",
	"category_22": "Steam Achievements",
	"category_23": "Steam Cloud",
	"category_24": "Shared/Split Screen",
	"category_27": "Cross-Platform Multiplayer",
	"category_28": "Full controller support",
	"category_29": "Steam Trading Cards",
	"category_30": "Steam Workshop",
	"category_35": "In-App Purchases",
	"category_36": "Online PvP",
	"category_37": "Shared/Split Screen PvP",
	"category_38": "Online Co-Op",
	"category_39": "Shared/Split Screen Co-Op",
	"category_44": "Remote Play Together",
	"category_49": "PvP",
	"category_52": "Tracked Controller Support",
	"category_53": "VR Supported",
	"category_54": "VR Only",
	"category_62": "Family Sharing",
}

// detectLocalSteamDir finds the local Steam installation directory containing appcache/.
func detectLocalSteamDir() string {
	if envPath := os.Getenv("STEAM_PATH"); envPath != "" {
		if info, err := os.Stat(filepath.Join(envPath, "appcache")); err == nil && info.IsDir() {
			return envPath
		}
		return ""
	}

	var candidates []string
	switch runtime.GOOS {
	case "windows":
		if out, err := exec.Command("reg", "query", `HKCU\Software\Valve\Steam`, "/v", "SteamPath").Output(); err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				if strings.Contains(line, "SteamPath") {
					parts := strings.Fields(line)
					if len(parts) >= 3 {
						regPath := strings.Join(parts[2:], " ")
						candidates = append(candidates, filepath.FromSlash(strings.TrimSpace(regPath)))
					}
				}
			}
		}
		if pf86 := os.Getenv("ProgramFiles(x86)"); pf86 != "" {
			candidates = append(candidates, filepath.Join(pf86, "Steam"))
		}
		if pf := os.Getenv("ProgramFiles"); pf != "" {
			candidates = append(candidates, filepath.Join(pf, "Steam"))
		}
		candidates = append(candidates, `C:\Program Files (x86)\Steam`, `C:\Program Files\Steam`)
	case "darwin":
		if home, err := os.UserHomeDir(); err == nil {
			candidates = append(candidates, filepath.Join(home, "Library", "Application Support", "Steam"))
		}
	default:
		if home, err := os.UserHomeDir(); err == nil {
			candidates = append(candidates,
				filepath.Join(home, ".steam", "steam"),
				filepath.Join(home, ".local", "share", "Steam"),
				filepath.Join(home, ".var", "app", "com.valvesoftware.Steam", ".local", "share", "Steam"),
			)
		}
	}

	for _, c := range candidates {
		if c == "" {
			continue
		}
		if info, err := os.Stat(filepath.Join(c, "appcache")); err == nil && info.IsDir() {
			return c
		}
	}
	return ""
}

// EnrichFromLocalSteamCache reads Steam's local binary packageinfo.vdf and appinfo.vdf files
// to populate app titles, free/complimentary status, retail key status, genres, categories,
// developers, publishers, and Steam Deck compatibility for all owned apps without network rate limits.
func EnrichFromLocalSteamCache(itemsByApp map[uint32]models.LibraryItem, steamDir string) int {
	if len(itemsByApp) == 0 {
		return 0
	}
	if steamDir == "" {
		steamDir = detectLocalSteamDir()
	}
	if steamDir == "" {
		return 0
	}

	appFreePkg := make(map[uint32]bool)
	appPaidPkg := make(map[uint32]bool)
	appRetailPkg := make(map[uint32]bool)

	pkgPath := filepath.Join(steamDir, "appcache", "packageinfo.vdf")
	if pkgData, err := os.ReadFile(pkgPath); err == nil {
		parsePackageInfoVDF(pkgData, appFreePkg, appPaidPkg, appRetailPkg)
	}

	appPath := filepath.Join(steamDir, "appcache", "appinfo.vdf")
	appData, err := os.ReadFile(appPath)
	if err != nil {
		return 0
	}

	return parseAppInfoVDF(appData, itemsByApp, appFreePkg, appPaidPkg, appRetailPkg)
}

func parsePackageInfoVDF(
	pkgData []byte,
	appFreePkg map[uint32]bool,
	appPaidPkg map[uint32]bool,
	appRetailPkg map[uint32]bool,
) {
	if len(pkgData) < 8 {
		return
	}
	magic := binary.LittleEndian.Uint32(pkgData[0:4])
	hasToken := (magic & 0xFF) >= 0x28 // v28 (0x06565528) includes 8-byte PICS token
	pos := 8
	for pos+4 <= len(pkgData) {
		pkgID := binary.LittleEndian.Uint32(pkgData[pos : pos+4])
		pos += 4
		if pkgID == 0xFFFFFFFF {
			break
		}
		headerSkip := 24
		if hasToken {
			headerSkip = 32
		}
		if pos+headerSkip > len(pkgData) {
			break
		}
		pos += headerSkip

		m, newPos := parseInlineBinaryVDF(pkgData, pos)
		pos = newPos
		if sub, ok := m[fmt.Sprint(pkgID)].(map[string]any); ok {
			m = sub
		}
		if pkgID == 0 {
			continue // Skip Steam base redistributables package 0
		}

		bt := vdfAsInt(m["billingtype"])
		ext, _ := m["extended"].(map[string]any)
		freePromo := vdfAsInt(ext["freepromotion"])

		appidsMap, _ := m["appids"].(map[string]any)
		for _, av := range appidsMap {
			aid := uint32(vdfAsInt(av))
			if aid == 0 {
				continue
			}
			switch {
			case bt == 12 || bt == 0 || bt == 7 || freePromo == 1:
				appFreePkg[aid] = true
			case bt == 3 || bt == 5 || bt == 8:
				appRetailPkg[aid] = true
			case bt == 1 || bt == 10:
				appPaidPkg[aid] = true
			}
		}
	}
}

func parseAppInfoVDF(
	appData []byte,
	itemsByApp map[uint32]models.LibraryItem,
	appFreePkg map[uint32]bool,
	appPaidPkg map[uint32]bool,
	appRetailPkg map[uint32]bool,
) int {
	if len(appData) < 16 {
		return 0
	}
	magic := binary.LittleEndian.Uint32(appData[0:4])
	version := magic & 0xFF
	if version < 0x27 || version > 0x30 {
		return 0
	}

	hasStringTable := version >= 0x29
	var strTable []string
	endOfEntries := len(appData)
	pos := 8

	if hasStringTable {
		strTableOffset := int(binary.LittleEndian.Uint64(appData[8:16]))
		pos = 16
		if strTableOffset <= 16 || strTableOffset+4 > len(appData) {
			return 0
		}
		endOfEntries = strTableOffset
		strCount := int(binary.LittleEndian.Uint32(appData[strTableOffset : strTableOffset+4]))
		strTable = make([]string, strCount)
		sPos := strTableOffset + 4
		for i := 0; i < strCount && sPos < len(appData); i++ {
			end := bytes.IndexByte(appData[sPos:], 0)
			if end < 0 {
				break
			}
			strTable[i] = string(appData[sPos : sPos+end])
			sPos += end + 1
		}
	}

	enrichedCount := 0
	visited := make(map[uint32]bool, len(itemsByApp))
	entryMetaLen := 60 // infoState(4)+lastUpdated(4)+token(8)+sha1Text(20)+changeNum(4)+sha1Bin(20)
	if version == 0x27 {
		entryMetaLen = 40
	}

	for pos+8 <= endOfEntries {
		appID := binary.LittleEndian.Uint32(appData[pos : pos+4])
		if appID == 0 {
			break
		}
		size := int(binary.LittleEndian.Uint32(appData[pos+4 : pos+8]))
		entryStart := pos + 8
		vdfStart := entryStart + entryMetaLen
		vdfEnd := entryStart + size
		if size < entryMetaLen || vdfEnd > endOfEntries {
			break
		}

		if existing, owned := itemsByApp[appID]; owned {
			visited[appID] = true
			var m map[string]any
			if hasStringTable {
				m, _ = parseTableBinaryVDF(appData, vdfStart, vdfEnd, strTable)
			} else {
				m, _ = parseInlineBinaryVDF(appData[:vdfEnd], vdfStart)
			}
			if appInfo, ok := m["appinfo"].(map[string]any); ok {
				m = appInfo
			}
			applyAppInfoToLibraryItem(&existing, m, appFreePkg[appID], appPaidPkg[appID], appRetailPkg[appID])
			itemsByApp[appID] = existing
			enrichedCount++
		}

		pos = vdfEnd
	}

	// Also apply packageinfo.vdf billing types to any owned apps not yet present in appinfo.vdf
	for appID, existing := range itemsByApp {
		if visited[appID] {
			continue
		}
		if appFreePkg[appID] || appPaidPkg[appID] || appRetailPkg[appID] {
			applyAppInfoToLibraryItem(&existing, nil, appFreePkg[appID], appPaidPkg[appID], appRetailPkg[appID])
			itemsByApp[appID] = existing
			enrichedCount++
		}
	}
	return enrichedCount
}

func applyAppInfoToLibraryItem(
	item *models.LibraryItem,
	m map[string]any,
	inFreePkg bool,
	inPaidPkg bool,
	inRetailPkg bool,
) {
	common, _ := m["common"].(map[string]any)
	extended, _ := m["extended"].(map[string]any)

	if name, ok := common["name"].(string); ok && strings.TrimSpace(name) != "" {
		if item.Name == "" || strings.HasPrefix(item.Name, "App ") {
			item.Name = strings.TrimSpace(name)
		}
	}

	// Free / Complimentary check
	isFreeApp := vdfAsInt(extended["isfreeapp"]) == 1
	hasFreeGenre := vdfAsInt(common["primary_genre"]) == 37

	// Genres
	if len(item.Genres) == 0 {
		seenGenres := make(map[string]bool)
		if genresMap, ok := common["genres"].(map[string]any); ok {
			for _, gv := range genresMap {
				gid := vdfAsInt(gv)
				if gid == 37 {
					hasFreeGenre = true
				}
				if gName, exists := vdfGenreNames[gid]; exists && !seenGenres[gName] {
					seenGenres[gName] = true
					item.Genres = append(item.Genres, gName)
				}
			}
		}
		if pg := vdfAsInt(common["primary_genre"]); pg > 0 {
			if gName, exists := vdfGenreNames[pg]; exists && !seenGenres[gName] {
				item.Genres = append(item.Genres, gName)
			}
		}
	}

	if isFreeApp || hasFreeGenre || (inFreePkg && !inPaidPkg) {
		item.IsFree = true
		if item.AcquisitionType == "" || item.AcquisitionType == models.AcquisitionUnknown {
			item.AcquisitionType = models.AcquisitionFree
			item.PriceDisplay = "Free"
			switch {
			case isFreeApp:
				item.AcquisitionReason = "Local Steam appinfo.vdf (extended.isfreeapp=1)"
			case hasFreeGenre:
				item.AcquisitionReason = "Local Steam appinfo.vdf (Genre 37: Free to Play)"
			default:
				item.AcquisitionReason = "Local Steam packageinfo.vdf (FreeOnDemand / Complimentary package)"
			}
		}
	} else if inRetailPkg && !inPaidPkg {
		if item.AcquisitionType == "" || item.AcquisitionType == models.AcquisitionUnknown {
			item.AcquisitionType = models.AcquisitionRetailKey
			item.AcquisitionReason = "Local Steam packageinfo.vdf (BillingType=3 Retail CD Key / Promo)"
		}
	} else if inPaidPkg {
		if item.AcquisitionType == "" || item.AcquisitionType == models.AcquisitionUnknown {
			item.AcquisitionType = models.AcquisitionPurchase
			item.AcquisitionReason = "Local Steam packageinfo.vdf (BillingType=1 Paid Steam Store Package)"
		}
	}

	// Categories
	if len(item.Categories) == 0 {
		if catMap, ok := common["category"].(map[string]any); ok {
			for k, v := range catMap {
				if vdfAsInt(v) == 1 {
					if cName, exists := vdfCategoryNames[k]; exists {
						item.Categories = append(item.Categories, cName)
					}
				}
			}
		}
	}

	// Developers & Publishers
	if len(item.Developers) == 0 || len(item.Publishers) == 0 {
		if assocMap, ok := common["associations"].(map[string]any); ok {
			for _, av := range assocMap {
				if entry, ok := av.(map[string]any); ok {
					aType, _ := entry["type"].(string)
					aName, _ := entry["name"].(string)
					aName = strings.TrimSpace(aName)
					if aName == "" {
						continue
					}
					switch strings.ToLower(aType) {
					case "developer":
						item.Developers = appendUniqueStr(item.Developers, aName)
					case "publisher":
						item.Publishers = appendUniqueStr(item.Publishers, aName)
					}
				}
			}
		}
		if dev, ok := extended["developer"].(string); ok && strings.TrimSpace(dev) != "" {
			item.Developers = appendUniqueStr(item.Developers, strings.TrimSpace(dev))
		}
		if pub, ok := extended["publisher"].(string); ok && strings.TrimSpace(pub) != "" {
			item.Publishers = appendUniqueStr(item.Publishers, strings.TrimSpace(pub))
		}
	}

	// Steam Deck Compatibility
	if item.DeckCompatibility == "" || item.DeckCompatibility == models.DeckUnknown {
		if deckMap, ok := common["steam_deck_compatibility"].(map[string]any); ok {
			switch vdfAsInt(deckMap["category"]) {
			case 3:
				item.DeckCompatibility = models.DeckVerified
			case 2:
				item.DeckCompatibility = models.DeckPlayable
			case 1:
				item.DeckCompatibility = models.DeckUnsupported
			}
		}
	}
}

func appendUniqueStr(slice []string, val string) []string {
	for _, existing := range slice {
		if strings.EqualFold(existing, val) {
			return slice
		}
	}
	return append(slice, val)
}

func vdfAsInt(v any) int {
	switch x := v.(type) {
	case int32:
		return int(x)
	case int:
		return x
	case uint64:
		return int(x)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(x))
		return n
	}
	return 0
}

func vdfReadCString(b []byte, pos int) (string, int) {
	if pos >= len(b) {
		return "", len(b)
	}
	end := bytes.IndexByte(b[pos:], 0)
	if end < 0 {
		return "", len(b)
	}
	return string(b[pos : pos+end]), pos + end + 1
}

func parseInlineBinaryVDF(b []byte, pos int) (map[string]any, int) {
	m := make(map[string]any)
	for pos < len(b) {
		t := b[pos]
		pos++
		if t == 0x08 {
			break
		}
		key, newPos := vdfReadCString(b, pos)
		pos = newPos
		switch t {
		case 0x00:
			sub, np := parseInlineBinaryVDF(b, pos)
			m[key] = sub
			pos = np
		case 0x01:
			val, np := vdfReadCString(b, pos)
			m[key] = val
			pos = np
		case 0x02, 0x03, 0x07:
			if pos+4 <= len(b) {
				m[key] = int32(binary.LittleEndian.Uint32(b[pos : pos+4]))
				pos += 4
			} else {
				return m, len(b)
			}
		case 0x04, 0x05, 0x06, 0x0A:
			if pos+8 <= len(b) {
				m[key] = binary.LittleEndian.Uint64(b[pos : pos+8])
				pos += 8
			} else {
				return m, len(b)
			}
		default:
			return m, pos
		}
	}
	return m, pos
}

func parseTableBinaryVDF(b []byte, pos, end int, strTable []string) (map[string]any, int) {
	m := make(map[string]any)
	for pos < end {
		t := b[pos]
		pos++
		if t == 0x08 {
			break
		}
		if pos+4 > end {
			break
		}
		keyIdx := int(binary.LittleEndian.Uint32(b[pos : pos+4]))
		pos += 4
		key := ""
		if keyIdx >= 0 && keyIdx < len(strTable) {
			key = strTable[keyIdx]
		}
		switch t {
		case 0x00:
			sub, np := parseTableBinaryVDF(b, pos, end, strTable)
			m[key] = sub
			pos = np
		case 0x01:
			val, np := vdfReadCString(b, pos)
			m[key] = val
			pos = np
		case 0x02, 0x03, 0x07:
			if pos+4 <= end {
				m[key] = int32(binary.LittleEndian.Uint32(b[pos : pos+4]))
				pos += 4
			} else {
				return m, end
			}
		case 0x04, 0x05, 0x06, 0x0A:
			if pos+8 <= end {
				m[key] = binary.LittleEndian.Uint64(b[pos : pos+8])
				pos += 8
			} else {
				return m, end
			}
		default:
			return m, pos
		}
	}
	return m, pos
}
