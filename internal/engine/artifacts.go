package engine

import (
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/kiwi-init/greenrun/internal/model"
)

func CollectArtifacts(root string) []model.Artifact {
	var values []model.Artifact
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		info, statErr := entry.Info()
		if statErr != nil {
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		parts := strings.Split(filepath.ToSlash(relative), "/")
		name := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		var id int64
		if len(parts) >= 3 {
			id, _ = strconv.ParseInt(parts[0], 10, 64)
			name = parts[len(parts)-2]
		}
		values = append(values, model.Artifact{
			ID:           id,
			Name:         name,
			SizeBytes:    info.Size(),
			CreatedAt:    info.ModTime().UTC(),
			DownloadedTo: path,
		})
		return nil
	})
	sort.Slice(values, func(i, j int) bool {
		if values[i].Name == values[j].Name {
			return values[i].DownloadedTo < values[j].DownloadedTo
		}
		return values[i].Name < values[j].Name
	})
	return values
}
