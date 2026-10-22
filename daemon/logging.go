package main

import (
	"io"
	"log"
	"os"

	"github.com/GitJournal/git-auto-sync/common"
)

func setupLogging(interactive bool) {
	path := common.LogPath()
	w, err := common.NewRotatingWriter(path, common.LogMaxBytes, common.LogMaxFiles)
	if err != nil {
		log.Printf("daemon: cannot open log file %s, logging to stderr: %v", path, err)
		return
	}
	if interactive {
		log.SetOutput(io.MultiWriter(os.Stderr, w))
	} else {
		log.SetOutput(w)
	}
	if common.DebugEnabled() {
		log.Printf("daemon: debug logging enabled (GAS_DEBUG=1)")
	}
}
