package collections

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/csull/go-steam-organizer/internal/auth"
	"github.com/csull/go-steam-organizer/internal/steam"
)

const sampleCloudStorageJSON = `[
  ["showcase-1", {"key":"showcase-1","timestamp":1700000000,"value":"{\"test\":true}","version":"10"}],
  ["user-collections.uc-existing", {"key":"user-collections.uc-existing","timestamp":1700000100,"value":"{\"id\":\"uc-existing\",\"name\":\"Favorites\",\"added\":[440],\"removed\":[]}","version":"12","conflictResolutionMethod":"custom","strMethodId":"union-collections"}]
]`

func TestCollectionManagerLoadAddRemoveSave(t *testing.T) {
	tmpDir := t.TempDir()
	csFile := filepath.Join(tmpDir, "cloud-storage-namespace-1.json")
	if err := os.WriteFile(csFile, []byte(sampleCloudStorageJSON), 0644); err != nil {
		t.Fatal(err)
	}

	mgr, err := LoadManager(csFile)
	if err != nil {
		t.Fatalf("LoadManager failed: %v", err)
	}

	cols := mgr.ListCollections()
	if len(cols) != 1 || cols[0].Name != "Favorites" || !cols[0].ContainsApp(440) {
		t.Fatalf("unexpected initial collections: %+v", cols)
	}

	// Add apps to existing collection and create a new collection
	added, favCol := mgr.AddAppsToCollection("Favorites", []uint32{440, 620, 730})
	if added != 2 || len(favCol.Added) != 3 {
		t.Errorf("expected 2 newly added apps (3 total), got added=%d, total=%d", added, len(favCol.Added))
	}

	addedNew, newCol := mgr.AddAppsToCollection("Bought in 2025", []uint32{1145350})
	if addedNew != 1 || newCol.Name != "Bought in 2025" {
		t.Errorf("unexpected new collection: %+v", newCol)
	}

	// Remove app 440 from Favorites
	removed, err := mgr.RemoveAppsFromCollection("Favorites", []uint32{440})
	if err != nil || removed != 1 {
		t.Errorf("expected 1 removed app, got %d (err=%v)", removed, err)
	}

	if _, err := mgr.RemoveAppsFromCollection("NonExistent", []uint32{440}); err == nil {
		t.Errorf("expected error removing from nonexistent collection")
	}

	backupPath, err := mgr.Save()
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	if backupPath == "" {
		t.Errorf("expected backup file to be created")
	}

	// Reload and verify persistence + preservation of non-collection entries
	reloaded, err := LoadManager(csFile)
	if err != nil {
		t.Fatalf("Reload failed: %v", err)
	}
	if len(reloaded.rawEntries) != 3 {
		t.Errorf("expected 3 total raw entries (1 showcase + 2 collections), got %d", len(reloaded.rawEntries))
	}
	favReloaded := reloaded.FindByNameOrID("Favorites")
	if favReloaded == nil || favReloaded.ContainsApp(440) || !favReloaded.ContainsApp(620) {
		t.Errorf("unexpected Favorites state after reload: %+v", favReloaded)
	}
}

func TestLocateCloudStorageFile(t *testing.T) {
	tmpSteam := t.TempDir()
	accDir := filepath.Join(tmpSteam, "userdata", "123456", "config", "cloudstorage")
	if err := os.MkdirAll(accDir, 0755); err != nil {
		t.Fatal(err)
	}
	targetFile := filepath.Join(accDir, "cloud-storage-namespace-1.json")
	if err := os.WriteFile(targetFile, []byte("[]"), 0644); err != nil {
		t.Fatal(err)
	}

	// 1. Explicit path
	got, err := LocateCloudStorageFile(targetFile, "", 0)
	if err != nil || got != targetFile {
		t.Errorf("expected explicit path %q, got %q (err=%v)", targetFile, got, err)
	}

	// 2. By steamDirOverride + accountID
	got, err = LocateCloudStorageFile("", tmpSteam, 123456)
	if err != nil || got != targetFile {
		t.Errorf("expected accountID path %q, got %q (err=%v)", targetFile, got, err)
	}

	// 3. Fallback scan when accountID is 0
	got, err = LocateCloudStorageFile("", tmpSteam, 0)
	if err != nil || got != targetFile {
		t.Errorf("expected fallback scan path %q, got %q (err=%v)", targetFile, got, err)
	}
}

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestSyncWithCloud(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{
			"response": {
				"data": [
					{
						"enamespace": 1,
						"version": "50",
						"entries": [
							{
								"key": "user-collections.uc-cloud1",
								"timestamp": 1710000000,
								"value": "{\"id\":\"uc-cloud1\",\"name\":\"Cloud Synced\",\"added\":[570],\"removed\":[]}",
								"version": "50",
								"deleted": false
							}
						]
					}
				]
			}
		}`)
	})

	tmpFile := filepath.Join(t.TempDir(), "cloud-storage-namespace-1.json")
	mgr, err := LoadManager(tmpFile)
	if err != nil {
		t.Fatal(err)
	}

	sess := &auth.Session{
		SteamID64:        "76561198000000001",
		AccountID:        39734273,
		SteamLoginSecure: "76561198000000001||tok",
		WebAPIToken:      "tok",
		ExpiresAt:        time.Now().Add(time.Hour),
	}
	client := steam.NewClient(sess)
	client.SetHTTPClient(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			return rec.Result(), nil
		}),
	})

	if err := mgr.SyncWithCloud(t.Context(), client); err != nil {
		t.Fatalf("SyncWithCloud failed: %v", err)
	}
	if col := mgr.FindByNameOrID("Cloud Synced"); col == nil || !col.ContainsApp(570) {
		t.Errorf("expected 'Cloud Synced' collection with AppID 570 after cloud sync, got %+v", col)
	}

	if err := mgr.SyncWithCloud(t.Context(), nil); err == nil {
		t.Errorf("expected error when client is nil")
	}
}
