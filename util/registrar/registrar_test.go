package registrar

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestDelayedRegistration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := New[string, string]()
		registered := make(chan struct{})
		go func() {
			time.Sleep(4 * time.Second)
			r.Register("build", "bridge")
			close(registered)
		}()
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		value, err := r.Get(ctx, "build")
		<-registered
		if err != nil || value != "bridge" {
			t.Fatalf("live request lost its pending registration: value=%q err=%v", value, err)
		}
	})
}

func TestAbandonedLookupIsRemoved(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := New[string, string]()
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		_, err := r.Get(ctx, "missing")
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected request deadline, got %v", err)
		}
		retained := len(r.values)
		r.Discard("missing")
		synctest.Wait()
		if retained != 0 {
			t.Fatal("abandoned lookup retained a registration")
		}
	})
}

func TestOneCanceledWaiterDoesNotCancelAnother(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := New[string, string]()
		ctx, cancel := context.WithCancel(t.Context())
		go func() {
			_, err := r.Get(ctx, "build")
			if !errors.Is(err, context.Canceled) {
				t.Errorf("expected cancellation, got %v", err)
			}
		}()
		go func() {
			value, err := r.Get(t.Context(), "build")
			if err != nil || value != "bridge" {
				t.Errorf("surviving waiter: value=%q err=%v", value, err)
			}
		}()
		synctest.Wait()
		cancel()
		synctest.Wait()
		r.Register("build", "bridge")
		synctest.Wait()
	})
}

func TestDiscardWakesWaitersWithoutDeletingReplacement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := New[string, string]()
		go func() {
			_, err := r.Get(t.Context(), "build")
			if !errors.Is(err, context.Canceled) {
				t.Errorf("expected discarded build, got %v", err)
			}
		}()
		synctest.Wait()
		r.Discard("build")
		r.Register("build", "replacement")
		synctest.Wait()
		value, err := r.Get(t.Context(), "build")
		if err != nil || value != "replacement" {
			t.Fatalf("replacement registration lost: value=%q err=%v", value, err)
		}
	})
}

func TestRegisteredValuePersistsUntilDiscard(t *testing.T) {
	r := New[string, string]()
	r.Register("build", "bridge")
	r.Register("build", "must-not-replace")
	for range 2 {
		value, err := r.Get(t.Context(), "build")
		if err != nil || value != "bridge" {
			t.Fatalf("registered value changed: value=%q err=%v", value, err)
		}
	}
	r.Discard("build")
	if len(r.values) != 0 {
		t.Fatal("discarded registration retained")
	}
}

func TestConcurrentCancelAndRegister(t *testing.T) {
	for range 100 {
		r := New[string, string]()
		ctx, cancel := context.WithCancel(t.Context())
		var wg sync.WaitGroup
		wg.Go(func() { r.Register("build", "bridge") })
		wg.Go(cancel)
		value, err := r.Get(ctx, "build")
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("lookup failed: %v", err)
		}
		if err == nil && value != "bridge" {
			t.Fatalf("lookup returned unpublished value: %q", value)
		}
		wg.Wait()
		lookup, stop := context.WithTimeout(t.Context(), time.Second)
		value, err = r.Get(lookup, "build")
		stop()
		if err != nil || value != "bridge" {
			t.Fatalf("canceled waiter removed completed registration: value=%q err=%v", value, err)
		}
	}
}
