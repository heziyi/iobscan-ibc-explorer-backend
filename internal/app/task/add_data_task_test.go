package task

import (
	"github.com/bianjieai/iobscan-ibc-explorer-backend/internal/app/global"
	"testing"
)

func TestRun(t *testing.T) {
	global.Config.Task.SwitchAddDataTask = true
	new(AddDataTask).Run()
}
