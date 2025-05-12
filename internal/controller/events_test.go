package cidrallocator

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"

	ipamv1 "github.com/appthrust/plexaubnet/api/v1alpha1"
)

// helper to drain one event from fake recorder and assert contains substrings
func expectEvent(t *testing.T, events <-chan string, wantReason string) {
	t.Helper()
	select {
	case e := <-events:
		if !strings.Contains(e, wantReason) {
			t.Errorf("expected event to contain %q, got %q", wantReason, e)
		}
	default:
		t.Fatalf("no event emitted, expected reason %s", wantReason)
	}
}

func TestEventEmitter(t *testing.T) {
	rec := record.NewFakeRecorder(5)
	sc := scheme.Scheme
	// Register our API type to scheme so fake recorder can encode object reference
	_ = ipamv1.AddToScheme(sc)

	emitter := NewEventEmitter(rec, sc)

	// Prepare claim and pool objects
	claim := &ipamv1.SubnetClaim{
		TypeMeta:   metav1.TypeMeta{Kind: "SubnetClaim", APIVersion: ipamv1.GroupVersion.String()},
		ObjectMeta: metav1.ObjectMeta{Name: "claim1", Namespace: "ns"},
	}
	pool := &ipamv1.SubnetPool{
		TypeMeta:   metav1.TypeMeta{Kind: "SubnetPool", APIVersion: ipamv1.GroupVersion.String()},
		ObjectMeta: metav1.ObjectMeta{Name: "pool1", Namespace: "ns"},
	}

	// Emit various events
	emitter.EmitAllocationSuccess(claim, "10.0.0.0/24")
	emitter.EmitAllocationConflict(claim, "overlaps")
	emitter.EmitPoolExhausted(claim, pool.Name)
	emitter.EmitValidationFailed(claim, "invalid request")
	emitter.EmitPoolEvent(pool, corev1.EventTypeNormal, EventReasonAllocated, "custom msg")

	// Verify each reason appears at least once
	expectEvent(t, rec.Events, EventReasonAllocated)
	expectEvent(t, rec.Events, EventReasonConflict)
	expectEvent(t, rec.Events, EventReasonPoolExhausted)
	expectEvent(t, rec.Events, EventReasonValidationFailed)

	// final custom event reason
	expectEvent(t, rec.Events, EventReasonAllocated)
}
