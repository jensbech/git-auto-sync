package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/GitJournal/git-auto-sync/common"
	cfg "github.com/GitJournal/git-auto-sync/common/config"
	"github.com/kardianos/service"
	cli "github.com/urfave/cli/v2"
	"github.com/ztrue/tracerr"
)

var errDaemonNotRunning = errors.New("the daemon is not running (start it with `git-auto-sync daemon start`)")

type daemonState int

const (
	daemonDown daemonState = iota
	daemonOutdated
	daemonUp
)

func callDaemon(req common.Request) (*common.ClientResponse, daemonState, error) {
	client, err := common.NewSocketClient(common.DefaultSocketPath())
	if err != nil {
		return nil, daemonDown, nil
	}
	defer client.Close()

	resp, err := client.SendCommand(req)
	if errors.Is(err, common.ErrIncompatibleDaemon) {
		return nil, daemonOutdated, nil
	}
	if err != nil {
		return nil, daemonUp, err
	}
	return resp, daemonUp, nil
}

func mustDaemon(req common.Request) (*common.ClientResponse, error) {
	resp, state, err := callDaemon(req)
	switch {
	case err != nil:
		return nil, err
	case state == daemonDown:
		return nil, errDaemonNotRunning
	case state == daemonOutdated:
		return nil, common.ErrIncompatibleDaemon
	}
	if err := resp.Err(); err != nil {
		return nil, err
	}
	return resp, nil
}

func printJSON(v interface{}) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func printRawJSON(raw json.RawMessage) error {
	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	return printJSON(v)
}

func serviceStatusLine() string {
	s, err := common.NewService()
	if err != nil {
		return "service status unavailable: " + err.Error()
	}
	status, err := s.Service.Status()
	if err != nil {
		if errors.Is(err, service.ErrNotInstalled) || strings.Contains(err.Error(), "not installed") {
			return "git-auto-sync-daemon is not installed (run `git-auto-sync daemon add <repo>` or `git-auto-sync daemon enable`)"
		}
		return "git-auto-sync-daemon status is unknown: " + err.Error()
	}
	switch status {
	case service.StatusRunning:
		return "git-auto-sync-daemon is running"
	case service.StatusStopped:
		return "git-auto-sync-daemon is installed but not running"
	default:
		return "git-auto-sync-daemon status is unknown"
	}
}

func daemonStatusName(state daemonState, err error) string {
	switch {
	case err != nil:
		return "error"
	case state == daemonOutdated:
		return "outdated"
	case state == daemonDown:
		return "not_running"
	}
	return "running"
}

func configuredRepoStates(repos []string) []common.RepoState {
	out := make([]common.RepoState, 0, len(repos))
	for _, rp := range repos {
		out = append(out, common.RepoState{Path: rp, Status: "unknown"})
	}
	return out
}

func daemonStatus(ctx *cli.Context) error {
	resp, state, err := callDaemon(common.Request{Cmd: common.CmdStatus})
	if err == nil && state == daemonUp {
		err = resp.Err()
	}
	reachable := err == nil && state == daemonUp

	if ctx.Bool("json") {
		if reachable {
			return printRawJSON(resp.Result)
		}
		config, cerr := cfg.Read()
		if cerr != nil {
			return tracerr.Wrap(cerr)
		}
		out := map[string]interface{}{
			"daemon":  daemonStatusName(state, err),
			"service": serviceStatusLine(),
			"socket":  common.DefaultSocketPath(),
			"repos":   configuredRepoStates(config.Repos),
		}
		if err != nil {
			out["error"] = err.Error()
		}
		if perr := printJSON(out); perr != nil {
			return perr
		}
		return err
	}

	fmt.Println(serviceStatusLine())

	if reachable {
		var status common.StatusResult
		if err := resp.Decode(&status); err != nil {
			return tracerr.Wrap(err)
		}
		fmt.Printf("daemon %s (protocol %d), socket %s\n\n", status.Version, status.Protocol, common.DefaultSocketPath())
		printRepoTable(os.Stdout, status.Repos)
		return nil
	}

	switch {
	case err != nil:
		fmt.Println("daemon socket at", common.DefaultSocketPath(), "did not answer")
	case state == daemonOutdated:
		fmt.Println(common.ErrIncompatibleDaemon)
	default:
		fmt.Println("daemon socket is not reachable at", common.DefaultSocketPath())
	}

	config, cerr := cfg.Read()
	if cerr != nil {
		return tracerr.Wrap(cerr)
	}
	if len(config.Repos) == 0 {
		fmt.Println("No repos configured")
	} else {
		fmt.Println("\nConfigured repos:")
		for _, repoPath := range config.Repos {
			fmt.Println("  ", repoPath)
		}
	}
	return err
}

func daemonList(ctx *cli.Context) error {
	resp, state, err := callDaemon(common.Request{Cmd: common.CmdList})
	if err == nil && state == daemonUp {
		err = resp.Err()
	}
	if err == nil && state == daemonUp {
		var status common.StatusResult
		if err := resp.Decode(&status); err != nil {
			return tracerr.Wrap(err)
		}
		if ctx.Bool("json") {
			return printJSON(status.Repos)
		}
		printRepoTable(os.Stdout, status.Repos)
		return nil
	}

	config, cerr := cfg.Read()
	if cerr != nil {
		return tracerr.Wrap(cerr)
	}
	if ctx.Bool("json") {
		if perr := printJSON(configuredRepoStates(config.Repos)); perr != nil {
			return perr
		}
		return err
	}
	switch {
	case err != nil:
		fmt.Fprintln(os.Stderr, "The daemon did not answer; showing configured repos.")
	case state == daemonOutdated:
		fmt.Fprintln(os.Stderr, common.ErrIncompatibleDaemon.Error()+"; showing configured repos.")
	default:
		fmt.Fprintln(os.Stderr, "The daemon is not running; showing configured repos.")
	}
	if len(config.Repos) == 0 {
		fmt.Println("No repos configured")
	}
	for _, repoPath := range config.Repos {
		fmt.Println(repoPath)
	}
	return err
}

func printRepoTable(out io.Writer, repos []common.RepoState) {
	if len(repos) == 0 {
		fmt.Fprintln(out, "No repos are being monitored")
		return
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].Path < repos[j].Path })

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSTATUS\tBRANCH\tAHEAD/BEHIND\tLAST CHANGE\tPROBLEM")
	now := time.Now()
	for _, r := range repos {
		branch := r.Branch
		if branch == "" {
			branch = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d/%d\t%s\t%s\n",
			filepath.Base(r.Path), r.Status, branch, r.Ahead, r.Behind,
			relativeTime(r.LastChangeTs, now), repoProblem(r, now))
	}
	_ = w.Flush()
}

func relativeTime(ts int64, now time.Time) string {
	if ts <= 0 {
		return "-"
	}
	d := now.Sub(time.Unix(ts, 0))
	if d < 0 {
		return "in " + shortDuration(-d)
	}
	return shortDuration(d) + " ago"
}

func shortDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func repoProblem(r common.RepoState, now time.Time) string {
	switch r.Status {
	case common.StatusConflict:
		if len(r.ConflictFiles) > 0 {
			return fmt.Sprintf("conflict in %d file(s): %s", len(r.ConflictFiles), truncate(strings.Join(r.ConflictFiles, ", "), 50))
		}
		return "conflict"
	case common.StatusPreflight:
		return "preflight: " + truncate(firstLine(r.Preflight), 60)
	case common.StatusOffline:
		if r.NextRetryTs > 0 {
			return "offline, retry " + relativeTime(r.NextRetryTs, now)
		}
		return "offline"
	case common.StatusError:
		msg := truncate(firstLine(r.LastError), 60)
		if r.ErrorCode != "" && r.ErrorCode != common.CodeUnknown {
			return r.ErrorCode + ": " + msg
		}
		return msg
	}
	return ""
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func repoArgs(ctx *cli.Context) []string {
	args := ctx.Args().Slice()
	if len(args) == 0 {
		return []string{"."}
	}
	return args
}

func daemonAdd(ctx *cli.Context) error {
	for _, arg := range repoArgs(ctx) {
		if err := addRepo(arg); err != nil {
			return err
		}
	}
	return nil
}

func addRepo(arg string) error {
	root, err := common.RepoRoot(arg)
	if err != nil {
		return err
	}

	resp, state, err := callDaemon(common.Request{Cmd: common.CmdAdd, Repo: root})
	if err != nil {
		return tracerr.Wrap(err)
	}
	if state == daemonUp {
		if !resp.OK {
			if resp.Code == common.CodeAlreadyExists {
				fmt.Println("Already monitoring " + root)
				return nil
			}
			return resp.Err()
		}
		var result common.CommandResult
		if err := resp.Decode(&result); err == nil && result.Repo != "" {
			root = result.Repo
		}
		if result.Status == common.CodeAlreadyExists {
			fmt.Println("Already monitoring " + root)
			return nil
		}
		fmt.Println("Now monitoring " + root)
		return nil
	}

	config, err := cfg.Read()
	if err != nil {
		return tracerr.Wrap(err)
	}

	if containsPath(config.Repos, root) {
		fmt.Println("Already monitoring " + root)
	} else {
		config.Repos = append(config.Repos, root)
		if err := cfg.Write(config); err != nil {
			return tracerr.Wrap(err)
		}
		fmt.Println("Now monitoring " + root)
	}

	s, err := common.NewService()
	if err != nil {
		return tracerr.Wrap(err)
	}
	return tracerr.Wrap(s.Enable())
}

func containsPath(repos []string, path string) bool {
	for _, rp := range repos {
		if filepath.Clean(rp) == path {
			return true
		}
	}
	return false
}

func resolveTracked(arg string, repos []string) (string, error) {
	abs, err := filepath.Abs(arg)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)

	candidates := []string{abs, common.ResolvePath(abs)}
	if root, err := common.RepoRoot(abs); err == nil {
		candidates = append(candidates, root)
	}

	for _, c := range candidates {
		for _, rp := range repos {
			if filepath.Clean(rp) == c {
				return rp, nil
			}
		}
	}
	return "", common.NewCodedError(common.CodeNotFound, "not monitoring "+abs+" (see `git-auto-sync daemon ls`)")
}

func daemonRm(ctx *cli.Context) error {
	args := ctx.Args().Slice()
	if len(args) == 0 {
		return errors.New("usage: git-auto-sync daemon rm <repo> [repo...]")
	}
	for _, arg := range args {
		if err := removeRepo(arg); err != nil {
			return err
		}
	}
	return nil
}

func removeRepo(arg string) error {
	config, err := cfg.Read()
	if err != nil {
		return tracerr.Wrap(err)
	}

	target, err := resolveTracked(arg, config.Repos)
	if err != nil {
		return err
	}

	resp, state, err := callDaemon(common.Request{Cmd: common.CmdRemove, Repo: target})
	if err != nil {
		return tracerr.Wrap(err)
	}
	if state == daemonUp {
		if err := resp.Err(); err != nil {
			return err
		}
		fmt.Println("Stopped monitoring " + target)
		return nil
	}

	repos := make([]string, 0, len(config.Repos))
	for _, rp := range config.Repos {
		if rp != target {
			repos = append(repos, rp)
		}
	}
	config.Repos = repos
	if err := cfg.Write(config); err != nil {
		return tracerr.Wrap(err)
	}
	fmt.Println("Stopped monitoring " + target)

	if len(config.Repos) == 0 {
		s, err := common.NewService()
		if err != nil {
			return tracerr.Wrap(err)
		}
		if err := s.Disable(); err != nil {
			fmt.Fprintln(os.Stderr, "warning: could not disable the daemon:", err)
		}
	}
	return nil
}

func resolveRepoArg(arg string) (string, error) {
	config, err := cfg.Read()
	if err == nil {
		if rp, err := resolveTracked(arg, config.Repos); err == nil {
			return rp, nil
		}
	}
	if root, err := common.RepoRoot(arg); err == nil {
		return root, nil
	}
	abs, err := filepath.Abs(arg)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func repoTargets(ctx *cli.Context) ([]string, error) {
	targets := []string{}
	for _, arg := range repoArgs(ctx) {
		rp, err := resolveRepoArg(arg)
		if err != nil {
			return nil, err
		}
		targets = append(targets, rp)
	}
	return targets, nil
}

func daemonSync(ctx *cli.Context) error {
	if ctx.Bool("all") {
		if _, err := mustDaemon(common.Request{Cmd: common.CmdSyncAll}); err != nil {
			return err
		}
		fmt.Println("Sync triggered for all repos")
		return nil
	}

	targets, err := repoTargets(ctx)
	if err != nil {
		return err
	}
	for _, rp := range targets {
		if _, err := mustDaemon(common.Request{Cmd: common.CmdSyncNow, Repo: rp, Force: ctx.Bool("force")}); err != nil {
			return err
		}
		fmt.Println("Sync triggered for " + rp)
	}
	return nil
}

func daemonPause(ctx *cli.Context) error {
	return setPaused(ctx, true)
}

func daemonResume(ctx *cli.Context) error {
	return setPaused(ctx, false)
}

func setPaused(ctx *cli.Context, paused bool) error {
	verb := "Resumed"
	cmd, allCmd := common.CmdResume, common.CmdResumeAll
	if paused {
		verb = "Paused"
		cmd, allCmd = common.CmdPause, common.CmdPauseAll
	}

	if ctx.Bool("all") {
		if _, err := mustDaemon(common.Request{Cmd: allCmd}); err != nil {
			return err
		}
		fmt.Println(verb + " all repos")
		return nil
	}

	targets, err := repoTargets(ctx)
	if err != nil {
		return err
	}
	for _, rp := range targets {
		if _, err := mustDaemon(common.Request{Cmd: cmd, Repo: rp}); err != nil {
			return err
		}
		fmt.Println(verb + " " + rp)
	}
	return nil
}

func daemonEvents(ctx *cli.Context) error {
	req := common.Request{Cmd: common.CmdEvents, Limit: ctx.Int("n")}
	if arg := ctx.Args().First(); arg != "" {
		rp, err := resolveRepoArg(arg)
		if err != nil {
			return err
		}
		req.Repo = rp
	}

	resp, err := mustDaemon(req)
	if err != nil {
		return err
	}
	var result common.EventsResult
	if err := resp.Decode(&result); err != nil {
		return tracerr.Wrap(err)
	}

	jsonOut := ctx.Bool("json")
	for _, ev := range result.Events {
		printEvent(ev, jsonOut)
	}

	if !ctx.Bool("follow") {
		return nil
	}
	return followEvents(req.Repo, result.LastEventID, jsonOut)
}

func followEvents(repo string, after uint64, jsonOut bool) error {
	client, err := common.NewSocketClient(common.DefaultSocketPath())
	if err != nil {
		return errDaemonNotRunning
	}
	defer client.Close()

	hello, err := client.Subscribe()
	if err != nil {
		return err
	}

	if hello.LastEventID > after {
		resp, err := mustDaemon(common.Request{Cmd: common.CmdEvents, Since: after, Repo: repo})
		if err != nil {
			return err
		}
		var gap common.EventsResult
		if err := resp.Decode(&gap); err != nil {
			return tracerr.Wrap(err)
		}
		for _, ev := range gap.Events {
			if ev.ID <= hello.LastEventID {
				printEvent(ev, jsonOut)
			}
		}
		after = hello.LastEventID
	}

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-sigCtx.Done()
		client.Close()
	}()

	for {
		frame, err := client.ReadFrame()
		if err != nil {
			if sigCtx.Err() != nil {
				return nil
			}
			if errors.Is(err, io.EOF) {
				return errors.New("the daemon closed the connection")
			}
			return err
		}
		var ev common.Event
		if err := json.Unmarshal(frame, &ev); err != nil || ev.ID == 0 {
			continue
		}
		if ev.ID <= after || (repo != "" && ev.Repo != repo) {
			continue
		}
		printEvent(ev, jsonOut)
	}
}

func printEvent(ev common.Event, jsonOut bool) {
	if jsonOut {
		data, err := json.Marshal(ev)
		if err == nil {
			fmt.Println(string(data))
		}
		return
	}
	msg := ev.Message
	if msg == "" {
		msg = ev.Status
	}
	if ev.Code != "" {
		msg = fmt.Sprintf("%s [%s]", msg, ev.Code)
	}
	fmt.Printf("%s  %-9s  %-16s  %s\n",
		time.Unix(ev.Ts, 0).Format("2006-01-02 15:04:05"), ev.Type, filepath.Base(ev.Repo), firstLine(msg))
}

func logPathCandidates() []string {
	primary := common.LogPath()
	candidates := []string{
		primary,
		filepath.Join(filepath.Dir(primary), "git-auto-sync-daemon.err.log"),
		filepath.Join(filepath.Dir(primary), "git-auto-sync-daemon.out.log"),
	}
	if runtime.GOOS == "darwin" {
		if home, err := os.UserHomeDir(); err == nil {
			candidates = append(candidates, filepath.Join(home, "git-auto-sync-daemon.err.log"))
		}
	}
	return candidates
}

func findLogFile() (string, error) {
	candidates := logPathCandidates()
	for _, p := range candidates {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("no daemon log found (looked in %s)", strings.Join(candidates, ", "))
}

func daemonLogs(ctx *cli.Context) error {
	path, err := findLogFile()
	if err != nil {
		return err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return tracerr.Wrap(err)
	}
	fmt.Print(lastLines(string(data), ctx.Int("n")))

	if !ctx.Bool("follow") {
		return nil
	}

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return followFile(sigCtx, path, int64(len(data)), os.Stdout)
}

func lastLines(s string, n int) string {
	if n <= 0 || s == "" {
		return s
	}
	trimmed := strings.TrimSuffix(s, "\n")
	idx := len(trimmed)
	for i := 0; i < n; i++ {
		j := strings.LastIndexByte(trimmed[:idx], '\n')
		if j < 0 {
			return s
		}
		idx = j
	}
	return s[idx+1:]
}

func followFile(ctx context.Context, path string, offset int64, out io.Writer) error {
	prev, _ := os.Stat(path)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}

		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if (prev != nil && !os.SameFile(prev, info)) || info.Size() < offset {
			offset = 0
		}
		prev = info
		if info.Size() == offset {
			continue
		}

		f, err := os.Open(path)
		if err != nil {
			continue
		}
		if _, err := f.Seek(offset, io.SeekStart); err == nil {
			n, _ := io.Copy(out, f)
			offset += n
		}
		f.Close()
	}
}

func daemonEnv(ctx *cli.Context) error {
	vars := ctx.Args().Slice()

	for _, v := range vars {
		if !strings.Contains(v, "=") {
			return fmt.Errorf("env variables must be in the format 'key=value', got %q", v)
		}
	}

	config, err := cfg.Read()
	if err != nil {
		return tracerr.Wrap(err)
	}

	envMap := toEnvMap(config.Envs)
	newMap := toEnvMap(vars)

	for k, v := range newMap {
		envMap[k] = v
	}

	config.Envs = toEnvStrings(envMap)
	if len(vars) > 0 {
		if err := cfg.Write(config); err != nil {
			return tracerr.Wrap(err)
		}
	}

	if len(config.Envs) > 0 {
		fmt.Println(strings.Join(config.Envs, "\n"))
	}

	if len(vars) > 0 {
		if _, state, _ := callDaemon(common.Request{Cmd: common.CmdVersion}); state != daemonDown {
			fmt.Fprintln(os.Stderr, "The running daemon keeps its old environment until you run `git-auto-sync daemon restart`.")
		}
	}
	return nil
}

func toEnvMap(envs []string) map[string]string {
	m := map[string]string{}
	for _, e := range envs {
		parts := strings.Split(e, "=")
		if len(parts) > 1 {
			m[parts[0]] = strings.Join(parts[1:], "=")
		} else {
			m[e] = ""
		}
	}

	return m
}

func toEnvStrings(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	vals := []string{}
	for _, k := range keys {
		vals = append(vals, fmt.Sprintf("%s=%s", k, m[k]))
	}

	return vals
}
