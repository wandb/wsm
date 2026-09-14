package kubectl

import (
	"fmt"
	"sync"
	"testing"
)

// ResetClients used to reassign the package's sync.Once values, which swaps a
// Once's internal mutex out from under any goroutine inside once.Do and aborts
// the process with "sync: unlock of unlocked mutex". This hammers the lifecycle
// the way a kubeconfig switch does while requests are in flight.
func TestClientLifecycleIsRaceFree(t *testing.T) {
	t.Setenv("KUBECONFIG", t.TempDir()+"/missing-kubeconfig")
	SetContext("")
	t.Cleanup(func() { SetContext(""); ResetClients() })

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start
			for j := 0; j < 200; j++ {
				switch (n + j) % 4 {
				case 0:
					SetContext(fmt.Sprintf("ctx-%d", j))
				case 1:
					ResetClients()
				case 2:
					_, _, _ = GetClientset()
					_ = GetContext()
				case 3:
					_, _ = RefreshRESTMapper()
					_, _ = GetRESTMapper()
				}
			}
		}(i)
	}
	close(start)
	wg.Wait()
}
