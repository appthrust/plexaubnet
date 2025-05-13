package cidrallocator

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

func TestSubnetEventsPredicate(t *testing.T) {
	pred := SubnetEvents()

	// Create event should be allowed
	ce := event.CreateEvent{Object: &corev1.ConfigMap{}}
	if !pred.Create(ce) {
		t.Errorf("expected CreateEvent to be allowed")
	}

	// Delete event should be allowed
	de := event.DeleteEvent{Object: &corev1.ConfigMap{}}
	if !pred.Delete(de) {
		t.Errorf("expected DeleteEvent to be allowed")
	}

	// Update with same generation should be skipped
	oldObj := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Generation: 1}}
	newObjSameGen := oldObj.DeepCopy()
	ueSame := event.UpdateEvent{ObjectOld: oldObj, ObjectNew: newObjSameGen}
	if pred.Update(ueSame) {
		t.Errorf("expected UpdateEvent with same generation to be filtered out")
	}

	// Update with changed generation should pass
	newObjNewGen := oldObj.DeepCopy()
	newObjNewGen.Generation = 2
	ueDiff := event.UpdateEvent{ObjectOld: oldObj, ObjectNew: newObjNewGen}
	if !pred.Update(ueDiff) {
		t.Errorf("expected UpdateEvent with different generation to pass")
	}
}

func TestDeletionOnlyPredicate(t *testing.T) {
	pred := DeletionOnly()

	if pred.Create(event.CreateEvent{}) {
		t.Errorf("CreateEvent should be blocked")
	}
	if !pred.Delete(event.DeleteEvent{}) {
		t.Errorf("DeleteEvent should be allowed")
	}
	if pred.Update(event.UpdateEvent{}) {
		t.Errorf("UpdateEvent should be blocked")
	}
	if pred.Generic(event.GenericEvent{}) {
		t.Errorf("GenericEvent should be blocked")
	}
}

func TestPoolStatusPred(t *testing.T) {
	pred := PoolStatusPred()

	// Update with same generation -> false
	obj := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Generation: 1}}
	ueSame := event.UpdateEvent{ObjectOld: obj, ObjectNew: obj.DeepCopy()}
	if pred.Update(ueSame) {
		t.Errorf("UpdateEvent with same generation should be filtered")
	}

	// Update with diff generation -> true
	newObj := obj.DeepCopy()
	newObj.Generation = 2
	ueDiff := event.UpdateEvent{ObjectOld: obj, ObjectNew: newObj}
	if !pred.Update(ueDiff) {
		t.Errorf("UpdateEvent with new generation should pass")
	}

	// External events should pass
	if !pred.Create(event.CreateEvent{}) {
		t.Errorf("CreateEvent should pass")
	}
	if !pred.Delete(event.DeleteEvent{}) {
		t.Errorf("DeleteEvent should pass")
	}
	if !pred.Generic(event.GenericEvent{}) {
		t.Errorf("GenericEvent should pass")
	}
}
