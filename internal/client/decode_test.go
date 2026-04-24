package client

import "testing"

func TestDecodeInstanceList_Variants(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want int
	}{
		{name: "nil", in: nil, want: 0},
		{name: "single object", in: map[string]any{"id": 1, "instance_name": "a"}, want: 1},
		{name: "list", in: []map[string]any{{"id": 1, "instance_name": "a"}, {"id": 2, "instance_name": "b"}}, want: 2},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeInstanceList(tc.in)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != tc.want {
				t.Fatalf("want len %d, got %d", tc.want, len(got))
			}
		})
	}
}

func TestDecodeInstance_Invalid(t *testing.T) {
	_, err := decodeInstance("not-json-object")
	if err == nil {
		t.Fatal("expected error")
	}
}
