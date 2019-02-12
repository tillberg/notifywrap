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

func WatchRecursive(path string, opts Opts) (<-chan *EventInfo, error) {
	pathEvents := make(chan *EventInfo, 100)
	rawPathEvents := make(chan notify.EventInfo, 1000)
	err := notify.Watch(filepath.Join(path, "..."), rawPathEvents, notify.All)
	if err != nil {
		return nil, err
	}
	go run(opts, rawPathEvents, pathEvents)
	if opts.NotifyDirectoriesOnStartup {
		go walkDirTree(path, pathEvents)
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

func walkDirTree(path string, pathEvents chan<- *EventInfo) {
	err := filepath.Walk(path, func(path string, info os.FileInfo, err error) error {
		if info.IsDir() {
			pathEvents <- &EventInfo{
				Event: notify.Write,
				Path:  path,
			}
			return nil
		}
		return nil
	})
	if err != nil {
		panic(err)
	}
}

type EventInfo struct {
	Event notify.Event
	Path  string
}
