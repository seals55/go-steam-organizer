package steam

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/csull/go-steam-organizer/internal/auth"
	"github.com/csull/go-steam-organizer/internal/models"
)

func TestEnrichFromLocalSteamCacheV29(t *testing.T) {
	tmpSteam := t.TempDir()
	appcacheDir := filepath.Join(tmpSteam, "appcache")
	if err := os.MkdirAll(appcacheDir, 0700); err != nil {
		t.Fatal(err)
	}

	// 1. Build synthetic packageinfo.vdf (v28: 0x06565528)
	// Package 1563530: billingtype=12 (FreeOnDemand), appids={0: 4480260}
	// Package 9999:    billingtype=3  (Retail Key),   appids={0: 1086940}
	var pkgBuf bytes.Buffer
	_ = binary.Write(&pkgBuf, binary.LittleEndian, uint32(0x06565528))
	_ = binary.Write(&pkgBuf, binary.LittleEndian, uint32(1))

	writePkgEntry := func(pkgID uint32, billingType int32, appID int32) {
		_ = binary.Write(&pkgBuf, binary.LittleEndian, pkgID)
		pkgBuf.Write(make([]byte, 32)) // sha1(20) + changeNum(4) + token(8)
		// Root object "<pkgID>"
		pkgBuf.WriteByte(0x00)
		pkgBuf.WriteString(fmt.Sprint(pkgID) + "\x00")
		// billingtype (int32)
		pkgBuf.WriteByte(0x02)
		pkgBuf.WriteString("billingtype\x00")
		_ = binary.Write(&pkgBuf, binary.LittleEndian, billingType)
		// appids map
		pkgBuf.WriteByte(0x00)
		pkgBuf.WriteString("appids\x00")
		pkgBuf.WriteByte(0x02)
		pkgBuf.WriteString("0\x00")
		_ = binary.Write(&pkgBuf, binary.LittleEndian, appID)
		pkgBuf.WriteByte(0x08) // end appids
		pkgBuf.WriteByte(0x08) // end "<pkgID>"
		pkgBuf.WriteByte(0x08) // end entry VDF
	}

	writePkgEntry(1563530, 12, 4480260)
	writePkgEntry(9999, 3, 1086940)
	_ = binary.Write(&pkgBuf, binary.LittleEndian, uint32(0xFFFFFFFF))

	if err := os.WriteFile(filepath.Join(appcacheDir, "packageinfo.vdf"), pkgBuf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}

	// 2. Build synthetic appinfo.vdf (v29: 0x07564429 with string table)
	strTable := []string{
		"appinfo",                  // 0
		"common",                   // 1
		"name",                     // 2
		"type",                     // 3
		"primary_genre",            // 4
		"genres",                   // 5
		"0",                        // 6
		"category",                 // 7
		"category_2",               // 8
		"category_28",              // 9
		"steam_deck_compatibility", // 10
		"extended",                 // 11
		"isfreeapp",                // 12
		"developer",                // 13
		"publisher",                // 14
	}

	buildAppVDF := func(name string, primaryGenre int32, deckCat int32, isFree int32, dev, pub string) []byte {
		var b bytes.Buffer
		writeKey := func(typ byte, idx uint32) {
			b.WriteByte(typ)
			_ = binary.Write(&b, binary.LittleEndian, idx)
		}
		writeKey(0x00, 0) // appinfo
		writeKey(0x00, 1) // common
		writeKey(0x01, 2) // name = string
		b.WriteString(name + "\x00")
		writeKey(0x02, 4) // primary_genre = int32
		_ = binary.Write(&b, binary.LittleEndian, primaryGenre)
		writeKey(0x00, 7) // category
		writeKey(0x02, 8) // category_2 = 1 (Single-player)
		_ = binary.Write(&b, binary.LittleEndian, int32(1))
		writeKey(0x02, 9) // category_28 = 1 (Full controller support)
		_ = binary.Write(&b, binary.LittleEndian, int32(1))
		b.WriteByte(0x08)  // end category
		writeKey(0x00, 10) // steam_deck_compatibility
		writeKey(0x02, 7)  // "category" (idx 7) = deckCat
		_ = binary.Write(&b, binary.LittleEndian, deckCat)
		b.WriteByte(0x08)  // end steam_deck_compatibility
		b.WriteByte(0x08)  // end common
		writeKey(0x00, 11) // extended
		writeKey(0x02, 12) // isfreeapp = isFree
		_ = binary.Write(&b, binary.LittleEndian, isFree)
		writeKey(0x01, 13) // developer
		b.WriteString(dev + "\x00")
		writeKey(0x01, 14) // publisher
		b.WriteString(pub + "\x00")
		b.WriteByte(0x08) // end extended
		b.WriteByte(0x08) // end appinfo
		b.WriteByte(0x08) // end root
		return b.Bytes()
	}

	var entriesBuf bytes.Buffer
	writeAppEntry := func(appID uint32, vdfBytes []byte) {
		_ = binary.Write(&entriesBuf, binary.LittleEndian, appID)
		_ = binary.Write(&entriesBuf, binary.LittleEndian, uint32(60+len(vdfBytes)))
		entriesBuf.Write(make([]byte, 60)) // 60-byte entry header
		entriesBuf.Write(vdfBytes)
	}

	writeAppEntry(4480260, buildAppVDF("ChallaChat", 37, 2, 1, "Challacade", "Challacade"))
	writeAppEntry(1086940, buildAppVDF("Baldur's Gate 3", 3, 3, 0, "Larian Studios", "Larian Studios"))
	_ = binary.Write(&entriesBuf, binary.LittleEndian, uint32(0)) // sentinel appID 0

	var appFileBuf bytes.Buffer
	strTableOffset := uint64(16 + entriesBuf.Len())
	_ = binary.Write(&appFileBuf, binary.LittleEndian, uint32(0x07564429))
	_ = binary.Write(&appFileBuf, binary.LittleEndian, uint32(1))
	_ = binary.Write(&appFileBuf, binary.LittleEndian, strTableOffset)
	appFileBuf.Write(entriesBuf.Bytes())

	_ = binary.Write(&appFileBuf, binary.LittleEndian, uint32(len(strTable)))
	for _, s := range strTable {
		appFileBuf.WriteString(s + "\x00")
	}

	if err := os.WriteFile(filepath.Join(appcacheDir, "appinfo.vdf"), appFileBuf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}

	items := map[uint32]models.LibraryItem{
		4480260: {AppID: 4480260, Name: "App 4480260", AcquisitionType: models.AcquisitionUnknown},
		1086940: {AppID: 1086940, Name: "App 1086940", AcquisitionType: models.AcquisitionUnknown},
	}

	count := EnrichFromLocalSteamCache(items, tmpSteam)
	if count != 2 {
		t.Fatalf("expected 2 enriched apps from local VDF cache, got %d", count)
	}

	challa := items[4480260]
	if challa.Name != "ChallaChat" || !challa.IsFree || challa.AcquisitionType != models.AcquisitionFree {
		t.Errorf("expected ChallaChat to be resolved as Free from local VDF, got: %+v", challa)
	}
	if challa.DeckCompatibility != models.DeckPlayable || len(challa.Developers) == 0 || challa.Developers[0] != "Challacade" {
		t.Errorf("unexpected ChallaChat metadata from local VDF: %+v", challa)
	}

	bg3 := items[1086940]
	if bg3.Name != "Baldur's Gate 3" || bg3.IsFree || bg3.AcquisitionType != models.AcquisitionRetailKey {
		t.Errorf("expected Baldur's Gate 3 to be resolved as RetailKey from packageinfo.vdf, got: %+v", bg3)
	}
	if bg3.DeckCompatibility != models.DeckVerified {
		t.Errorf("expected Baldur's Gate 3 DeckVerified, got %s", bg3.DeckCompatibility)
	}
}

func TestBulkEnrichStoreItemsRetriesOnHTTP429(t *testing.T) {
	var attempts atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/IStoreBrowseService/GetItems/v1/" {
			c := attempts.Add(1)
			if c == 1 {
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			_, _ = fmt.Fprint(w, `{
				"response": {
					"store_items": [
						{"appid": 4480260, "name": "ChallaChat", "is_free": true}
					]
				}
			}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	sess := &auth.Session{
		SteamID64:        "76561198000000001",
		SteamLoginSecure: "76561198000000001||token",
		WebAPIToken:      "token",
		ExpiresAt:        time.Now().Add(time.Hour),
	}
	client := NewClient(sess)
	client.SetHTTPClient(newHandlerHTTPClient(handler))

	items := map[uint32]models.LibraryItem{
		4480260: {AppID: 4480260, Name: "App 4480260", AcquisitionType: models.AcquisitionUnknown},
	}
	client.bulkEnrichStoreItems(t.Context(), items)

	if attempts.Load() < 2 {
		t.Fatalf("expected HTTP 429 retry, got %d attempt(s)", attempts.Load())
	}
	if items[4480260].Name != "ChallaChat" || !items[4480260].IsFree {
		t.Errorf("expected ChallaChat to be enriched after HTTP 429 retry, got %+v", items[4480260])
	}
	if client.Session() != sess {
		t.Errorf("expected client.Session() to return sess")
	}
}

func TestLocalVDFInlineV28AndHelpers(t *testing.T) {
	tmpSteam := t.TempDir()
	appcacheDir := filepath.Join(tmpSteam, "appcache")
	if err := os.MkdirAll(appcacheDir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STEAM_PATH", tmpSteam)
	if got := detectLocalSteamDir(); got != tmpSteam {
		t.Errorf("expected detectLocalSteamDir to return STEAM_PATH %q, got %q", tmpSteam, got)
	}

	// Test vdfAsInt across all supported types
	if vdfAsInt(int(42)) != 42 || vdfAsInt(int32(7)) != 7 || vdfAsInt(uint64(99)) != 99 || vdfAsInt("123") != 123 || vdfAsInt(nil) != 0 {
		t.Errorf("unexpected vdfAsInt conversions")
	}

	// Build v27/v28 packageinfo.vdf (magic 0x06565527 -> 24-byte header skip) with billingtype=1 (paid) and pkg 0
	var pkgBuf bytes.Buffer
	_ = binary.Write(&pkgBuf, binary.LittleEndian, uint32(0x06565527))
	_ = binary.Write(&pkgBuf, binary.LittleEndian, uint32(1))

	// Package 0 (should be skipped)
	_ = binary.Write(&pkgBuf, binary.LittleEndian, uint32(0))
	pkgBuf.Write(make([]byte, 24))
	pkgBuf.WriteByte(0x08)

	// Package 555: billingtype=1 (paid), extended.freepromotion=0, appids={0: 777}
	_ = binary.Write(&pkgBuf, binary.LittleEndian, uint32(555))
	pkgBuf.Write(make([]byte, 24))
	pkgBuf.WriteByte(0x02)
	pkgBuf.WriteString("billingtype\x00")
	_ = binary.Write(&pkgBuf, binary.LittleEndian, int32(1))
	pkgBuf.WriteByte(0x06) // 64-bit field test
	pkgBuf.WriteString("token64\x00")
	_ = binary.Write(&pkgBuf, binary.LittleEndian, uint64(123456))
	pkgBuf.WriteByte(0x00)
	pkgBuf.WriteString("appids\x00")
	pkgBuf.WriteByte(0x02)
	pkgBuf.WriteString("0\x00")
	_ = binary.Write(&pkgBuf, binary.LittleEndian, int32(777))
	pkgBuf.WriteByte(0x08)
	pkgBuf.WriteByte(0x08)
	_ = binary.Write(&pkgBuf, binary.LittleEndian, uint32(0xFFFFFFFF))

	if err := os.WriteFile(filepath.Join(appcacheDir, "packageinfo.vdf"), pkgBuf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}

	// Build inline appinfo.vdf (v28: 0x07564428 without string table) for AppID 777
	var vdfB bytes.Buffer
	vdfB.WriteByte(0x00)
	vdfB.WriteString("appinfo\x00")
	vdfB.WriteByte(0x00)
	vdfB.WriteString("common\x00")
	vdfB.WriteByte(0x01)
	vdfB.WriteString("name\x00Paid Inline Game\x00")
	// genres map with genre 1 (Action)
	vdfB.WriteByte(0x00)
	vdfB.WriteString("genres\x00")
	vdfB.WriteByte(0x01)
	vdfB.WriteString("0\x001\x00")
	vdfB.WriteByte(0x08)
	// associations map with developer & publisher (and duplicate developer to test appendUniqueStr)
	vdfB.WriteByte(0x00)
	vdfB.WriteString("associations\x00")
	vdfB.WriteByte(0x00)
	vdfB.WriteString("0\x00")
	vdfB.WriteByte(0x01)
	vdfB.WriteString("type\x00developer\x00")
	vdfB.WriteByte(0x01)
	vdfB.WriteString("name\x00InlineDev\x00")
	vdfB.WriteByte(0x08)
	vdfB.WriteByte(0x00)
	vdfB.WriteString("1\x00")
	vdfB.WriteByte(0x01)
	vdfB.WriteString("type\x00publisher\x00")
	vdfB.WriteByte(0x01)
	vdfB.WriteString("name\x00InlinePub\x00")
	vdfB.WriteByte(0x08)
	vdfB.WriteByte(0x08)
	// steam_deck_compatibility = 1 (Unsupported)
	vdfB.WriteByte(0x00)
	vdfB.WriteString("steam_deck_compatibility\x00")
	vdfB.WriteByte(0x02)
	vdfB.WriteString("category\x00")
	_ = binary.Write(&vdfB, binary.LittleEndian, int32(1))
	vdfB.WriteByte(0x08)
	vdfB.WriteByte(0x08) // end common
	vdfB.WriteByte(0x00)
	vdfB.WriteString("extended\x00")
	vdfB.WriteByte(0x01)
	vdfB.WriteString("developer\x00InlineDev\x00")
	vdfB.WriteByte(0x08) // end extended
	vdfB.WriteByte(0x08) // end appinfo
	vdfB.WriteByte(0x08) // end root

	var appFile bytes.Buffer
	_ = binary.Write(&appFile, binary.LittleEndian, uint32(0x07564428))
	_ = binary.Write(&appFile, binary.LittleEndian, uint32(1))
	_ = binary.Write(&appFile, binary.LittleEndian, uint32(777))
	_ = binary.Write(&appFile, binary.LittleEndian, uint32(60+vdfB.Len()))
	appFile.Write(make([]byte, 60))
	appFile.Write(vdfB.Bytes())
	_ = binary.Write(&appFile, binary.LittleEndian, uint32(0))

	if err := os.WriteFile(filepath.Join(appcacheDir, "appinfo.vdf"), appFile.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}

	items := map[uint32]models.LibraryItem{
		777: {AppID: 777, Name: "App 777", AcquisitionType: models.AcquisitionUnknown},
	}
	if n := EnrichFromLocalSteamCache(items, ""); n != 1 {
		t.Fatalf("expected 1 enriched item from STEAM_PATH v28 cache, got %d", n)
	}
	got := items[777]
	if got.Name != "Paid Inline Game" || got.AcquisitionType != models.AcquisitionPurchase || got.DeckCompatibility != models.DeckUnsupported {
		t.Errorf("unexpected v28 inline item enrichment: %+v", got)
	}
	if len(got.Developers) != 1 || got.Developers[0] != "InlineDev" || len(got.Publishers) != 1 || got.Publishers[0] != "InlinePub" {
		t.Errorf("unexpected associations dev/pub: %+v", got)
	}
}


