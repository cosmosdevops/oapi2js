package convert

import (
	"strings"
	"testing"
)

func TestRankOrder(t *testing.T) {
	resource := NewMap()
	resource.Set("x-kubernetes-group-version-kind", NewList())
	helper := NewMap()

	// Best first.
	order := []struct {
		title string
		def   *Map
	}{
		{"io.k8s.api.core.v2.Thing", resource},
		{"io.k8s.api.apps.v2.Thing", resource},
		{"io.k8s.api.core.v1.Thing", resource},
		{"io.k8s.api.apps.v1.Thing", resource},
		{"io.k8s.api.apps.v2beta1.Thing", resource},
		{"io.k8s.api.apps.v1beta2.Thing", resource},
		{"io.k8s.api.apps.v1beta1.Thing", resource},
		{"io.k8s.api.apps.v1alpha1.Thing", resource},
		{"io.k8s.api.apps.notaversion.Thing", resource},
		{"io.k8s.api.core.v1.Thing", helper}, // any resource beats a helper type
		{"io.k8s.api.apps.v1alpha1.Thing", helper},
	}
	for i := 0; i+1 < len(order); i++ {
		better := rankOf(strings.Split(order[i].title, "."), order[i].def)
		worse := rankOf(strings.Split(order[i+1].title, "."), order[i+1].def)
		if !worse.less(better) || better.less(worse) {
			t.Errorf("%s (resource=%v) should beat %s (resource=%v)",
				order[i].title, order[i].def == resource, order[i+1].title, order[i+1].def == resource)
		}
	}

	same := rankOf(strings.Split("io.k8s.api.apps.v1.Thing", "."), resource)
	if same.less(same) {
		t.Error("equal ranks must tie, so the later definition wins as in the Python tool")
	}
}
