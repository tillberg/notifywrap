package notifywrap

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/rjeczalik/notify"
	"github.com/tillberg/alog"
)

type Opts struct {
	Path                       string
	DebounceDuration           time.Duration
	CoalesceEventTypes         bool
	NotifyDirectoriesOnStartup bool
	NotifyFilesOnStartup       bool
}

func WatchRecursive(path string, opts Opts) (<-chan *EventInfo, error) {
	pathEvents := make(chan *EventInfo, 100)
	rawPathEvents := make(chan notify.EventInfo, 1000)
	go run(opts, rawPathEvents, pathEvents)
	go walkDirTree(path, opts, pathEvents)
	err := notify.Watch(filepath.Clean(path)+"/...", rawPathEvents, notify.All)
	if err != nil {
		return nil, errors.Wrapf(err, "adding root watch for %q", filepath.Clean(path)+"/...")
	}
	if runtime.GOOS == "darwin" {
		err := handleDarwinBindfsMounts(path, opts, rawPathEvents)
		if err != nil {
			return nil, errors.Wrap(err, "adding bindfs-workaround watches")
		}
	}
	return pathEvents, nil
}

type eventKey struct {
	event notify.Event
	path  string
}

func run(opts Opts, rawPathEvents chan notify.EventInfo, pathEvents chan *EventInfo) {
	timerElapsed := false
	timer := time.NewTimer(1e6 * time.Hour)
	events := map[eventKey]*EventInfo{}
	for {
		var sendPathEvents chan<- *EventInfo
		var sendPathEventKey eventKey
		var sendPathEvent *EventInfo
		if timerElapsed && len(events) > 0 {
			sendPathEvents = pathEvents
			for key, ev := range events {
				sendPathEventKey = key
				sendPathEvent = ev
				break
			}
		}

		select {
		case rawEvent := <-rawPathEvents:
			path := rawEvent.Path()
			event := rawEvent.Event()
			key := eventKey{path: path}
			if !opts.CoalesceEventTypes {
				key.event = event
			}
			ev, ok := events[key]
			if !ok {
				ev = &EventInfo{
					Event: event,
					Path:  path,
				}
				events[key] = ev
			} else if opts.CoalesceEventTypes {
				ev.Event |= event
			}
			// reset timer
			if timerElapsed {
				timerElapsed = false
			} else if !timer.Stop() {
				<-timer.C
			}
			timer.Reset(opts.DebounceDuration)
		case sendPathEvents <- sendPathEvent:
			delete(events, sendPathEventKey)
		case <-timer.C:
			timerElapsed = true
		}
	}
}

func walkDirTree(rootPath string, opts Opts, pathEvents chan<- *EventInfo) error {
	return filepath.Walk(rootPath, func(path string, info os.FileInfo, err error) error {
		path = filepath.Clean(path)
		var shouldNotify bool
		if info.IsDir() {
			shouldNotify = opts.NotifyDirectoriesOnStartup
		} else {
			shouldNotify = opts.NotifyFilesOnStartup
		}
		if shouldNotify {
			pathEvents <- &EventInfo{
				Event: notify.Write,
				Path:  path,
			}
		}
		return nil
	})
}

type EventInfo struct {
	Event notify.Event
	Path  string
}

func handleDarwinBindfsMounts(rootPath string, opts Opts, rawPathEvents chan notify.EventInfo) error {
	cmd := exec.Command("mount")
	buf, err := cmd.CombinedOutput()
	if err != nil {
		return errors.Wrap(err, "getting list of mounts")
	}
	s := bufio.NewScanner(bytes.NewBuffer(buf))
	for s.Scan() {
		line := s.Text()
		parts := strings.SplitN(line, " ", 4)
		if len(parts) < 4 {
			continue
		}
		src, _, dest, rest := parts[0], parts[1], parts[2], parts[3]
		if !strings.Contains(rest, "osxfuse") {
			continue
		}
		_, err = os.Stat(src)
		if err != nil {
			continue
		}
		_, err = os.Stat(dest)
		if err != nil {
			continue
		}
		alog.Printf("Found apparent bindfs mount at %q. Adding path-rewriting watcher for it at %q.\n", dest, src)
		err = watchWithPathRewrite(dest, src, opts, rawPathEvents)
		if err != nil {
			return errors.Wrapf(err, "adding watch for bindfs mount with source root %q", src)
		}
	}
	err = s.Err()
	if err != nil {
		return errors.Wrap(err, "parsing list of mounts")
	}
	return nil
}

type rawEventInfo struct {
	event notify.Event
	path  string
	sys   interface{}
}

func (i rawEventInfo) Event() notify.Event {
	return i.event
}

func (i rawEventInfo) Path() string {
	return i.path
}

func (i rawEventInfo) Sys() interface{} {
	return i.sys
}

func watchWithPathRewrite(logicalRoot string, actualRoot string, opts Opts, rawPathEvents chan notify.EventInfo) error {
	origRawPathEvents := make(chan notify.EventInfo, 1000)
	go func() {
		for ev := range origRawPathEvents {
			origPath := ev.Path()
			logicalPath := strings.Replace(origPath, actualRoot, logicalRoot, 1)
			if origPath == logicalPath {
				alog.Printf("@(error:Failed to rewrite path %q in notifywrap.)", origPath)
			}
			rawPathEvents <- rawEventInfo{
				event: ev.Event(),
				path:  logicalPath,
				sys:   ev.Sys(),
			}
		}
	}()
	err := notify.Watch(filepath.Clean(actualRoot)+"/...", origRawPathEvents, notify.All)
	if err != nil {
		return errors.Wrapf(err, "adding bindfs-mount-workaround watch for %q", filepath.Clean(actualRoot)+"/...")
	}
	return nil
}
