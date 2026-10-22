package main

import (
	"context"
	"log"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/GitJournal/git-auto-sync/common"
)

const networkCheckInterval = 5 * time.Second

func networkFingerprint() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	var parts []string
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.IsLinkLocalUnicast() || ipnet.IP.IsLoopback() {
				continue
			}
			parts = append(parts, ifc.Name+"="+ipnet.IP.String())
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func (d *Daemon) startNetworkWatcher(ctx context.Context) {
	fingerprint := d.netFingerprint
	if fingerprint == nil {
		fingerprint = networkFingerprint
	}
	interval := d.netInterval
	if interval <= 0 {
		interval = networkCheckInterval
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		prev := fingerprint()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cur := fingerprint()
				if cur == prev {
					continue
				}
				prev = cur
				if cur == "" {
					continue
				}
				d.retryOffline()
			}
		}
	}()
}

func (d *Daemon) retryOffline() {
	var offline []string
	for _, st := range d.state.Snapshot() {
		if st.Status == common.StatusOffline && !st.Paused {
			offline = append(offline, st.Path)
		}
	}
	if len(offline) == 0 {
		return
	}
	log.Printf("daemon: network changed, retrying %d offline repo(s)", len(offline))
	for _, p := range offline {
		_ = d.SyncNow(p, false)
	}
}
