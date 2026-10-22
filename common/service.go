package common

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/kardianos/service"
	"github.com/ztrue/tracerr"
)

type Service struct {
	Service service.Service
}

type emptyDaemon struct{}

func (d emptyDaemon) Start(s service.Service) error {
	return nil
}

func (d emptyDaemon) Stop(s service.Service) error {
	return nil
}

const launchdPath = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

func serviceConfig(goos, executable string) *service.Config {
	options := make(service.KeyValue)
	options["Restart"] = "on-success"
	options["UserService"] = true
	options["RunAtLoad"] = true

	deps := []string{}
	var envVars map[string]string
	switch goos {
	case "darwin":
		options["LogDirectory"] = filepath.Dir(LogPath())
		options["KeepAlive"] = true
		envVars = map[string]string{"PATH": launchdPath}
	case "linux":
		deps = []string{"After=network-online.target syslog.target"}
	}

	return &service.Config{
		Name:        "git-auto-sync-daemon",
		DisplayName: "Git Auto Sync Daemon",
		Description: "Background Process for Auto Syncing Git Repos",

		Executable:   filepath.Join(filepath.Dir(executable), "git-auto-sync-daemon"),
		Dependencies: deps,
		EnvVars:      envVars,
		Option:       options,
	}
}

func NewServiceWithDaemon(daemon service.Interface) (Service, error) {
	ex, err := os.Executable()
	if err != nil {
		return Service{}, tracerr.Wrap(err)
	}

	s, err := service.New(daemon, serviceConfig(runtime.GOOS, ex))
	if err != nil {
		return Service{}, tracerr.Wrap(err)
	}

	return Service{Service: s}, nil
}

func NewService() (Service, error) {
	return NewServiceWithDaemon(emptyDaemon{})
}

func (srv Service) Enable() error {
	s := srv.Service

	if runtime.GOOS == "darwin" {
		if err := os.MkdirAll(filepath.Dir(LogPath()), 0o755); err != nil {
			return tracerr.Wrap(err)
		}
	}

	status, err := s.Status()
	if err != nil {
		if !strings.Contains(err.Error(), "the service is not installed") {
			return tracerr.Wrap(err)
		}
	}

	stopped := false
	if status == service.StatusRunning {
		err := s.Stop()
		if err != nil {
			return tracerr.Wrap(err)
		}
		stopped = true
	}

	err = s.Install()
	if err != nil {
		if strings.Contains(err.Error(), "Init already exists") {
			_ = s.Uninstall()
			_ = s.Install()
		} else {
			return tracerr.Wrap(err)
		}
	} else {
		fmt.Println("Installing git-auto-sync as a daemon")
	}

	if stopped {
		fmt.Println("Restarting git-auto-sync-daemon")
	} else {
		fmt.Println("Starting git-auto-sync-daemon")
	}

	err = s.Start()
	if err != nil {
		return tracerr.Wrap(err)
	}

	return nil
}

func (srv Service) Disable() error {
	fmt.Println("Stopping git-auto-sync-daemon")
	err := srv.Service.Stop()
	if err != nil {
		return tracerr.Wrap(err)
	}

	fmt.Println("Uninstalling git-auto-sync as a daemon")
	err = srv.Service.Uninstall()
	if err != nil {
		return tracerr.Wrap(err)
	}

	return nil
}

func (srv Service) Status() error {
	status, err := srv.Service.Status()
	if err != nil {
		return tracerr.Wrap(err)
	}

	switch status {
	case service.StatusRunning:
		fmt.Println("git-auto-sync-daemon is Running!")
	case service.StatusStopped:
		fmt.Println("git-auto-sync-daemon is NOT Running!")
	case service.StatusUnknown:
	default:
		fmt.Println("git-auto-sync-daemon status is Unknown. How mysterious!")
	}

	return nil
}
