package watchdog

import "sync"

var (
	gcNotifeeMutex sync.Mutex
	gcNotifees     []notifeeEntry

	forcedGCNotifeeMutex sync.Mutex
	forcedGCNotifees     []notifeeEntry
)

// RegisterPostGCNotifee registers a function that is called every time a GC has happened,
// both GC runs triggered by the Go runtime and by watchdog.
// The unregister function returned can be used to unregister this notifee.
func RegisterPostGCNotifee(f func()) (unregister func()) {
	gcNotifeeMutex.Lock()
	defer gcNotifeeMutex.Unlock()

	var id int
	if len(gcNotifees) > 0 {
		id = gcNotifees[len(gcNotifees)-1].id + 1
	}
	gcNotifees = append(gcNotifees, notifeeEntry{id: id, f: f})

	return func() {
		gcNotifeeMutex.Lock()
		defer gcNotifeeMutex.Unlock()

		for i, entry := range gcNotifees {
			if entry.id == id {
				gcNotifees = append(gcNotifees[:i], gcNotifees[i+1:]...)
			}
		}
	}
}

func notifyGC() {
	if NotifyGC != nil {
		NotifyGC()
	}
	gcNotifeeMutex.Lock()
	defer gcNotifeeMutex.Unlock()
	for _, entry := range gcNotifees {
		entry.f()
	}
}

// RegisterPreGCNotifee registers a function that is called before watchdog triggers a GC run.
// It is ONLY called when watchdog triggers a GC run, not when the Go runtime triggers it.
// The unregister function returned can be used to unregister this notifee.
func RegisterPreGCNotifee(f func()) (unregister func()) {
	forcedGCNotifeeMutex.Lock()
	defer forcedGCNotifeeMutex.Unlock()

	var id int
	if len(forcedGCNotifees) > 0 {
		id = forcedGCNotifees[len(forcedGCNotifees)-1].id + 1
	}
	forcedGCNotifees = append(forcedGCNotifees, notifeeEntry{id: id, f: f})

	return func() {
		forcedGCNotifeeMutex.Lock()
		defer forcedGCNotifeeMutex.Unlock()

		for i, entry := range forcedGCNotifees {
			if entry.id == id {
				forcedGCNotifees = append(forcedGCNotifees[:i], forcedGCNotifees[i+1:]...)
			}
		}
	}
}

func notifyForcedGC() {
	forcedGCNotifeeMutex.Lock()
	defer forcedGCNotifeeMutex.Unlock()
	for _, entry := range forcedGCNotifees {
		entry.f()
	}
}
