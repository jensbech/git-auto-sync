package common

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultPollInterval = 10 * time.Minute
	MinPollInterval     = 30 * time.Second
	MaxPollInterval     = 24 * time.Hour
	MaxBatchWindow      = 24 * time.Hour
)

const (
	SettingPollInterval  = "pollInterval"
	SettingBatchWindow   = "batchWindow"
	SettingExec          = "exec"
	SettingMessagePrefix = "messagePrefix"
)

var settingGitKeys = map[string]string{
	SettingPollInterval:  "syncInterval",
	SettingBatchWindow:   "batchWindow",
	SettingExec:          "exec",
	SettingMessagePrefix: "messagePrefix",
}

func SettingGitKey(setting string) (string, bool) {
	k, ok := settingGitKeys[setting]
	return k, ok
}

func SettingNames() []string {
	return []string{SettingPollInterval, SettingBatchWindow, SettingExec, SettingMessagePrefix}
}

func ReadAutoSyncGitConfig(repoPath string) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "config", "-z", "--get-regexp", `^auto-sync\.`)
	cmd.Dir = repoPath
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := map[string]string{}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 && stderr.Len() == 0 {
			return out, nil
		}
		return nil, fmt.Errorf("git config: %v: %s", err, strings.TrimSpace(stderr.String()))
	}

	for _, rec := range strings.Split(stdout.String(), "\x00") {
		if rec == "" {
			continue
		}
		key, val, _ := strings.Cut(rec, "\n")
		key = strings.TrimPrefix(strings.ToLower(key), "auto-sync.")
		out[key] = val
	}
	return out, nil
}

func gitConfigValue(raw map[string]string, gitKey string) (string, bool) {
	v, ok := raw[strings.ToLower(gitKey)]
	return strings.TrimSpace(v), ok
}

type RepoSettings struct {
	PollInterval  time.Duration
	BatchWindow   time.Duration
	GitExec       string
	MessagePrefix string
}

func ParseRepoSettings(raw map[string]string) (RepoSettings, []string) {
	var warnings []string
	s := RepoSettings{PollInterval: DefaultPollInterval}

	if v, ok := gitConfigValue(raw, "syncInterval"); ok && v != "" {
		secs, err := strconv.Atoi(v)
		switch {
		case err != nil:
			warnings = append(warnings, fmt.Sprintf("auto-sync.syncInterval=%q is not a number, using default %ds", v, int(DefaultPollInterval/time.Second)))
		case time.Duration(secs)*time.Second < MinPollInterval:
			s.PollInterval = MinPollInterval
			warnings = append(warnings, fmt.Sprintf("auto-sync.syncInterval=%d is below the minimum, using %ds", secs, int(MinPollInterval/time.Second)))
		case time.Duration(secs)*time.Second > MaxPollInterval:
			s.PollInterval = MaxPollInterval
			warnings = append(warnings, fmt.Sprintf("auto-sync.syncInterval=%d is above the maximum, using %ds", secs, int(MaxPollInterval/time.Second)))
		default:
			s.PollInterval = time.Duration(secs) * time.Second
		}
	}

	if v, ok := gitConfigValue(raw, "batchWindow"); ok && v != "" {
		secs, err := strconv.Atoi(v)
		switch {
		case err != nil:
			warnings = append(warnings, fmt.Sprintf("auto-sync.batchWindow=%q is not a number, using 0", v))
		case secs < 0:
			warnings = append(warnings, fmt.Sprintf("auto-sync.batchWindow=%d is negative, using 0", secs))
		case time.Duration(secs)*time.Second > MaxBatchWindow:
			s.BatchWindow = MaxBatchWindow
			warnings = append(warnings, fmt.Sprintf("auto-sync.batchWindow=%d is above the maximum, using %ds", secs, int(MaxBatchWindow/time.Second)))
		default:
			s.BatchWindow = time.Duration(secs) * time.Second
		}
	}

	if v, ok := gitConfigValue(raw, "exec"); ok && v != "" {
		if err := checkExecutable(v); err != nil {
			warnings = append(warnings, fmt.Sprintf("auto-sync.exec=%q is unusable (%v), using git from PATH", v, err))
		} else {
			s.GitExec = v
		}
	}

	if v, ok := gitConfigValue(raw, "messagePrefix"); ok {
		s.MessagePrefix = v
	}

	return s, warnings
}

func checkExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return errors.New("is a directory")
	}
	return nil
}

func ValidateSetting(setting, value string) (string, error) {
	value = strings.TrimSpace(value)
	switch setting {
	case SettingPollInterval:
		if value == "" {
			return "", nil
		}
		secs, err := strconv.Atoi(value)
		minS, maxS := int(MinPollInterval/time.Second), int(MaxPollInterval/time.Second)
		if err != nil || secs < minS || secs > maxS {
			return "", NewCodedError(CodeConfig, fmt.Sprintf("pollInterval must be a whole number of seconds between %d and %d, got %q", minS, maxS, value))
		}
		return strconv.Itoa(secs), nil
	case SettingBatchWindow:
		if value == "" {
			return "", nil
		}
		secs, err := strconv.Atoi(value)
		maxS := int(MaxBatchWindow / time.Second)
		if err != nil || secs < 0 || secs > maxS {
			return "", NewCodedError(CodeConfig, fmt.Sprintf("batchWindow must be a whole number of seconds between 0 and %d, got %q", maxS, value))
		}
		return strconv.Itoa(secs), nil
	case SettingExec:
		if value == "" {
			return "", nil
		}
		if err := checkExecutable(value); err != nil {
			return "", NewCodedError(CodeConfig, fmt.Sprintf("exec must point to a git executable: %v", err))
		}
		return value, nil
	case SettingMessagePrefix:
		if strings.ContainsAny(value, "\r\n") {
			return "", NewCodedError(CodeConfig, "messagePrefix must be a single line")
		}
		return value, nil
	}
	return "", NewCodedError(CodeConfig, "unknown setting: "+setting)
}
