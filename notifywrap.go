package notifywrap

import (
	"os"
	"path/filepath"
	"time"

	"github.com/rjeczalik/notify"
)

type Opts struct {
	Path                       string
	DebounceDuration           time.Duration
	CoalesceEventTypes         bool
	NotifyDirectoriesOnStartup bool
}

func WatchRecursive(path string, opts Opts) (<-chan notify.EventInfo, error) {
	pathEvents := make(chan notify.EventInfo, 100)
	rawPathEvents := make(chan notify.EventInfo, 1000)
	err := notify.Watch(filepath.Join(path, "..."), rawPathEvents, notify.All)
	if err != nil {
		return nil, err
	}
	go run(opts, rawPathEvents, pathEvents)
	if opts.NotifyDirectoriesOnStartup {
		go walkDirTree(path, rawPathEvents)
	}
	return pathEvents, nil
}

type eventKey struct {
	event notify.Event
	path  string
}

func run(opts Opts, rawPathEvents chan notify.EventInfo, pathEvents chan notify.EventInfo) {
	timerElapsed := false
	timer := time.NewTimer(1e6 * time.Hour)
	events := map[eventKey]*eventInfo{}
	for {
		var sendPathEvents chan<- notify.EventInfo
		var sendPathEventKey eventKey
		var sendPathEvent notify.EventInfo
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
				ev = &eventInfo{
					event: event,
					path:  path,
				}
				events[key] = ev
			} else if opts.CoalesceEventTypes {
				ev.event |= event
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

func walkDirTree(path string, rawPathEvents chan<- notify.EventInfo) {
	err := filepath.Walk(path, func(path string, info os.FileInfo, err error) error {
		if info.IsDir() {
			rawPathEvents <- &eventInfo{
				event: notify.Write,
				path:  path,
			}
			return nil
		}
		return nil
	})
	if err != nil {
		panic(err)
	}
}

type eventInfo struct {
	event notify.Event
	path  string
}

func (e *eventInfo) Event() notify.Event {
	return e.event
}

func (e *eventInfo) Path() string {
	return e.path
}

func (e *eventInfo) Sys() interface{} {
	return nil
}
