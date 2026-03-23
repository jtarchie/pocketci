package orchestra_test

import (
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreAnyFunction("net/http.(*persistConn).readLoop"),
		goleak.IgnoreAnyFunction("net/http.(*persistConn).writeLoop"),
		goleak.IgnoreTopFunction("k8s.io/apimachinery/pkg/util/wait.PollImmediateUntilWithContext.poller.func1.1"),
		goleak.IgnoreAnyFunction("k8s.io/client-go/util/workqueue.(*delayingType[...]).waitingLoop"),
		goleak.IgnoreAnyFunction("k8s.io/client-go/transport.(*dynamicClientCert).run"),
		goleak.IgnoreAnyFunction("k8s.io/client-go/transport.(*dynamicClientCert).processNextWorkItem"),
		goleak.IgnoreAnyFunction("k8s.io/apimachinery/pkg/util/wait.waitForWithContext"),
	)
}
