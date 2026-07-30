package lease

import (
	"os"
	"testing"

	"github.com/alexis-marcel/gemline/internal/dbtest"
)

func TestMain(m *testing.M) {
	unlock := dbtest.Lock()
	code := m.Run()
	unlock()
	os.Exit(code)
}
