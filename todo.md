* Merge strategies for conflicts (pick newest, ignore YAML header changes) instead of only reporting them
  - Add the logs of operation to .git/logs/auto-sync

* Add a way to only sync some-subfolder in a repo
* Ensure that the 'daemon' process can be found
  - It seems it wasn't being installed by the homebrew package

* When stopping the daemon, do not do anything if it is not running

* Fewer commits from editors that save constantly (Obsidian)
  - `auto-sync.batchWindow` batches changes today
  - Squash unpushed auto-commits into one before pushing
  - Or only commit when the editor is closed (for vim: figure out the backupfile directory)

* Empty files aren't being tracked? This behaviour needs to be documented

* It doesn't seem to be tracking some deletes?
