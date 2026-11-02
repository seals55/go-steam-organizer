package collections

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// LocateCloudStorageFile finds the path to Steam's cloud-storage-namespace-1.json for the given AccountID.
// If accountID is 0, it searches userdata/ for the most recently modified cloud-storage-namespace-1.json.
func LocateCloudStorageFile(explicitPath string, steamDirOverride string, accountID uint32) (string, error) {
	if explicitPath != "" {
		return explicitPath, nil
	}
	if envFile := os.Getenv("STEAM_CLOUDSTORAGE_FILE"); envFile != "" {
		return envFile, nil
	}

	steamDir := steamDirOverride
	if steamDir == "" {
		steamDir = os.Getenv("STEAM_PATH")
	}
	if steamDir == "" {
		var err error
		steamDir, err = detectSteamInstallDir()
		if err != nil {
			return "", err
		}
	}

	userdataDir := filepath.Join(steamDir, "userdata")
	if accountID > 0 {
		accStr := strconv.FormatUint(uint64(accountID), 10)
		target := filepath.Join(userdataDir, accStr, "config", "cloudstorage", "cloud-storage-namespace-1.json")
		if _, err := os.Stat(target); err == nil {
			return target, nil
		}
		// Even if the file doesn't exist yet, if the userdata/<accountID> directory exists, return the target path
		accDir := filepath.Join(userdataDir, accStr)
		if info, err := os.Stat(accDir); err == nil && info.IsDir() {
			return target, nil
		}
	}

	// Fallback: scan userdata/* for cloud-storage-namespace-1.json and pick the most recently modified
	entries, err := os.ReadDir(userdataDir)
	if err != nil {
		return "", fmt.Errorf("failed to read Steam userdata directory %q: %w", userdataDir, err)
	}

	var bestPath string
	var bestMod int64
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "0" || entry.Name() == "ac" {
			continue
		}
		candidate := filepath.Join(userdataDir, entry.Name(), "config", "cloudstorage", "cloud-storage-namespace-1.json")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			if info.ModTime().UnixNano() > bestMod {
				bestMod = info.ModTime().UnixNano()
				bestPath = candidate
			}
		}
	}

	if bestPath != "" {
		return bestPath, nil
	}
	if accountID > 0 {
		return filepath.Join(userdataDir, strconv.FormatUint(uint64(accountID), 10), "config", "cloudstorage", "cloud-storage-namespace-1.json"), nil
	}
	return "", errors.New("could not find any Steam userdata account directory; specify --steam-path or log in first")
}

func detectSteamInstallDir() (string, error) {
	var candidates []string

	switch runtime.GOOS {
	case "windows":
		// Query Windows registry HKCU\Software\Valve\Steam -> SteamPath
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
		if info, err := os.Stat(filepath.Join(c, "userdata")); err == nil && info.IsDir() {
			return c, nil
		}
	}

	return "", errors.New("could not auto-detect Steam installation directory; set STEAM_PATH or pass --steam-path")
}

