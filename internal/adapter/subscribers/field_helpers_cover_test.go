// field_helpers_cover_test.go — white-box tests for the map field-coercion
// helpers shared by the eventbus dispatchers (numField / intField /
// stringSliceField / strField / boolField / int64Field). The JSON decode path
// always yields float64 for numbers, but the binary-protodecode projectors can
// emit native int / int32 / int64, so each numeric branch is load-bearing.
package subscribers

import "testing"

func TestNumField_AllNumericKinds(t *testing.T) {
	cases := []struct {
		name string
		m    map[string]any
		key  string
		want float64
		ok   bool
	}{
		{"nil map", nil, "x", 0, false},
		{"missing key", map[string]any{"a": 1}, "x", 0, false},
		{"float64", map[string]any{"x": float64(3.5)}, "x", 3.5, true},
		{"float32", map[string]any{"x": float32(2)}, "x", 2, true},
		{"int", map[string]any{"x": int(7)}, "x", 7, true},
		{"int32", map[string]any{"x": int32(9)}, "x", 9, true},
		{"int64", map[string]any{"x": int64(11)}, "x", 11, true},
		{"non-numeric string", map[string]any{"x": "5"}, "x", 0, false},
		{"bool", map[string]any{"x": true}, "x", 0, false},
	}
	for _, c := range cases {
		got, ok := numField(c.m, c.key)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: numField = (%v,%v); want (%v,%v)", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestIntField(t *testing.T) {
	if got := intField(map[string]any{"x": float64(4)}, "x"); got != 4 {
		t.Errorf("intField float64 = %d; want 4", got)
	}
	if got := intField(map[string]any{"x": int64(8)}, "x"); got != 8 {
		t.Errorf("intField int64 = %d; want 8", got)
	}
	if got := intField(map[string]any{"x": "nope"}, "x"); got != 0 {
		t.Errorf("intField non-numeric = %d; want 0", got)
	}
	if got := intField(nil, "x"); got != 0 {
		t.Errorf("intField nil map = %d; want 0", got)
	}
}

func TestStringSliceField(t *testing.T) {
	cases := []struct {
		name string
		m    map[string]any
		key  string
		want []string
	}{
		{"nil map", nil, "x", nil},
		{"missing key", map[string]any{"a": 1}, "x", nil},
		{"wrong shape (string)", map[string]any{"x": "tag"}, "x", nil},
		{"empty slice", map[string]any{"x": []any{}}, "x", []string{}},
		{"mixed types — non-strings dropped", map[string]any{"x": []any{"a", 2, "b", true}}, "x", []string{"a", "b"}},
		{"all strings", map[string]any{"x": []any{"p", "q"}}, "x", []string{"p", "q"}},
	}
	for _, c := range cases {
		got := stringSliceField(c.m, c.key)
		if len(got) != len(c.want) {
			t.Errorf("%s: len = %d; want %d (%v)", c.name, len(got), len(c.want), got)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: [%d] = %q; want %q", c.name, i, got[i], c.want[i])
			}
		}
	}
}

func TestStrField(t *testing.T) {
	if got := strField(nil, "x"); got != "" {
		t.Errorf("strField nil = %q", got)
	}
	if got := strField(map[string]any{"x": "hi"}, "x"); got != "hi" {
		t.Errorf("strField = %q; want hi", got)
	}
	if got := strField(map[string]any{"x": 5}, "x"); got != "" {
		t.Errorf("strField non-string = %q; want empty", got)
	}
}

func TestBoolField(t *testing.T) {
	if boolField(nil, "x") {
		t.Error("boolField nil = true; want false")
	}
	if !boolField(map[string]any{"x": true}, "x") {
		t.Error("boolField true = false; want true")
	}
	if boolField(map[string]any{"x": "true"}, "x") {
		t.Error("boolField string = true; want false (non-bool)")
	}
}

func TestInt64Field(t *testing.T) {
	if got := int64Field(nil, "x"); got != 0 {
		t.Errorf("int64Field nil = %d", got)
	}
	if got := int64Field(map[string]any{"x": float64(99)}, "x"); got != 99 {
		t.Errorf("int64Field float64 = %d; want 99", got)
	}
	if got := int64Field(map[string]any{"x": "99"}, "x"); got != 0 {
		t.Errorf("int64Field non-numeric = %d; want 0", got)
	}
}
