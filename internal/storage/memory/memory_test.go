package memory_test

import (
	"testing"

	"github.com/Driane/slotguard/internal/booking"
	"github.com/Driane/slotguard/internal/booking/bookingtest"
	"github.com/Driane/slotguard/internal/storage/memory"
)

func TestStore(t *testing.T) {
	bookingtest.RunStoreTests(t, func(*testing.T) booking.Store {
		return memory.New()
	})
}
