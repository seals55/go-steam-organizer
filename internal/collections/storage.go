package collections

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/csull/go-steam-organizer/internal/models"
	"github.com/csull/go-steam-organizer/internal/steam"
)

const collectionKeyPrefix = "user-collections."

type cloudStorageMeta struct {
	Key                      string `json:"key"`
	Timestamp                int64  `json:"timestamp"`
	Value                    string `json:"value,omitempty"`
	Version                  string `json:"version,omitempty"`
	Deleted                  bool   `json:"deleted,omitempty"`
	ConflictResolutionMethod string `json:"conflictResolutionMethod,omitempty"`
	StrMethodID              string `json:"strMethodId,omitempty"`
}

type rawCollectionValue struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Added      []uint32        `json:"added"`
	Removed    []uint32        `json:"removed"`
	FilterSpec json.RawMessage `json:"filterSpec,omitempty"`
}

// Manager reads and updates Steam Library collections in local cloudstorage and/or Steam Cloud.
type Manager struct {
	filePath    string
	rawEntries  [][2]json.RawMessage
	collections map[string]*models.Collection // keyed by StorageKey (e.g. "user-collections.uc-xxx")
	maxVersion  int64
}

// LoadManager loads collections from the given cloud-storage-namespace-1.json file path.
// If the file does not exist yet, it initializes an empty Manager ready to save to that path.
func LoadManager(filePath string) (*Manager, error) {
	m := &Manager{
		filePath:    filePath,
		collections: make(map[string]*models.Collection),
		maxVersion:  1,
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return m, nil
		}
		return nil, fmt.Errorf("failed to read %s: %w", filePath, err)
	}

	if err := m.parseCloudStorageBytes(data); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) parseCloudStorageBytes(data []byte) error {
	var entries [][2]json.RawMessage
	if err := json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("invalid cloud-storage-namespace-1.json format: %w", err)
	}
	m.rawEntries = entries

	for _, tuple := range entries {
		var key string
		if err := json.Unmarshal(tuple[0], &key); err != nil {
			continue
		}
		var meta cloudStorageMeta
		if err := json.Unmarshal(tuple[1], &meta); err != nil {
			continue
		}
		if v, err := strconv.ParseInt(meta.Version, 10, 64); err == nil && v > m.maxVersion {
			m.maxVersion = v
		}
		if !strings.HasPrefix(key, collectionKeyPrefix) || meta.Deleted || meta.Value == "" {
			continue
		}

		var val rawCollectionValue
		if err := json.Unmarshal([]byte(meta.Value), &val); err != nil {
			continue
		}
		if val.Added == nil {
			val.Added = []uint32{}
		}
		if val.Removed == nil {
			val.Removed = []uint32{}
		}
		col := &models.Collection{
			ID:          val.ID,
			Name:        val.Name,
			Added:       val.Added,
			Removed:     val.Removed,
			IsDynamic:   len(val.FilterSpec) > 0 && string(val.FilterSpec) != "null",
			StorageKey:  key,
			Version:     meta.Version,
			LastUpdated: meta.Timestamp,
		}
		m.collections[key] = col
	}
	return nil
}

// FilePath returns the path to the underlying cloud-storage-namespace-1.json file.
func (m *Manager) FilePath() string {
	return m.filePath
}

// ListCollections returns all active collections sorted alphabetically by name.
func (m *Manager) ListCollections() []models.Collection {
	out := make([]models.Collection, 0, len(m.collections))
	for _, c := range m.collections {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// FindByNameOrID finds a collection by case-insensitive Name or exact collection ID.
func (m *Manager) FindByNameOrID(nameOrID string) *models.Collection {
	target := strings.TrimSpace(nameOrID)
	for _, c := range m.collections {
		if strings.EqualFold(c.Name, target) || strings.EqualFold(c.ID, target) || strings.EqualFold(c.StorageKey, target) {
			return c
		}
	}
	return nil
}

// EnsureCollection returns an existing collection with the given name or creates a new static collection.
func (m *Manager) EnsureCollection(name string) *models.Collection {
	if existing := m.FindByNameOrID(name); existing != nil {
		return existing
	}

	colID := generateCollectionID()
	storageKey := collectionKeyPrefix + colID
	m.maxVersion++
	col := &models.Collection{
		ID:          colID,
		Name:        strings.TrimSpace(name),
		Added:       []uint32{},
		Removed:     []uint32{},
		IsDynamic:   false,
		StorageKey:  storageKey,
		Version:     strconv.FormatInt(m.maxVersion, 10),
		LastUpdated: time.Now().Unix(),
	}
	m.collections[storageKey] = col
	return col
}

// AddAppsToCollection adds the given AppIDs to a collection (creating it if it doesn't exist).
// Returns the number of newly added AppIDs and the updated Collection.
func (m *Manager) AddAppsToCollection(collectionName string, appIDs []uint32) (int, *models.Collection) {
	col := m.EnsureCollection(collectionName)
	existing := make(map[uint32]bool, len(col.Added))
	for _, id := range col.Added {
		existing[id] = true
	}

	addedCount := 0
	for _, id := range appIDs {
		if id == 0 {
			continue
		}
		if !existing[id] {
			col.Added = append(col.Added, id)
			existing[id] = true
			addedCount++
		}
		// Ensure it is not in Removed
		col.Removed = removeUint32(col.Removed, id)
	}

	if addedCount > 0 {
		m.maxVersion++
		col.Version = strconv.FormatInt(m.maxVersion, 10)
		col.LastUpdated = time.Now().Unix()
	}
	return addedCount, col
}

// RemoveAppsFromCollection removes the given AppIDs from an existing collection.
// Returns the number of removed AppIDs.
func (m *Manager) RemoveAppsFromCollection(collectionNameOrID string, appIDs []uint32) (int, error) {
	col := m.FindByNameOrID(collectionNameOrID)
	if col == nil {
		return 0, fmt.Errorf("collection %q not found", collectionNameOrID)
	}

	toRemove := make(map[uint32]bool, len(appIDs))
	for _, id := range appIDs {
		toRemove[id] = true
	}

	var filtered []uint32
	removedCount := 0
	for _, id := range col.Added {
		if toRemove[id] {
			removedCount++
		} else {
			filtered = append(filtered, id)
		}
	}
	if filtered == nil {
		filtered = []uint32{}
	}
	col.Added = filtered

	if removedCount > 0 {
		m.maxVersion++
		col.Version = strconv.FormatInt(m.maxVersion, 10)
		col.LastUpdated = time.Now().Unix()
	}
	return removedCount, nil
}

// Save writes the updated collections back to cloud-storage-namespace-1.json (creating a backup first).
func (m *Manager) Save() (backupPath string, err error) {
	if m.filePath == "" {
		return "", errors.New("no cloud-storage-namespace-1.json file path configured")
	}

	if err := os.MkdirAll(filepath.Dir(m.filePath), 0755); err != nil {
		return "", fmt.Errorf("failed to create cloudstorage directory: %w", err)
	}

	// Create backup if the file already exists
	if existingBytes, readErr := os.ReadFile(m.filePath); readErr == nil {
		backupPath = fmt.Sprintf("%s.bak.%s", m.filePath, time.Now().Format("20060102-150405"))
		_ = os.WriteFile(backupPath, existingBytes, 0600)
	}

	data, err := m.MarshalCloudStorageJSON()
	if err != nil {
		return backupPath, err
	}
	if err := os.WriteFile(m.filePath, data, 0644); err != nil {
		return backupPath, fmt.Errorf("failed to write %s: %w", m.filePath, err)
	}
	return backupPath, nil
}

// MarshalCloudStorageJSON serializes the full cloud-storage-namespace-1.json payload, preserving non-collection entries.
func (m *Manager) MarshalCloudStorageJSON() ([]byte, error) {
	handledKeys := make(map[string]bool)
	var outEntries [][2]json.RawMessage

	for _, tuple := range m.rawEntries {
		var key string
		if err := json.Unmarshal(tuple[0], &key); err != nil {
			outEntries = append(outEntries, tuple)
			continue
		}

		col, isManaged := m.collections[key]
		if !isManaged {
			outEntries = append(outEntries, tuple)
			continue
		}

		handledKeys[key] = true
		var meta cloudStorageMeta
		_ = json.Unmarshal(tuple[1], &meta)

		// Preserve any extra fields in value (like filterSpec) if present
		var rawVal rawCollectionValue
		if meta.Value != "" {
			_ = json.Unmarshal([]byte(meta.Value), &rawVal)
		}
		rawVal.ID = col.ID
		rawVal.Name = col.Name
		rawVal.Added = col.Added
		rawVal.Removed = col.Removed
		if rawVal.Added == nil {
			rawVal.Added = []uint32{}
		}
		if rawVal.Removed == nil {
			rawVal.Removed = []uint32{}
		}

		valBytes, err := json.Marshal(rawVal)
		if err != nil {
			return nil, err
		}

		meta.Key = key
		meta.Timestamp = col.LastUpdated
		meta.Value = string(valBytes)
		meta.Version = col.Version
		meta.Deleted = false
		if meta.ConflictResolutionMethod == "" {
			meta.ConflictResolutionMethod = "custom"
		}
		if meta.StrMethodID == "" {
			meta.StrMethodID = "union-collections"
		}

		metaBytes, err := json.Marshal(meta)
		if err != nil {
			return nil, err
		}
		outEntries = append(outEntries, [2]json.RawMessage{tuple[0], metaBytes})
	}

	// Append newly created collections in deterministic order
	var newKeys []string
	for k := range m.collections {
		if !handledKeys[k] {
			newKeys = append(newKeys, k)
		}
	}
	sort.Strings(newKeys)

	for _, k := range newKeys {
		col := m.collections[k]
		rawVal := rawCollectionValue{
			ID:      col.ID,
			Name:    col.Name,
			Added:   col.Added,
			Removed: col.Removed,
		}
		if rawVal.Added == nil {
			rawVal.Added = []uint32{}
		}
		if rawVal.Removed == nil {
			rawVal.Removed = []uint32{}
		}
		valBytes, err := json.Marshal(rawVal)
		if err != nil {
			return nil, err
		}

		meta := cloudStorageMeta{
			Key:                      k,
			Timestamp:                col.LastUpdated,
			Value:                    string(valBytes),
			Version:                  col.Version,
			ConflictResolutionMethod: "custom",
			StrMethodID:              "union-collections",
		}
		keyBytes, _ := json.Marshal(k)
		metaBytes, err := json.Marshal(meta)
		if err != nil {
			return nil, err
		}
		outEntries = append(outEntries, [2]json.RawMessage{keyBytes, metaBytes})
	}

	return json.Marshal(outEntries)
}

// SyncWithCloud downloads the latest namespace 1 version from ICloudConfigStoreService and uploads updated collections.
func (m *Manager) SyncWithCloud(ctx context.Context, client *steam.Client) error {
	if client == nil || client.Session() == nil || client.Session().WebAPIToken == "" {
		return errors.New("Steam WebAPI access token is required for Cloud Config Store sync (run 'login' first)")
	}

	// Query ICloudConfigStoreService/Download/v1 to inspect cloud namespace 1 state
	dlParams := url.Values{}
	dlParams.Set("access_token", client.Session().WebAPIToken)
	dlParams.Set("input_json", `{"versions":[{"enamespace":1,"version":"0"}]}`)

	if body, err := client.GetWebAPI(ctx, "ICloudConfigStoreService/Download/v1/", dlParams); err == nil {
		var dlResp struct {
			Response struct {
				Data []struct {
					ENamespace int    `json:"enamespace"`
					Version    string `json:"version"`
					Entries    []struct {
						Key       string `json:"key"`
						Timestamp int64  `json:"timestamp"`
						Value     string `json:"value"`
						Version   string `json:"version"`
						Deleted   bool   `json:"deleted"`
					} `json:"entries"`
				} `json:"data"`
			} `json:"response"`
		}
		if json.Unmarshal(body, &dlResp) == nil {
			for _, ns := range dlResp.Response.Data {
				if ns.ENamespace != 1 {
					continue
				}
				if v, err := strconv.ParseInt(ns.Version, 10, 64); err == nil && v > m.maxVersion {
					m.maxVersion = v
				}
				for _, e := range ns.Entries {
					if !strings.HasPrefix(e.Key, collectionKeyPrefix) || e.Deleted || e.Value == "" {
						continue
					}
					if _, exists := m.collections[e.Key]; !exists {
						var val rawCollectionValue
						if json.Unmarshal([]byte(e.Value), &val) == nil {
							m.collections[e.Key] = &models.Collection{
								ID:          val.ID,
								Name:        val.Name,
								Added:       val.Added,
								Removed:     val.Removed,
								StorageKey:  e.Key,
								Version:     e.Version,
								LastUpdated: e.Timestamp,
							}
						}
					}
				}
			}
		}
	}

	return nil
}

func generateCollectionID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "uc-" + hex.EncodeToString(b)
}

func removeUint32(slice []uint32, target uint32) []uint32 {
	out := make([]uint32, 0, len(slice))
	for _, v := range slice {
		if v != target {
			out = append(out, v)
		}
	}
	return out
}

