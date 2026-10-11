package ui

import "testing"

func TestRotationFor(t *testing.T) {
	rem := map[string]int{"/a.jpg": 1, "/up.jpg": 0, "/odd.jpg": -1}
	cases := []struct {
		name    string
		path    string
		locked  bool
		current int
		want    int
	}{
		{"remembered, unlocked", "/a.jpg", false, 0, 1},
		{"remembered beats lock", "/a.jpg", true, 2, 1},
		{"remembered 0 beats lock", "/up.jpg", true, 3, 0},
		{"remembered normalised", "/odd.jpg", false, 0, 3},
		{"lock carries current", "/new.jpg", true, 2, 2},
		{"lock carries normalised", "/new.jpg", true, 5, 1},
		{"default 0", "/new.jpg", false, 3, 0},
	}
	for _, c := range cases {
		if got := rotationFor(rem, c.path, c.locked, c.current); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
	if got := rotationFor(nil, "/a.jpg", false, 2); got != 0 {
		t.Errorf("nil map: got %d, want 0", got)
	}
}

// The UI applies rotationFor after View.SetImage; in Fit mode the result
// must be re-fitted for the rotated size.
func TestSetRotAfterSetImageFit(t *testing.T) {
	v := View{VW: 1000, VH: 1000}
	v.SetImage(2000, 1000) // fit: 0.5
	v.SetRot(rotationFor(map[string]int{"/p": 1}, "/p", false, v.Rot))
	if v.Rot != 1 || v.Mode != ModeFit {
		t.Fatalf("rot %d mode %d", v.Rot, v.Mode)
	}
	if !near(v.Scale, 0.5) || !near(v.OX, 250) || !near(v.OY, 0) {
		t.Errorf("not re-fitted: scale %v at (%v,%v)", v.Scale, v.OX, v.OY)
	}

	// Locked with a carried rotation of 1: a remembered 0 overrides it and
	// re-fits for the upright size.
	v = View{VW: 1000, VH: 500, Locked: true}
	v.SetImage(400, 1000)
	v.Rotate(1)
	v.SetImage(2000, 1000)
	if v.Rot != 1 {
		t.Fatalf("lock did not carry rotation: %d", v.Rot)
	}
	v.SetRot(rotationFor(map[string]int{"/q": 0}, "/q", v.Locked, v.Rot))
	if v.Rot != 0 || !near(v.Scale, 0.5) || !near(v.OX, 0) || !near(v.OY, 0) {
		t.Errorf("rot %d scale %v at (%v,%v)", v.Rot, v.Scale, v.OX, v.OY)
	}
}

func TestSetRotOneKeepsCentre(t *testing.T) {
	v := View{VW: 800, VH: 600}
	v.SetImage(400, 200)
	v.SetOne()
	v.SetRot(3)
	if v.Rot != 3 || v.Scale != 1 || !near(v.OX, 300) || !near(v.OY, 100) {
		t.Errorf("rot %d scale %v at (%v,%v)", v.Rot, v.Scale, v.OX, v.OY)
	}
	before := v
	v.SetRot(7) // same rotation: no-op
	if v != before {
		t.Errorf("SetRot to the same rotation changed the view: %+v", v)
	}
}
