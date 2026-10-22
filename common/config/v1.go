package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/GitJournal/git-auto-sync/common"
	"github.com/kirsle/configdir"
	"github.com/ztrue/tracerr"
)

type ConfigV1 struct {
	Repos []string `json:"repos"`
	Envs  []string `json:"envs"`
}

func configFilePath() (string, error) {
	configPath := configdir.LocalConfig("git-auto-sync")
	if err := configdir.MakePath(configPath); err != nil {
		return "", tracerr.Wrap(err)
	}
	return filepath.Join(configPath, "config.json"), nil
}

func ReadV1() (*ConfigV1, error) {
	configFile, err := configFilePath()
	if err != nil {
		return nil, err
	}

	config := ConfigV1{}
	data, err := os.ReadFile(configFile)
	if os.IsNotExist(err) {
		return &config, nil
	}
	if err != nil {
		return nil, tracerr.Wrap(err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return &config, nil
	}

	if err := json.Unmarshal(data, &config); err != nil {
		return nil, tracerr.Wrap(err)
	}

	config.Repos = NormalizeRepos(config.Repos)
	return &config, nil
}

func WriteV1(config *ConfigV1) error {
	configFile, err := configFilePath()
	if err != nil {
		return err
	}

	out := *config
	out.Repos = NormalizeRepos(config.Repos)

	data, err := json.Marshal(&out)
	if err != nil {
		return tracerr.Wrap(err)
	}
	data = append(data, '\n')

	if err := common.WriteFileAtomic(configFile, data, 0o644); err != nil {
		return tracerr.Wrap(err)
	}
	return nil
}

func NormalizeRepos(repos []string) []string {
	out := make([]string, 0, len(repos))
	seen := make(map[string]bool, len(repos))
	for _, rp := range repos {
		if rp == "" {
			continue
		}
		clean := filepath.Clean(rp)
		if seen[clean] {
			continue
		}
		seen[clean] = true
		out = append(out, clean)
	}
	return out
}
