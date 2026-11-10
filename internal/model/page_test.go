package model

import (
	"reflect"
	"testing"
	"time"
)

// SameConnection must compare every field: a field added to Connection
// and left out would never reach the window's copy (ConnectionsSince).
func TestSameConnectionCoversEveryField(t *testing.T) {
	base := Connection{ID: "1", Chains: []string{"a"}, Start: time.Unix(1, 0)}
	typ := reflect.TypeOf(base)
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.Name == "ID" {
			continue
		}
		c := base
		c.Chains = []string{"a"}
		fv := reflect.ValueOf(&c).Elem().Field(i)
		switch fv.Kind() {
		case reflect.String:
			fv.SetString("x")
		case reflect.Int64:
			fv.SetInt(7)
		case reflect.Bool:
			fv.SetBool(true)
		case reflect.Slice:
			fv.Set(reflect.ValueOf([]string{"b"}))
		case reflect.Struct:
			fv.Set(reflect.ValueOf(time.Unix(2, 0)))
		default:
			t.Fatalf("field %s: kind %s not handled by the test", f.Name, fv.Kind())
		}
		if SameConnection(base, c) {
			t.Errorf("SameConnection ignores %s", f.Name)
		}
	}
	if !SameConnection(base, Connection{ID: "1", Chains: []string{"a"}, Start: time.Unix(1, 0)}) {
		t.Error("equal connections differ")
	}
}
